package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RunState is a run's lifecycle state: running until it finishes, then done or
// failed. It mirrors JobState; a run is durable history, not a box's bounded
// recent-jobs list.
type RunState string

const (
	RunRunning RunState = "running"
	RunDone    RunState = "done"
	RunFailed  RunState = "failed"
)

// TriggerManual names a run started by `pluto run` (the only trigger #141
// records; schedules and repository events arrive later).
const TriggerManual = "manual"

// RunSchema is the schema version of a run record. It is the run record's own
// version, independent of the box record schema, so either may evolve alone.
// A reader refuses a record it does not understand.
const RunSchema = 1

// DefaultRunRetention bounds how long run records and their step logs are kept
// when nothing overrides it: 30 days (#142). A non-positive window keeps
// everything.
const DefaultRunRetention = 30 * 24 * time.Hour

// Valid reports whether s is a known run state.
func (s RunState) Valid() bool {
	switch s {
	case RunRunning, RunDone, RunFailed:
		return true
	}
	return false
}

// ErrRunNotFound reports a run the store does not know.
var ErrRunNotFound = errors.New("run not found")

// TriggerContext is the detail behind a run's trigger: when it fired and, for
// non-manual triggers, what fired it. A schedule occurrence carries its name
// and time; a repository event carries its event context. Run.Trigger names
// the kind.
type TriggerContext struct {
	FiredAt    time.Time     `json:"fired_at"`
	Schedule   string        `json:"schedule,omitempty"`
	Occurrence *time.Time    `json:"occurrence,omitempty"`
	Event      *EventContext `json:"event,omitempty"`
}

// Step is one part of a run: a single command with its own outcome, timing,
// and its own retained log stream. A task given as a single command has one
// implicit step.
type Step struct {
	Name       string     `json:"name"`
	Command    string     `json:"command"`
	State      RunState   `json:"state"`
	ExitCode   int        `json:"exit_code,omitempty"`
	Error      string     `json:"error,omitempty"`
	Log        string     `json:"log,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Run is one execution of a task, recorded in that task's history with its
// status, timing, exit code, and steps. Every invocation adds a run; repeated
// manual runs and schedule occurrences share the shape (list/show carry it).
type Run struct {
	Schema         int             `json:"schema"`
	ID             string          `json:"id"`
	Task           string          `json:"task"`
	BoxID          string          `json:"box_id"`
	WorkspacePath  string          `json:"workspace,omitempty"`
	Trigger        string          `json:"trigger"`
	TriggerContext *TriggerContext `json:"trigger_context,omitempty"`
	State          RunState        `json:"state"`
	ExitCode       int             `json:"exit_code"`
	Error          string          `json:"error,omitempty"`
	Steps          []Step          `json:"steps"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	DurationMS     int64           `json:"duration_ms,omitempty"`
}

// StartRun builds the record of a task run that is about to execute.
func StartRun(id, task, boxID, workspace, trigger string) Run {
	now := time.Now().UTC()
	return Run{
		Schema:         RunSchema,
		ID:             id,
		Task:           task,
		BoxID:          boxID,
		WorkspacePath:  workspace,
		Trigger:        trigger,
		TriggerContext: &TriggerContext{FiredAt: now},
		State:          RunRunning,
		CreatedAt:      now,
		StartedAt:      &now,
	}
}

// NewStep builds a step that is about to start.
func NewStep(name, command string) Step {
	return Step{Name: name, Command: command, State: RunRunning}
}

// Start stamps a step running at now.
func (s *Step) Start(now time.Time) {
	s.State = RunRunning
	s.StartedAt = &now
}

// Finish stamps a step's terminal outcome. A non-zero exit code or an error
// message fails it.
func (s *Step) Finish(exitCode int, errMsg string, now time.Time) {
	s.ExitCode = exitCode
	s.Error = errMsg
	s.FinishedAt = &now
	if errMsg != "" || exitCode != 0 {
		s.State = RunFailed
		return
	}
	s.State = RunDone
}

// Finish stamps the run's terminal outcome from its steps: any failed step
// fails the run, and the run's exit code is the first non-zero step code.
func (r *Run) Finish(now time.Time) {
	r.FinishedAt = &now
	if r.StartedAt != nil {
		r.DurationMS = now.Sub(*r.StartedAt).Milliseconds()
	}
	r.ExitCode = 0
	r.State = RunDone
	for i := range r.Steps {
		if r.Steps[i].State == RunFailed {
			r.State = RunFailed
		}
		if r.ExitCode == 0 && r.Steps[i].ExitCode != 0 {
			r.ExitCode = r.Steps[i].ExitCode
		}
	}
}

// Fail stamps a run failed before any step could produce an outcome, carrying
// the reason (for example, the box could not exec the command).
func (r *Run) Fail(reason string, now time.Time) {
	r.State = RunFailed
	r.Error = reason
	if r.ExitCode == 0 {
		r.ExitCode = 1
	}
	r.Finish(now)
}

func (r *Run) validate() error {
	if !ValidID(r.ID) {
		return fmt.Errorf("invalid run id %q", r.ID)
	}
	if r.Task == "" {
		return fmt.Errorf("run %s: task is required", r.ID)
	}
	if !r.State.Valid() {
		return fmt.Errorf("run %s: unknown state %q", r.ID, r.State)
	}
	return nil
}

// BeginRun records a run as running in the task's history.
func (s *Store) BeginRun(run Run) (*Run, error) {
	if err := run.validate(); err != nil {
		return nil, err
	}
	if run.State != RunRunning {
		return nil, fmt.Errorf("begin run %s: state must be %q", run.ID, RunRunning)
	}
	if run.Schema == 0 {
		run.Schema = RunSchema
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeRun(&run); err != nil {
		return nil, err
	}
	return &run, nil
}

// SetRun records the latest report of a run. A missing record is adopted, so a
// run that outlives a daemon restart still lands.
func (s *Store) SetRun(run Run) (*Run, error) {
	if err := run.validate(); err != nil {
		return nil, err
	}
	if run.Schema == 0 {
		run.Schema = RunSchema
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeRun(&run); err != nil {
		return nil, err
	}
	return &run, nil
}

// Run returns a run by exact id.
func (s *Store) Run(id string) (*Run, error) {
	if !ValidID(id) {
		return nil, ErrRunNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readRun(s.runPath(id))
}

// Runs returns every recorded run, newest first.
func (s *Store) Runs() ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listRunsLocked()
}

// ResolveRun turns a full run id or an unambiguous id prefix into a run.
func (s *Store) ResolveRun(arg string) (*Run, error) {
	if ValidID(arg) {
		if run, err := s.Run(arg); err == nil {
			return run, nil
		}
	}
	runs, err := s.Runs()
	if err != nil {
		return nil, err
	}
	var match *Run
	for _, run := range runs {
		if !strings.HasPrefix(run.ID, arg) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("run id %q matches more than one run", arg)
		}
		match = run
	}
	if match == nil {
		return nil, ErrRunNotFound
	}
	return match, nil
}

// TaskRuns returns one box's runs for a task, newest first.
func (s *Store) TaskRuns(boxID, task string) ([]*Run, error) {
	runs, err := s.Runs()
	if err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(runs))
	for _, run := range runs {
		if run.BoxID == boxID && run.Task == task {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *Store) runsDir() string          { return filepath.Join(s.root, "runs") }
func (s *Store) runPath(id string) string { return filepath.Join(s.runsDir(), id+".json") }

func (s *Store) listRunsLocked() ([]*Run, error) {
	entries, err := os.ReadDir(s.runsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runs dir: %w", err)
	}
	runs := make([]*Run, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		run, err := readRun(filepath.Join(s.runsDir(), e.Name()))
		if err != nil {
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	return runs, nil
}

func (s *Store) readRun(path string) (*Run, error) {
	run, err := readRun(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrRunNotFound
	}
	return run, err
}

func (s *Store) writeRun(run *Run) error {
	if err := os.MkdirAll(s.runsDir(), 0o755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run record: %w", err)
	}
	return writeFileAtomic(s.runPath(run.ID), append(data, '\n'), 0o644)
}

// readRun reads and validates a run record from an explicit path.
func readRun(path string) (*Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if run.Schema != RunSchema {
		return nil, fmt.Errorf("%s: unsupported record schema %d", path, run.Schema)
	}
	if !ValidID(run.ID) {
		return nil, fmt.Errorf("%s: invalid run id %q", path, run.ID)
	}
	return &run, nil
}
