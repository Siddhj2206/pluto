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

// BoxState is the lifecycle state of a container box. A box moves
// created -> running -> (frozen) -> stopped, and a failed wake enters
// wake_failed, retained for inspection (#138, #146).
type BoxState string

const (
	StateCreated BoxState = "created"
	StateRunning BoxState = "running"
	StateFrozen  BoxState = "frozen"
	StateStopped BoxState = "stopped"
	// StateWakeFailed is a box whose wake phase failed: it is retained for
	// inspection, no run starts, and the failing step is surfaced (#146). The
	// next work retries the wake; the box can also be stopped or removed.
	StateWakeFailed BoxState = "wake_failed"
	StateFailed     BoxState = "failed"
)

var transitions = map[BoxState][]BoxState{
	StateCreated:    {StateRunning, StateStopped, StateWakeFailed, StateFailed},
	StateRunning:    {StateFrozen, StateStopped, StateWakeFailed, StateFailed},
	StateFrozen:     {StateRunning, StateStopped, StateWakeFailed, StateFailed},
	StateStopped:    {StateRunning, StateWakeFailed, StateFailed},
	StateWakeFailed: {StateRunning, StateStopped, StateFailed},
	StateFailed:     {StateRunning, StateStopped},
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

// Box is one durable work machine: a container whose root is composed from
// layers, with durable volumes outside that stack (ADR 0014, ADR 0017). The
// core names only a workspace, repo, layers, volumes, and state; it never
// names a branch or worktree (ADR 0015).
type Box struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	// WorkspacePath identifies the workspace a box belongs to.
	WorkspacePath string `json:"workspace"`
	// Repo is the workspace's repo identity as the vcs adapter resolved it.
	Repo string `json:"repo,omitempty"`
	// Revision is the content the box was seeded from, as the vcs adapter
	// resolved it before the box booted.
	Revision string `json:"revision,omitempty"`
	// EngineID is the container engine's handle for the box's container.
	EngineID string `json:"engine_id,omitempty"`
	// Layers are the content-addressed layers a box's root composes.
	Layers []Layer `json:"layers,omitempty"`
	// Volumes are the durable volumes mounted outside the layer stack.
	Volumes []Volume `json:"volumes,omitempty"`
	State   BoxState `json:"state"`
	// Ephemeral marks a box created for one CI run (#148): it is seeded from
	// the workspace's package layer, mounts no durable volumes, and is
	// discarded on success or retained for inspection on failure.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// RetainedAt is when an ephemeral box's failed run finished and the box
	// was kept for inspection. Nil while the run is in flight and for every
	// durable box. Retained boxes are evicted at the earlier of an age and a
	// count unless pinned.
	RetainedAt *time.Time `json:"retained_at,omitempty"`
	// Pinned exempts a box from retention eviction (#148).
	Pinned bool    `json:"pinned,omitempty"`
	Phases *Phases `json:"phases,omitempty"`
	// Schedules are the contract's alarms, stored when the box applied it and
	// fired by the daemon's scheduler loop (ADR 0003).
	Schedules []Schedule `json:"schedules,omitempty"`
	// IdleSince is when the box was last observed with nothing attached and
	// no run active. Nil means busy or not yet evaluated. It resets on every
	// state transition, so a wake always gets a fresh window.
	IdleSince *time.Time `json:"idle_since,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// PhaseState is the state of a contract phase.
type PhaseState string

const (
	PhasePending PhaseState = "pending"
	PhaseRunning PhaseState = "running"
	PhaseDone    PhaseState = "done"
	PhaseFailed  PhaseState = "failed"
)

// PhaseStatus is the last known outcome of a wake run.
type PhaseStatus struct {
	State    PhaseState `json:"state"`
	ExitCode int        `json:"exit_code,omitempty"`
	Error    string     `json:"error,omitempty"`
	// Command is the phase's declared command. It is set on a box's wake
	// status so a failed wake surfaces the failing step (#146).
	Command    string     `json:"command,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Phases is the last known contract state of a box.
type Phases struct {
	Synced    bool        `json:"synced"`
	Wake      PhaseStatus `json:"wake"`
	UpdatedAt time.Time   `json:"updated_at"`
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
// A state change resets the idle clock: the idle window starts fresh after a
// wake, freeze, or failure.
func (s *Store) Transition(id string, next BoxState) (*Box, error) {
	if !next.Valid() {
		return nil, fmt.Errorf("unknown box state %q", next)
	}
	return s.mutate(id, func(box *Box) error {
		if !box.State.CanTransition(next) {
			return fmt.Errorf("box %s cannot move from %s to %s", box.ID, box.State, next)
		}
		box.State = next
		box.IdleSince = nil
		return nil
	})
}

// TransitionIdle is Transition for an idle lifecycle step: it validates the
// transition exactly as Transition does but preserves the idle clock, so the
// longer stop window keeps measuring from the moment the box went idle rather
// than restarting when it freezes (#146).
func (s *Store) TransitionIdle(id string, next BoxState) (*Box, error) {
	if !next.Valid() {
		return nil, fmt.Errorf("unknown box state %q", next)
	}
	return s.mutate(id, func(box *Box) error {
		if !box.State.CanTransition(next) {
			return fmt.Errorf("box %s cannot move from %s to %s", box.ID, box.State, next)
		}
		box.State = next
		return nil
	})
}

// SetPhases records the box's last reported wake state.
func (s *Store) SetPhases(id string, phases Phases) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		phases.UpdatedAt = time.Now().UTC()
		box.Phases = &phases
		return nil
	})
}

// mutate reads a box, applies change, and persists it atomically.
func (s *Store) mutate(id string, change func(*Box) error) (*Box, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid box id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	box, err := ReadBox(s.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := change(box); err != nil {
		return nil, err
	}
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

// Box returns one box record by ID.
func (s *Store) Box(id string) (*Box, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid box id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	box, err := ReadBox(s.recordPath(id))
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
		box, err := ReadBox(path)
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

// ReadBox reads and validates a box record from an explicit path.
func ReadBox(path string) (*Box, error) {
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

// NewID returns a fresh random identifier, used for boxes and runs.
func NewID() string { return newID() }

// ValidID reports whether id is a well-formed box or run ID.
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

// ShortID shortens an identifier for display.
func ShortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
