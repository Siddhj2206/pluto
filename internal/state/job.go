package state

import (
	"errors"
	"fmt"
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

// Valid reports whether s is a known job state.
func (s JobState) Valid() bool {
	switch s {
	case JobRunning, JobDone, JobFailed:
		return true
	}
	return false
}

// Job records one bounded command run in a box: what ran, how it ended, and
// where its output was recorded. At most one job runs per box, so a box keeps
// its latest job (ADR 0002); history is M1.
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

// JobRunning reports whether the box has a job in flight. Auto-pause consults
// it: a running job blocks the pause.
func (b *Box) JobRunning() bool {
	return b.Job != nil && b.Job.State == JobRunning
}

// StartJob builds the record of an argv job that is about to run.
func StartJob(id string, argv []string) Job {
	return StartJobCommand(id, strings.Join(argv, " "))
}

// StartJobCommand builds the record of a job that is about to run from its
// display command: an ad-hoc argv joined, or a declared job's command text.
func StartJobCommand(id, command string) Job {
	return Job{
		ID:        id,
		Command:   command,
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

// BeginJob records a job as running. It refuses with ErrJobRunning when the
// box already has one, and otherwise replaces the previous, finished job.
func (s *Store) BeginJob(id string, job Job) (*Box, error) {
	if !ValidID(job.ID) {
		return nil, fmt.Errorf("invalid job id %q", job.ID)
	}
	if job.State != JobRunning {
		return nil, fmt.Errorf("begin job %s: state must be %q", job.ID, JobRunning)
	}
	return s.mutate(id, func(box *Box) error {
		if box.JobRunning() {
			return fmt.Errorf("%w: %q started %s", ErrJobRunning,
				box.Job.Command, box.Job.StartedAt.Format(time.RFC3339))
		}
		next := job
		carryLog(box.Job, &next)
		box.Job = &next
		return nil
	})
}

// SetJob records the latest report of the box's job. Only the recorded job
// may be updated (BeginJob starts a new one), a finished job is never
// regressed to running, and a host-side log reference survives reports from
// the agent, which does not know it.
func (s *Store) SetJob(id string, job Job) (*Box, error) {
	if !ValidID(job.ID) {
		return nil, fmt.Errorf("invalid job id %q", job.ID)
	}
	if !job.State.Valid() {
		return nil, fmt.Errorf("job %s: unknown state %q", job.ID, job.State)
	}
	return s.mutate(id, func(box *Box) error {
		if old := box.Job; old != nil {
			if old.ID != job.ID {
				return nil
			}
			if old.State != JobRunning && job.State == JobRunning {
				return nil
			}
		}
		next := job
		carryLog(box.Job, &next)
		box.Job = &next
		return nil
	})
}
