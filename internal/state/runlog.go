package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Run logs are the per-step output a run retains after its process exits. They
// live beside the run record under runs/<run-id>/<step>.log and are referenced
// from the record by a state-root-relative path (Step.Log), so a run is legible
// long after the box is gone.
//
// Runs and their logs share one retention window (PruneRuns): a record and its
// logs are pruned together, never one without the other.

// RunLogRef returns the record-relative reference to a run step's log. The
// reference is what a run's Step.Log stores.
func (s *Store) RunLogRef(runID string, step int) string {
	return filepath.ToSlash(filepath.Join("runs", runID, fmt.Sprintf("%d.log", step)))
}

// RunLog is a run step's log open for writing during the run. Writes append to
// disk immediately, so output survives the process and is readable by another
// client (pluto logs) while the run is still going.
type RunLog struct {
	path string
	f    *os.File
}

// CreateRunLog opens (creating) a run step's log for writing. Callers close it
// when the step's process exits.
func (s *Store) CreateRunLog(runID string, step int) (*RunLog, error) {
	if !ValidID(runID) {
		return nil, fmt.Errorf("invalid run id %q", runID)
	}
	path := s.runLogPath(runID, step)
	if path == "" {
		return nil, fmt.Errorf("refusing to open run log for %q step %d", runID, step)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create run log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run log: %w", err)
	}
	return &RunLog{path: path, f: f}, nil
}

// Write appends a chunk to the run log, flushing it to disk as it arrives.
func (l *RunLog) Write(p []byte) (int, error) {
	if l == nil || l.f == nil {
		return len(p), nil
	}
	return l.f.Write(p)
}

// Close releases the run log's handle.
func (l *RunLog) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// ReadRunLog returns a run step log's retained output by reference. A log that
// has not been written (or was pruned) reads empty, not an error, so a record
// without one still renders.
func (s *Store) ReadRunLog(ref string) (string, error) {
	path, err := s.resolveRunLogRef(ref)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read run log: %w", err)
	}
	return string(data), nil
}

// runLogPath is the absolute path of a run step's log.
func (s *Store) runLogPath(runID string, step int) string {
	if !ValidID(runID) || step < 0 {
		return ""
	}
	return filepath.Join(s.runsDir(), runID, fmt.Sprintf("%d.log", step))
}

// resolveRunLogRef turns a record's log reference into an absolute path,
// refusing to escape the runs directory. An empty reference resolves to "".
func (s *Store) resolveRunLogRef(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	clean := filepath.Clean(filepath.FromSlash(ref))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("run log reference %q escapes the state directory", ref)
	}
	base := filepath.Base(clean)
	dir := filepath.Dir(clean)
	if dir != "runs" && !strings.HasPrefix(dir, "runs"+string(filepath.Separator)) {
		return "", fmt.Errorf("run log reference %q is not under runs/", ref)
	}
	return filepath.Join(s.runsDir(), strings.TrimPrefix(dir, "runs"), base), nil
}

// PruneRuns deletes run records and their retained logs that finished (or, for
// a run without a finish time, were created) before cutoff. A run still in
// flight has no finish time and is kept. It returns how many runs it removed;
// an unreadable record is skipped, not fatal.
func (s *Store) PruneRuns(cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	runs, err := s.listRunsLocked()
	if err != nil {
		return 0, err
	}
	pruned := 0
	for _, run := range runs {
		if run.State == RunRunning || run.FinishedAt == nil {
			continue
		}
		if !run.FinishedAt.Before(cutoff) {
			continue
		}
		if err := os.Remove(s.runPath(run.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pruned, fmt.Errorf("remove run record %s: %w", run.ID, err)
		}
		if err := os.RemoveAll(filepath.Join(s.runsDir(), run.ID)); err != nil {
			return pruned, fmt.Errorf("remove run logs %s: %w", run.ID, err)
		}
		pruned++
	}
	return pruned, nil
}
