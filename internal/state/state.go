// Package state owns pluto's local state directory: box records and their
// on-disk homes. It is single-writer: the daemon holds an exclusive lock for
// its lifetime (ADR 0005), and all mutations are atomic file replaces.
package state

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// StateVersion is the layout version of the state directory.
	StateVersion = 1
	// RecordSchema is the schema version of a box record.
	RecordSchema = 1
)

// ErrNotFound reports a box that does not exist.
var ErrNotFound = errors.New("box not found")

// BoxState is the lifecycle state of a box. In v1 a box is created without a
// machine; the runner moves it to running and paused (ADR 0002).
type BoxState string

const (
	StateCreated BoxState = "created"
	StateRunning BoxState = "running"
	StatePaused  BoxState = "paused"
	StateFailed  BoxState = "failed"
)

var transitions = map[BoxState][]BoxState{
	StateCreated: {StateRunning, StateFailed},
	StateRunning: {StatePaused, StateFailed},
	StatePaused:  {StateRunning, StateFailed},
	StateFailed:  {StateRunning},
}

// Valid reports whether s is a known box state.
func (s BoxState) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// CanTransition reports whether a box may move from s to next. Moving to the
// same state is a no-op and allowed.
func (s BoxState) CanTransition(next BoxState) bool {
	if s == next {
		return true
	}
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Box is one durable work machine.
type Box struct {
	Schema    int       `json:"schema"`
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	Worktree  string    `json:"worktree"`
	State     BoxState  `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the daemon's handle on the state directory.
type Store struct {
	root string
	mu   sync.Mutex
	lock *os.File
}

// RecordError is a box record that exists but cannot be read. Listings and
// status surface these instead of hiding or crashing on them.
type RecordError struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

// Open prepares the state directory at root and takes the daemon's exclusive
// lock. It fails if another process holds the lock or the layout version is
// unsupported.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "boxes"), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := ensureVersion(root); err != nil {
		return nil, err
	}
	lock, err := lockDir(root)
	if err != nil {
		return nil, err
	}
	return &Store{root: root, lock: lock}, nil
}

// Close releases the daemon lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

// Root returns the state directory this store owns.
func (s *Store) Root() string { return s.root }

// Boxes returns all readable box records, sorted by creation time, plus one
// RecordError per unreadable record. Corrupt records never fail the listing.
func (s *Store) Boxes() ([]*Box, []RecordError, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

// Transition moves a box to next, validating the lifecycle, and persists it.
func (s *Store) Transition(id string, next BoxState) (*Box, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid box id %q", id)
	}
	if !next.Valid() {
		return nil, fmt.Errorf("unknown box state %q", next)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	box, err := readBox(s.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !box.State.CanTransition(next) {
		return nil, fmt.Errorf("box %s cannot move from %s to %s", box.ID, box.State, next)
	}
	box.State = next
	box.UpdatedAt = time.Now().UTC()
	if err := s.writeBox(box); err != nil {
		return nil, err
	}
	return box, nil
}

// DestroyBox removes a box's record and its disk directory.
func (s *Store) DestroyBox(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid box id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.boxDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	return os.RemoveAll(dir)
}

// CreateBox creates a box for a worktree, or returns the existing one. A
// worktree has one primary box, so create is idempotent per worktree path.
func (s *Store) CreateBox(project, branch, worktree string) (*Box, bool, error) {
	if worktree == "" {
		return nil, false, errors.New("worktree is required")
	}
	worktree = filepath.Clean(worktree)
	s.mu.Lock()
	defer s.mu.Unlock()

	boxes, recordErrs, err := s.listLocked()
	if err != nil {
		return nil, false, err
	}
	if len(recordErrs) > 0 {
		return nil, false, fmt.Errorf("unreadable box record: %s", recordErrs[0].Err)
	}
	for _, existing := range boxes {
		if existing.Worktree == worktree {
			return existing, false, nil
		}
	}

	now := time.Now().UTC()
	box := &Box{
		Schema:    RecordSchema,
		ID:        newID(),
		Project:   project,
		Branch:    branch,
		Worktree:  worktree,
		State:     StateCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}
	dir := s.boxDir(box.ID)
	if err := os.MkdirAll(filepath.Join(dir, "disk"), 0o755); err != nil {
		return nil, false, fmt.Errorf("create box dir: %w", err)
	}
	if err := s.writeBox(box); err != nil {
		os.RemoveAll(dir)
		return nil, false, err
	}
	return box, true, nil
}

// Box returns one box record by ID.
func (s *Store) Box(id string) (*Box, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid box id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	box, err := readBox(s.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return box, err
}

func (s *Store) boxDir(id string) string     { return filepath.Join(s.root, "boxes", id) }
func (s *Store) recordPath(id string) string { return filepath.Join(s.boxDir(id), "box.json") }

func (s *Store) listLocked() ([]*Box, []RecordError, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "boxes"))
	if err != nil {
		return nil, nil, fmt.Errorf("read boxes dir: %w", err)
	}
	var boxes []*Box
	var recordErrs []RecordError
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(s.root, "boxes", e.Name(), "box.json")
		box, err := readBox(path)
		if err != nil {
			recordErrs = append(recordErrs, RecordError{Path: path, Err: err.Error()})
			continue
		}
		boxes = append(boxes, box)
	}
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].CreatedAt.Before(boxes[j].CreatedAt) })
	return boxes, recordErrs, nil
}

func (s *Store) writeBox(box *Box) error {
	data, err := json.MarshalIndent(box, "", "  ")
	if err != nil {
		return fmt.Errorf("encode box record: %w", err)
	}
	data = append(data, '\n')
	return writeFileAtomic(s.recordPath(box.ID), data, 0o644)
}

func readBox(path string) (*Box, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var box Box
	if err := json.Unmarshal(data, &box); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if box.Schema != RecordSchema {
		return nil, fmt.Errorf("%s: unsupported record schema %d", path, box.Schema)
	}
	if !ValidID(box.ID) {
		return nil, fmt.Errorf("%s: invalid box id %q", path, box.ID)
	}
	if !box.State.Valid() {
		return nil, fmt.Errorf("%s: invalid box state %q", path, box.State)
	}
	return &box, nil
}

func ensureVersion(root string) error {
	path := filepath.Join(root, "version")
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if strings.TrimSpace(string(data)) != fmt.Sprint(StateVersion) {
			return fmt.Errorf("state directory %s has version %q, pluto supports %d", root, strings.TrimSpace(string(data)), StateVersion)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return writeFileAtomic(path, []byte(fmt.Sprint(StateVersion)+"\n"), 0o644)
	default:
		return fmt.Errorf("read state version: %w", err)
	}
}

func lockDir(root string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(root, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another pluto process is using %s", root)
	}
	return f, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	return d.Sync()
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}
