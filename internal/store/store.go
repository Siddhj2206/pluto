// Package store keeps the host's state in SQLite. The host is its only writer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver, so the binary needs no cgo.
)

// Run states.
const (
	StateQueued    = "queued"  // A task waiting for a free slot.
	StateWaiting   = "waiting" // CI: box is up and no job has reached it yet. Task: box is being prepared.
	StateRunning   = "running"
	StatePassed    = "passed"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
	StateRefused   = "refused"
	StateLost      = "lost"    // The box disappeared or stopped during the run.
	StateExpired   = "expired" // No job arrived before the box's time limit.
	StateSkipped   = "skipped" // A scheduled slot passed while the task was still going.
)

// ErrNotFound means no such record exists.
var ErrNotFound = errors.New("not found")

// Repo is a repository connected to this host.
type Repo struct {
	Name        string    `json:"name"`
	Token       string    `json:"-"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Kinds of run.
const (
	KindJob  = "" // A CI job, defined by the git host.
	KindTask = "task"
)

// Triggers say why a task ran.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	// TriggerBranch is a person asking for what the schedule would run, now.
	TriggerBranch = "branch"
)

// Run is one CI job's or task's box and outcome.
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
	Kind        string    `json:"kind,omitempty"`
	Trigger     string    `json:"trigger,omitempty"`
	// Note records something true about the run that isn't its result, such as
	// the machine sleeping through part of it.
	Note     string    `json:"note,omitempty"`
	Deadline time.Time `json:"deadline,omitzero"`
	// Waiting says why a queued run hasn't started, and WaitingNext is the
	// command that shows more. The host works both out on each pass, so they
	// aren't stored.
	Waiting     string `json:"waiting,omitempty"`
	WaitingNext string `json:"waiting_next,omitempty"`
	// Spec is a task run's plan as JSON, and Upload is its copy of a working tree.
	Spec   string `json:"-"`
	Upload string `json:"-"`
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
CREATE TABLE IF NOT EXISTS secrets (
	repo TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (repo, name)
);
CREATE TABLE IF NOT EXISTS grants (
	repo TEXT NOT NULL, name TEXT NOT NULL, PRIMARY KEY (repo, name)
);
CREATE TABLE IF NOT EXISTS boxes (
	name TEXT PRIMARY KEY, repo TEXT NOT NULL, image TEXT NOT NULL, privileged INTEGER NOT NULL,
	workspace_volume TEXT NOT NULL, home_volume TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS box_volumes (
	box TEXT NOT NULL, path TEXT NOT NULL, volume TEXT NOT NULL, PRIMARY KEY (box, path)
);
CREATE TABLE IF NOT EXISTS sources (
	repo TEXT PRIMARY KEY, url TEXT NOT NULL, branch TEXT NOT NULL, sha TEXT NOT NULL,
	config TEXT NOT NULL, problem TEXT NOT NULL, fetched_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS schedules (
	repo TEXT NOT NULL, task TEXT NOT NULL, expr TEXT NOT NULL, checked_at INTEGER NOT NULL,
	PRIMARY KEY (repo, task)
);
`

// addedRunColumns came after the first release of the runs table.
var addedRunColumns = []string{
	`kind TEXT NOT NULL DEFAULT ''`,
	`trigger TEXT NOT NULL DEFAULT ''`,
	`note TEXT NOT NULL DEFAULT ''`,
	`deadline INTEGER NOT NULL DEFAULT 0`,
	`spec TEXT NOT NULL DEFAULT ''`,
	`upload TEXT NOT NULL DEFAULT ''`,
}

func migrate(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('runs')`)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, col := range addedRunColumns {
		name, _, _ := strings.Cut(col, " ")
		if have[name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE runs ADD COLUMN ` + col); err != nil {
			return err
		}
	}
	return lowercaseRepos(db)
}

// githubKey is the SQL test for a GitHub-style repo key: owner/name, with no
// dot in the owner (so not host/owner/name or a path) and not local/....
// Everything else keeps its spelling, since its names can be case-sensitive.
const githubKey = `(%[1]s LIKE '%%/%%' AND %[1]s NOT LIKE '%%/%%/%%' AND %[1]s NOT LIKE 'local/%%' AND substr(%[1]s, 1, instr(%[1]s, '/') - 1) NOT LIKE '%%.%%')`

// lowercaseRepos gives every GitHub repo one spelling, since GitHub ignores case
// and two spellings would otherwise mean two boxes. Local and other hosts' keys
// stay as they are, and so do box names, which the boxes table keeps. Where both
// spellings already own a row, the lowercase one wins and the other is dropped
// (repos hold no timestamp of when a token was replaced, so none is newer).
func lowercaseRepos(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []struct {
		table, col string
		keys       []string // The rest of the primary key, if the table has one.
		unique     bool
	}{
		{"repos", "name", nil, true}, {"runs", "repo", nil, false}, {"secrets", "repo", []string{"name"}, true},
		{"grants", "repo", []string{"name"}, true}, {"boxes", "repo", nil, false},
		{"sources", "repo", nil, true}, {"schedules", "repo", []string{"task"}, true},
	} {
		cond := fmt.Sprintf(`%[1]s != lower(%[1]s) AND `+githubKey, t.col)
		if t.unique {
			twin := fmt.Sprintf(`t2.%[1]s = lower(%[2]s.%[1]s)`, t.col, t.table)
			for _, k := range t.keys {
				twin += fmt.Sprintf(` AND t2.%[1]s = %[2]s.%[1]s`, k, t.table)
			}
			q := fmt.Sprintf(`DELETE FROM %[1]s WHERE %[2]s AND EXISTS (SELECT 1 FROM %[1]s t2 WHERE %[3]s)`, t.table, cond, twin)
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		q := fmt.Sprintf(`UPDATE %[1]s SET %[2]s = lower(%[2]s) WHERE %[3]s`, t.table, t.col, cond)
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("updating %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// PutRepo connects a repo, or replaces its token.
func (s *Store) PutRepo(ctx context.Context, r Repo) error {
	r.Name = strings.ToLower(r.Name) // GitHub names ignore case, so one repo gets one row.
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
	err := s.db.QueryRowContext(ctx, `SELECT token, connected_at FROM repos WHERE name = ? COLLATE NOCASE`, name).Scan(&r.Token, &at)
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE state NOT IN (?, ?, ?) AND box_kept = 0 AND id NOT IN
		(SELECT id FROM runs ORDER BY created_at DESC, id LIMIT ?)`, StateQueued, StateWaiting, StateRunning, keep)
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
		 workspace, box_kept, created_at, started_at, finished_at, keep_until,
		 kind, trigger, note, deadline, spec, upload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Repo, r.Box, r.RunnerID, r.State, r.Reason, r.GitHubRunID, r.Workflow, r.Job, r.Event,
		r.Ref, r.SHA, r.Workspace, kept, unix(r.CreatedAt), unix(r.StartedAt), unix(r.FinishedAt), unix(r.KeepUntil),
		r.Kind, r.Trigger, r.Note, unix(r.Deadline), r.Spec, r.Upload)
	return err
}

const runColumns = `id, repo, box, runner_id, state, reason, github_run_id, workflow, job, event, ref, sha,
	workspace, box_kept, created_at, started_at, finished_at, keep_until,
	kind, trigger, note, deadline, spec, upload`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var kept int
	var created, started, finished, keep, deadline int64
	err := row.Scan(&r.ID, &r.Repo, &r.Box, &r.RunnerID, &r.State, &r.Reason, &r.GitHubRunID, &r.Workflow,
		&r.Job, &r.Event, &r.Ref, &r.SHA, &r.Workspace, &kept, &created, &started, &finished, &keep,
		&r.Kind, &r.Trigger, &r.Note, &deadline, &r.Spec, &r.Upload)
	if err != nil {
		return Run{}, err
	}
	r.BoxKept = kept == 1
	r.CreatedAt, r.StartedAt, r.FinishedAt, r.KeepUntil = fromUnix(created), fromUnix(started), fromUnix(finished), fromUnix(keep)
	r.Deadline = fromUnix(deadline)
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

// MoveQueued moves a queued run to another state, and reports whether it was
// still queued. The loop and a cancel can reach the same run together, and this
// lets exactly one of them win.
func (s *Store) MoveQueued(ctx context.Context, id, state string, at time.Time) (bool, error) {
	var finished int64
	if state != StateWaiting {
		finished = unix(at)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET state = ?, finished_at = ?, upload = CASE WHEN ? = ? THEN upload ELSE '' END
		WHERE id = ? AND state = ?`, state, finished, state, StateWaiting, id, StateQueued)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RunsSince lists runs created at or after t, newest first.
func (s *Store) RunsSince(ctx context.Context, t time.Time, limit int) ([]Run, error) {
	return s.queryRuns(ctx, `WHERE state != ? AND created_at >= ? ORDER BY created_at DESC, id LIMIT ?`, StateExpired, t.Unix(), limit)
}

// Queued lists task runs waiting for a slot, oldest first.
func (s *Store) Queued(ctx context.Context) ([]Run, error) {
	return s.queryRuns(ctx, `WHERE state = ? ORDER BY created_at, id`, StateQueued)
}

// LastTaskRun returns the newest run of a task that actually ran, if any.
func (s *Store) LastTaskRun(ctx context.Context, repo, task string) (Run, error) {
	runs, err := s.queryRuns(ctx, `WHERE kind = ? AND repo = ? AND job = ? AND state != ? ORDER BY created_at DESC, id LIMIT 1`, KindTask, repo, task, StateSkipped)
	if err != nil {
		return Run{}, err
	}
	if len(runs) == 0 {
		return Run{}, ErrNotFound
	}
	return runs[0], nil
}

// RunIDs lists every run's ID, so files that outlived their run can be found.
func (s *Store) RunIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
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

// Meta reads a host setting, returning "" when it isn't set.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta writes a host setting.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetSecret stores a repo's secret, replacing any earlier value.
func (s *Store) SetSecret(ctx context.Context, repo, name, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets (repo, name, value) VALUES (?, ?, ?)
		ON CONFLICT (repo, name) DO UPDATE SET value = excluded.value`, repo, name, value)
	return err
}

// DeleteSecret removes a repo's secret, and reports whether it existed.
func (s *Store) DeleteSecret(ctx context.Context, repo, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE repo = ? AND name = ?`, repo, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SecretNames lists a repo's secrets by name. Values never leave the host's
// runs, so nothing here returns them.
func (s *Store) SecretNames(ctx context.Context, repo string) ([]string, error) {
	values, err := s.Secrets(ctx, repo)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// Secrets returns a repo's secret values, for giving to its boxes.
func (s *Store) Secrets(ctx context.Context, repo string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM secrets WHERE repo = ?`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// Grant lets a repo use a privilege on this host.
func (s *Store) Grant(ctx context.Context, repo, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO grants (repo, name) VALUES (?, ?)`, repo, name)
	return err
}

// Revoke takes a privilege back.
func (s *Store) Revoke(ctx context.Context, repo, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE repo = ? AND name = ?`, repo, name)
	return err
}

// Granted reports whether a repo holds a privilege.
func (s *Store) Granted(ctx context.Context, repo, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM grants WHERE repo = ? AND name = ?`, repo, name).Scan(&n)
	return n > 0, err
}

// Box is a durable box and the settings it was created with.
type Box struct {
	Name            string `json:"name"`
	Repo            string `json:"repo"`
	Image           string `json:"image"`
	Privileged      bool   `json:"privileged"`
	WorkspaceVolume string `json:"-"`
	HomeVolume      string `json:"-"`
	// DataVolumes maps a path an image declares as a VOLUME to the volume that
	// holds it. It is empty for boxes made before these were kept.
	DataVolumes map[string]string `json:"-"`
	CreatedAt   time.Time         `json:"created_at"`
}

// AllVolumes lists every volume the box owns, mounted or not.
func (b Box) AllVolumes() []string {
	vols := []string{b.WorkspaceVolume, b.HomeVolume}
	for _, path := range slices.Sorted(maps.Keys(b.DataVolumes)) {
		vols = append(vols, b.DataVolumes[path])
	}
	return vols
}

// PutBox records a durable box.
func (s *Store) PutBox(ctx context.Context, b Box) error {
	privileged := 0
	if b.Privileged {
		privileged = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO boxes
		(name, repo, image, privileged, workspace_volume, home_volume, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		b.Name, b.Repo, b.Image, privileged, b.WorkspaceVolume, b.HomeVolume, b.CreatedAt.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM box_volumes WHERE box = ?`, b.Name); err != nil {
		return err
	}
	for path, volume := range b.DataVolumes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO box_volumes (box, path, volume) VALUES (?, ?, ?)`, b.Name, path, volume); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) queryBoxes(ctx context.Context, query string, args ...any) ([]Box, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, repo, image, privileged, workspace_volume, home_volume, created_at FROM boxes `+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Box
	for rows.Next() {
		var b Box
		var privileged int
		var at int64
		if err := rows.Scan(&b.Name, &b.Repo, &b.Image, &privileged, &b.WorkspaceVolume, &b.HomeVolume, &at); err != nil {
			return nil, err
		}
		b.Privileged, b.CreatedAt = privileged == 1, time.Unix(at, 0)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		vols, err := s.db.QueryContext(ctx, `SELECT path, volume FROM box_volumes WHERE box = ?`, out[i].Name)
		if err != nil {
			return nil, err
		}
		for vols.Next() {
			var path, volume string
			if err := vols.Scan(&path, &volume); err != nil {
				vols.Close()
				return nil, err
			}
			if out[i].DataVolumes == nil {
				out[i].DataVolumes = map[string]string{}
			}
			out[i].DataVolumes[path] = volume
		}
		err = vols.Err()
		vols.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Boxes lists durable boxes, optionally for one repo.
func (s *Store) Boxes(ctx context.Context, repo string) ([]Box, error) {
	if repo == "" {
		return s.queryBoxes(ctx, `ORDER BY repo, name`)
	}
	return s.queryBoxes(ctx, `WHERE repo = ? ORDER BY name`, repo)
}

// BoxByName returns one durable box.
func (s *Store) BoxByName(ctx context.Context, name string) (Box, error) {
	boxes, err := s.queryBoxes(ctx, `WHERE name = ?`, name)
	if err != nil {
		return Box{}, err
	}
	if len(boxes) == 0 {
		return Box{}, ErrNotFound
	}
	return boxes[0], nil
}

// DeleteBox forgets a durable box.
func (s *Store) DeleteBox(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM box_volumes WHERE box = ?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM boxes WHERE name = ?`, name); err != nil {
		return err
	}
	return tx.Commit()
}

// Source is where a repo's scheduled tasks come from: a branch of its remote.
type Source struct {
	Repo      string
	URL       string
	Branch    string
	SHA       string
	Config    string // The last config that parsed, as JSON.
	Problem   string // Why the last refresh failed, if it did.
	FetchedAt time.Time
}

// PutSource adds or updates a repo's source.
func (s *Store) PutSource(ctx context.Context, src Source) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO sources
		(repo, url, branch, sha, config, problem, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		src.Repo, src.URL, src.Branch, src.SHA, src.Config, src.Problem, unix(src.FetchedAt))
	return err
}

// Sources lists repos whose schedules are enabled.
func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo, url, branch, sha, config, problem, fetched_at FROM sources ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		var at int64
		if err := rows.Scan(&src.Repo, &src.URL, &src.Branch, &src.SHA, &src.Config, &src.Problem, &at); err != nil {
			return nil, err
		}
		src.FetchedAt = fromUnix(at)
		out = append(out, src)
	}
	return out, rows.Err()
}

// SourceOf returns one repo's source.
func (s *Store) SourceOf(ctx context.Context, repo string) (Source, error) {
	sources, err := s.Sources(ctx)
	if err != nil {
		return Source{}, err
	}
	for _, src := range sources {
		if src.Repo == repo {
			return src, nil
		}
	}
	return Source{}, ErrNotFound
}

// DeleteSource disables a repo's schedules.
func (s *Store) DeleteSource(ctx context.Context, repo string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE repo = ?`, repo); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE repo = ?`, repo)
	return err
}

// Schedule tracks how far a scheduled task has been checked.
type Schedule struct {
	Repo      string
	Task      string
	Expr      string
	CheckedAt time.Time
}

// Schedules lists all schedules.
func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo, task, expr, checked_at FROM schedules ORDER BY repo, task`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var at int64
		if err := rows.Scan(&sc.Repo, &sc.Task, &sc.Expr, &at); err != nil {
			return nil, err
		}
		sc.CheckedAt = fromUnix(at)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// PutSchedule adds a schedule or updates how far it has been checked.
func (s *Store) PutSchedule(ctx context.Context, sc Schedule) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO schedules (repo, task, expr, checked_at) VALUES (?, ?, ?, ?)`,
		sc.Repo, sc.Task, sc.Expr, unix(sc.CheckedAt))
	return err
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(ctx context.Context, repo, task string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE repo = ? AND task = ?`, repo, task)
	return err
}
