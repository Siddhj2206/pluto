package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// JobState is a job's lifecycle state: running until it exits.
type JobState string

const (
	JobRunning JobState = "running"
	JobDone    JobState = "done"
	JobFailed  JobState = "failed"
)

// JobHistoryLimit is the number of job summaries a box retains. Starting a
// new job prunes the oldest summaries past this limit along with their host
// logs.
const JobHistoryLimit = 20

// Valid reports whether s is a known job state.
func (s JobState) Valid() bool {
	switch s {
	case JobRunning, JobDone, JobFailed:
		return true
	}
	return false
}

// Job records one bounded command run in a box: what ran, how it ended, and
// where its output was recorded. A box keeps its last JobHistoryLimit jobs,
// newest first; the head is the box's latest job (ADR 0002, M1).
type Job struct {
	ID         string     `json:"id"`
	Command    string     `json:"command"`
	State      JobState   `json:"state"`
	ExitCode   int        `json:"exit_code,omitempty"`
	Error      string     `json:"error,omitempty"`
	Log        string     `json:"log,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
}

// ErrJobRunning reports a box that already has a running job. Concurrent
// runs on one box are refused, not queued.
var ErrJobRunning = errors.New("a job is already running")

// LatestJob returns the newest retained job, or nil when the box has none.
func (b *Box) LatestJob() *Job {
	if len(b.Jobs) == 0 {
		return nil
	}
	return &b.Jobs[0]
}

// Job returns a retained job by ID, or nil when it is not in the history.
func (b *Box) Job(id string) *Job {
	for i := range b.Jobs {
		if b.Jobs[i].ID == id {
			return &b.Jobs[i]
		}
	}
	return nil
}

// ResolveJob turns "last", a full job ID, or an unambiguous ID prefix into a
// retained job.
func (b *Box) ResolveJob(arg string) (*Job, error) {
	if len(b.Jobs) == 0 {
		return nil, fmt.Errorf("box %s has no recorded job", ShortID(b.ID))
	}
	if arg == "last" {
		return &b.Jobs[0], nil
	}
	if job := b.Job(arg); job != nil {
		return job, nil
	}
	var match *Job
	for i := range b.Jobs {
		if !strings.HasPrefix(b.Jobs[i].ID, arg) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("job prefix %q matches more than one job on box %s", arg, ShortID(b.ID))
		}
		match = &b.Jobs[i]
	}
	if match == nil {
		return nil, fmt.Errorf("no job %q on box %s (last is %s)", arg, ShortID(b.ID), ShortID(b.Jobs[0].ID))
	}
	return match, nil
}

// JobRunning reports whether the box has a job in flight. Auto-pause consults
// it: a running job blocks the pause.
func (b *Box) JobRunning() bool {
	latest := b.LatestJob()
	return latest != nil && latest.State == JobRunning
}

// StartJob builds the record of a job that is about to run.
func StartJob(id string, argv []string) Job {
	return Job{
		ID:        id,
		Command:   strings.Join(argv, " "),
		State:     JobRunning,
		StartedAt: time.Now().UTC(),
	}
}

// Finish stamps a terminal outcome onto the job.
func (j *Job) Finish(outcome JobState, exitCode int, errMsg string) {
	now := time.Now().UTC()
	j.State = outcome
	j.ExitCode = exitCode
	j.Error = errMsg
	j.FinishedAt = &now
	j.DurationMS = now.Sub(j.StartedAt).Milliseconds()
}

// carryLog keeps the host-side log reference when a report for the same job
// arrives without one; the agent does not know the host path.
func carryLog(old, next *Job) {
	if old != nil && old.ID == next.ID && next.Log == "" {
		next.Log = old.Log
	}
}

// BeginJob records a job as running at the head of the box's history. It
// refuses with ErrJobRunning when the box already has one, and otherwise
// retains the newest JobHistoryLimit summaries, removing the host logs of the
// ones it prunes.
func (s *Store) BeginJob(id string, job Job) (*Box, error) {
	if !ValidID(job.ID) {
		return nil, fmt.Errorf("invalid job id %q", job.ID)
	}
	if job.State != JobRunning {
		return nil, fmt.Errorf("begin job %s: state must be %q", job.ID, JobRunning)
	}
	var pruned []Job
	box, err := s.mutate(id, func(box *Box) error {
		if latest := box.LatestJob(); latest != nil && latest.State == JobRunning {
			return fmt.Errorf("%w: %q started %s", ErrJobRunning,
				latest.Command, latest.StartedAt.Format(time.RFC3339))
		}
		next := job
		carryLog(box.LatestJob(), &next)
		box.Jobs = append([]Job{next}, box.Jobs...)
		if len(box.Jobs) > JobHistoryLimit {
			pruned = append(pruned, box.Jobs[JobHistoryLimit:]...)
			box.Jobs = box.Jobs[:JobHistoryLimit]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, old := range pruned {
		s.removeJobLog(id, old)
	}
	return box, nil
}

// SetJob records the latest report of a retained job. Only retained jobs may
// be updated (BeginJob starts a new one), a finished job is never regressed
// to running, and a host-side log reference survives reports from the agent,
// which does not know it. A job the box does not know is adopted only when
// the record has no job at all — the crash-recovery path.
func (s *Store) SetJob(id string, job Job) (*Box, error) {
	if !ValidID(job.ID) {
		return nil, fmt.Errorf("invalid job id %q", job.ID)
	}
	if !job.State.Valid() {
		return nil, fmt.Errorf("job %s: unknown state %q", job.ID, job.State)
	}
	return s.mutate(id, func(box *Box) error {
		for i := range box.Jobs {
			old := &box.Jobs[i]
			if old.ID != job.ID {
				continue
			}
			if old.State != JobRunning && job.State == JobRunning {
				return nil
			}
			next := job
			carryLog(old, &next)
			box.Jobs[i] = next
			return nil
		}
		if len(box.Jobs) > 0 {
			return nil
		}
		box.Jobs = append([]Job{job}, box.Jobs...)
		return nil
	})
}

// removeJobLog deletes a pruned job's host-side log. It refuses paths outside
// the box's jobs directory, so a malformed record cannot delete arbitrary
// files.
func (s *Store) removeJobLog(boxID string, job Job) {
	if job.Log == "" {
		return
	}
	jobsDir := filepath.Join(s.boxDir(boxID), "jobs")
	path := filepath.Join(s.boxDir(boxID), filepath.Clean(job.Log))
	rel, err := filepath.Rel(jobsDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	_ = os.Remove(path)
}
