// Package store keeps the host's state in SQLite. The host is its only writer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver, so the binary needs no cgo.
)

// Run states.
const (
	StateWaiting   = "waiting" // Box is up, and no job has reached it yet.
	StateRunning   = "running"
	StatePassed    = "passed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateRefused   = "refused"
	StateLost      = "lost"    // The box disappeared or stopped during the run.
	StateExpired   = "expired" // No job arrived before the box's time limit.
)

// ErrNotFound means no such record exists.
var ErrNotFound = errors.New("not found")

// Repo is a repository connected to this host.
type Repo struct {
	Name        string    `json:"name"`
	Token       string    `json:"-"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Run is one CI job's box and outcome.
type Run struct {
	ID          string    `json:"id"`
	Repo        string    `json:"repo"`
	Box         string    `json:"box"`
	RunnerID    int64     `json:"-"`
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	GitHubRunID int64     `json:"github_run_id,omitempty"`
	Workflow    string    `json:"workflow,omitempty"`
	Job         string    `json:"job,omitempty"`
	Event       string    `json:"event,omitempty"`
	Ref         string    `json:"ref,omitempty"`
	SHA         string    `json:"sha,omitempty"`
	Workspace   string    `json:"workspace,omitempty"`
	BoxKept     bool      `json:"box_kept"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	FinishedAt  time.Time `json:"finished_at,omitzero"`
	KeepUntil   time.Time `json:"keep_until,omitzero"`
}

// Active reports whether the run still occupies a slot.
func (r Run) Active() bool { return r.State == StateWaiting || r.State == StateRunning }

// Store is the host's database.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS repos (
	name TEXT PRIMARY KEY, token TEXT NOT NULL, connected_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
	id TEXT PRIMARY KEY, repo TEXT NOT NULL, box TEXT NOT NULL, runner_id INTEGER NOT NULL,
	state TEXT NOT NULL, reason TEXT NOT NULL, github_run_id INTEGER NOT NULL,
	workflow TEXT NOT NULL, job TEXT NOT NULL, event TEXT NOT NULL, ref TEXT NOT NULL,
	sha TEXT NOT NULL, workspace TEXT NOT NULL, box_kept INTEGER NOT NULL,
	created_at INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL,
	keep_until INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_created ON runs (created_at DESC);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

// Open opens or creates the database at path. It holds tokens, so the file is
// created private first: SQLite gives its -wal and -shm files the same mode.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// PutRepo connects a repo, or replaces its token.
func (s *Store) PutRepo(ctx context.Context, r Repo) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO repos (name, token, connected_at) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET token = excluded.token`, r.Name, r.Token, r.ConnectedAt.Unix())
	return err
}

// Repos lists connected repos.
func (s *Store) Repos(ctx context.Context) ([]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, token, connected_at FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		var at int64
		if err := rows.Scan(&r.Name, &r.Token, &at); err != nil {
			return nil, err
		}
		r.ConnectedAt = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Repo returns one connected repo.
func (s *Store) Repo(ctx context.Context, name string) (Repo, error) {
	r := Repo{Name: name}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT token, connected_at FROM repos WHERE name = ?`, name).Scan(&r.Token, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, ErrNotFound
	}
	if err != nil {
		return Repo{}, err
	}
	r.ConnectedAt = time.Unix(at, 0)
	return r, nil
}

// HostID returns this host's short, stable ID, creating it with newID on first use.
// Several hosts can serve one repo, so each recognises only its own runners by it.
func (s *Store) HostID(ctx context.Context, newID func() string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'host_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = newID()
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('host_id', ?)`, id)
	return id, err
}

// Prune deletes the oldest finished runs beyond keep, so the database can't grow forever.
func (s *Store) Prune(ctx context.Context, keep int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE state NOT IN (?, ?) AND box_kept = 0 AND id NOT IN
		(SELECT id FROM runs ORDER BY created_at DESC, id LIMIT ?)`, StateWaiting, StateRunning, keep)
	return err
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

// PutRun inserts or replaces a run.
func (s *Store) PutRun(ctx context.Context, r Run) error {
	kept := 0
	if r.BoxKept {
		kept = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO runs
		(id, repo, box, runner_id, state, reason, github_run_id, workflow, job, event, ref, sha,
		 workspace, box_kept, created_at, started_at, finished_at, keep_until)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Repo, r.Box, r.RunnerID, r.State, r.Reason, r.GitHubRunID, r.Workflow, r.Job, r.Event,
		r.Ref, r.SHA, r.Workspace, kept, unix(r.CreatedAt), unix(r.StartedAt), unix(r.FinishedAt), unix(r.KeepUntil))
	return err
}

const runColumns = `id, repo, box, runner_id, state, reason, github_run_id, workflow, job, event, ref, sha,
	workspace, box_kept, created_at, started_at, finished_at, keep_until`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var kept int
	var created, started, finished, keep int64
	err := row.Scan(&r.ID, &r.Repo, &r.Box, &r.RunnerID, &r.State, &r.Reason, &r.GitHubRunID, &r.Workflow,
		&r.Job, &r.Event, &r.Ref, &r.SHA, &r.Workspace, &kept, &created, &started, &finished, &keep)
	if err != nil {
		return Run{}, err
	}
	r.BoxKept = kept == 1
	r.CreatedAt, r.StartedAt, r.FinishedAt, r.KeepUntil = fromUnix(created), fromUnix(started), fromUnix(finished), fromUnix(keep)
	return r, nil
}

func (s *Store) queryRuns(ctx context.Context, query string, args ...any) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM runs `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Runs lists recent runs, newest first. Boxes that never received a job are left out.
func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	return s.queryRuns(ctx, `WHERE state != ? ORDER BY created_at DESC, id LIMIT ?`, StateExpired, limit)
}

// Live lists runs that hold a box: active ones and kept failures, oldest first.
func (s *Store) Live(ctx context.Context) ([]Run, error) {
	return s.queryRuns(ctx, `WHERE state IN (?, ?) OR box_kept = 1 ORDER BY created_at, id`, StateWaiting, StateRunning)
}

// Run returns one run.
func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}
