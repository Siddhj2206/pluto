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
	StateFailed:  {StateRunning, StatePaused},
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
	Schema   int    `json:"schema"`
	ID       string `json:"id"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	Worktree string `json:"worktree"`
	// PrimaryRepoURL is the remote that initialized this box. It remains the
	// project's identity even when the worktree has additional remotes.
	PrimaryRepoURL string   `json:"primary_repo_url,omitempty"`
	WorkItemType   string   `json:"work_item_type,omitempty"`
	WorkItemID     string   `json:"work_item_id,omitempty"`
	Ref            string   `json:"ref,omitempty"`
	UpdateBlocked  string   `json:"update_blocked,omitempty"`
	Image          string   `json:"image,omitempty"`
	State          BoxState `json:"state"`
	Phases         *Phases  `json:"phases,omitempty"`
	// Jobs is the box's retained job history, newest first (ADR 0002, M1).
	Jobs []Job `json:"jobs,omitempty"`
	// Schedules are the contract's alarms, stored when the box applied it and
	// fired by the daemon's scheduler loop (ADR 0003). Records written before
	// schedules existed simply have none.
	Schedules []Schedule `json:"schedules,omitempty"`
	// Resources is the size the box was created with (machine and rootfs),
	// derived from the worktree's [box].resources and frozen at first start.
	// Nil means the record predates the field or the contract declared none;
	// the runner's defaults then apply. Changing resources on an existing box
	// has no effect until the box is destroyed and recreated (M3:
	// recreate-only).
	Resources *Resources `json:"resources,omitempty"`
	// Remotes are the host worktree's git remotes, mirrored into the box at
	// first sync and recorded here so `pluto status` reports them even while
	// the box is paused. Empty means the worktree had none: the box is
	// local-only (ADR 0008).
	Remotes   []Remote  `json:"remotes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// ContractHash is the hash of the contract this box applied at its last
	// handoff (contract.Contract.Hash). Empty on boxes created before
	// staleness tracking; `pluto status` compares it with a fresh hash read
	// from the worktree to report divergence.
	ContractHash string `json:"contract_hash,omitempty"`
	// ContractStale reports, in daemon responses only, that the worktree's
	// contract now differs from the applied one. The daemon computes it per
	// status look; it is never persisted.
	ContractStale bool `json:"contract_stale,omitempty"`

	// AutoPauseSetting is the idle window the daemon last evaluated for this
	// box: "off", a duration string ("1h0m0s"), or "unknown" in a response
	// when the daemon has no live view. Empty means never evaluated and the
	// contract's default applies.
	AutoPauseSetting string `json:"auto_pause,omitempty"`
	// IdleSince is when the box was last observed with no client attached and
	// no job running. Nil means busy or not yet evaluated. It resets on every
	// state transition, so a wake always gets a fresh window.
	IdleSince *time.Time `json:"idle_since,omitempty"`
}

// CreateWorkItemBox returns the durable box for one repository work item.
// Repeated deliveries for the same item reuse its record and worktree.
func (s *Store) CreateWorkItemBox(project, repoURL, kind, itemID, ref, worktree string) (*Box, bool, error) {
	if repoURL == "" || (kind != "pull_request" && kind != "issue") || itemID == "" || worktree == "" {
		return nil, false, errors.New("repository, work item, and worktree are required")
	}
	worktree = filepath.Clean(worktree)
	s.mu.Lock()
	defer s.mu.Unlock()
	boxes, _, err := s.listLocked()
	if err != nil {
		return nil, false, err
	}
	for _, b := range boxes {
		if b.PrimaryRepoURL == repoURL && b.WorkItemType == kind && b.WorkItemID == itemID {
			return b, false, nil
		}
	}
	now := time.Now().UTC()
	branchKind := kind
	if kind == "pull_request" {
		branchKind = "pr"
	}
	b := &Box{Schema: RecordSchema, ID: newID(), Project: project, Branch: "pluto/" + branchKind + "-" + itemID, Worktree: worktree, PrimaryRepoURL: repoURL, WorkItemType: kind, WorkItemID: itemID, Ref: ref, State: StateCreated, CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(filepath.Join(s.boxDir(b.ID), "disk"), 0o755); err != nil {
		return nil, false, fmt.Errorf("create box dir: %w", err)
	}
	if err := s.writeBox(b); err != nil {
		_ = os.RemoveAll(s.boxDir(b.ID))
		return nil, false, err
	}
	return b, true, nil
}

// UpdateWorkItemRef records a successfully advanced ref or a visible safety block.
func (s *Store) UpdateWorkItemRef(id, ref, blocked string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := ReadBox(s.recordPath(id))
	if err != nil {
		return err
	}
	if b.WorkItemType == "" {
		return errors.New("box is not a work-item box")
	}
	b.Ref, b.UpdateBlocked, b.UpdatedAt = ref, blocked, time.Now().UTC()
	return s.writeBox(b)
}

// UnmarshalJSON reads a box record, promoting the pre-history single "job"
// field to the head of the job history so an existing state dir upgrades in
// place instead of dropping its latest job.
func (b *Box) UnmarshalJSON(data []byte) error {
	type plain Box
	var decoded struct {
		*plain
		LegacyJob *Job `json:"job"`
	}
	decoded.plain = (*plain)(b)
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.LegacyJob != nil && len(b.Jobs) == 0 {
		b.Jobs = []Job{*decoded.LegacyJob}
	}
	return nil
}

// Resources is a box's declared machine size, resolved from
// [box].resources at creation. A zero field means "unset": the runner fills it
// with its default. It is frozen once set, so a contract edit cannot resize a
// live box (recreate-only).
type Resources struct {
	CPUs      int `json:"cpus,omitempty"`
	MemoryMiB int `json:"memory_mib,omitempty"`
	// DiskMiB is the rootfs size in MiB. Zero means "unset": the disk keeps
	// the base image's size. It is frozen with the rest of the record, so a
	// contract edit cannot resize an existing disk (recreate-only).
	DiskMiB int `json:"disk_mib,omitempty"`
}

// PhaseState is the state of a contract phase.
type PhaseState string

const (
	PhasePending PhaseState = "pending"
	PhaseRunning PhaseState = "running"
	PhaseDone    PhaseState = "done"
	PhaseFailed  PhaseState = "failed"
)

// PhaseStatus is the last known outcome of a provision or wake run.
type PhaseStatus struct {
	State      PhaseState `json:"state"`
	ExitCode   int        `json:"exit_code,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// ServiceStatus is a declared service's observed state.
type ServiceStatus struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Port        int    `json:"port,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionStatus is a declared session's observed state: whether it is
// running and whether a client is attached to it. It is distinct from the ssh
// client count in Phases.Clients.
type SessionStatus struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Attached    bool   `json:"attached,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionUsage is the cumulative CPU and IO the box's declared sessions have
// consumed, read from their cgroup v2 counters and summed across sessions.
// The counters only grow, so the daemon compares successive readings to tell
// whether a session burned CPU/IO since its last look; the agent reports the
// counters and never decides busy. A nil Phases.SessionUsage means the agent
// could not read them, and auto-pause treats that as unknown, never idle.
type SessionUsage struct {
	CPUUsec int64 `json:"cpu_usec,omitempty"`
	IOBytes int64 `json:"io_bytes,omitempty"`
}

// Phases is the last known contract state of a box, reported by the agent.
type Phases struct {
	Synced    bool            `json:"synced"`
	BootID    string          `json:"boot_id,omitempty"`
	Worktree  string          `json:"worktree,omitempty"`
	Provision PhaseStatus     `json:"provision"`
	Wake      PhaseStatus     `json:"wake"`
	Services  []ServiceStatus `json:"services,omitempty"`
	// Sessions is the observed state of the box's declared sessions, distinct
	// from Clients (the live ssh count).
	Sessions []SessionStatus `json:"sessions,omitempty"`
	// SessionUsage is the cumulative cgroup CPU/IO work of the declared
	// sessions. Nil means the agent could not read it; auto-pause treats that
	// as unknown and keeps the box awake, never as idle.
	SessionUsage *SessionUsage `json:"session_usage,omitempty"`
	// Clients is the number of live ssh sessions in the box, as observed by
	// the agent. Nil means the agent could not tell; auto-pause refuses to
	// guess and leaves the box running.
	Clients   *int      `json:"clients,omitempty"`
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
// A state change resets the idle clock: the auto-pause window starts fresh
// after a wake, pause, or failure.
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

// SetImage pins a box to an image version; later ups boot that version.
func (s *Store) SetImage(id, version string) (*Box, error) {
	if version == "" {
		return nil, errors.New("image version is required")
	}
	return s.mutate(id, func(box *Box) error {
		box.Image = version
		return nil
	})
}

// SetResources records the machine size derived from the worktree's
// [box].resources. The runner calls it once, before the first start, so the
// box is sized at creation and never resized in place.
func (s *Store) SetResources(id string, res *Resources) (*Box, error) {
	if res == nil {
		return nil, errors.New("resources are required")
	}
	return s.mutate(id, func(box *Box) error {
		box.Resources = res
		return nil
	})
}

// SetPhases records the agent's last reported contract state.
func (s *Store) SetPhases(id string, phases Phases) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		phases.UpdatedAt = time.Now().UTC()
		box.Phases = &phases
		return nil
	})
}

// StopSessions marks every declared session stopped, keeping its name and
// description. Pausing a box kills the machine and every session with it, so a
// paused box must not keep reporting a session running (issue #56). A box with
// no phases (no contract applied) has nothing to mark.
func (s *Store) StopSessions(id string) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		if box.Phases == nil {
			return nil
		}
		for i := range box.Phases.Sessions {
			box.Phases.Sessions[i].State = "stopped"
			box.Phases.Sessions[i].Attached = false
		}
		return nil
	})
}

// SetContractHash records the hash of the contract the box applied, so
// staleness survives daemon restarts and host reboots.
func (s *Store) SetContractHash(id, hash string) (*Box, error) {
	if hash == "" {
		return nil, errors.New("contract hash is required")
	}
	return s.mutate(id, func(box *Box) error {
		box.ContractHash = hash
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

// CreateBox creates a box for a worktree, or returns the existing one. A
// worktree has one primary box, so create is idempotent per worktree path.
func (s *Store) CreateBox(project, branch, worktree string) (*Box, bool, error) {
	return s.CreateBoxWithRepo(project, branch, worktree, "")
}

// CreateBoxWithRepo creates a box and records its initializing repository URL.
func (s *Store) CreateBoxWithRepo(project, branch, worktree, repoURL string) (*Box, bool, error) {
	if worktree == "" {
		return nil, false, errors.New("worktree is required")
	}
	worktree = filepath.Clean(worktree)
	s.mu.Lock()
	defer s.mu.Unlock()

	boxes, _, err := s.listLocked()
	if err != nil {
		return nil, false, err
	}
	for _, existing := range boxes {
		if existing.Worktree != worktree {
			continue
		}
		if existing.Project != project || existing.Branch != branch {
			existing.Project = project
			existing.Branch = branch
			existing.UpdatedAt = time.Now().UTC()
			if err := s.writeBox(existing); err != nil {
				return nil, false, err
			}
		}
		if repoURL != "" && existing.PrimaryRepoURL == "" {
			existing.PrimaryRepoURL = repoURL
			existing.UpdatedAt = time.Now().UTC()
			if err := s.writeBox(existing); err != nil {
				return nil, false, err
			}
		}
		return existing, false, nil
	}

	now := time.Now().UTC()
	box := &Box{
		Schema:         RecordSchema,
		ID:             newID(),
		Project:        project,
		Branch:         branch,
		Worktree:       worktree,
		PrimaryRepoURL: repoURL,
		State:          StateCreated,
		CreatedAt:      now,
		UpdatedAt:      now,
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

// NewID returns a fresh random identifier, used for boxes and jobs.
func NewID() string { return newID() }

// ValidID reports whether id is a well-formed box or job ID.
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
