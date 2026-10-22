package state

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoSchedule reports an operation on a schedule a box does not carry.
var ErrNoSchedule = errors.New("no such schedule")

// Schedule is one durable alarm on a box record: the parsed [[schedule]] entry
// applied at the box's last handoff, plus how far its occurrences have been
// materialized into durable queue work (ADR 0003). ArmedAt is when the
// definition was stored, so a new schedule never backfills occurrences from
// before it existed; LastFired is nil until its first due occurrence is
// durably represented by the queue. It is the cron clock, not the last
// explicit occurrence: `pluto schedule run` materializes an occurrence
// without moving it.
//
// LastOutcome, LastRunID, and LastJobID describe the most recent occurrence
// that reached a terminal state, whichever path materialized it. Zero values
// mean unknown (a schedule that has never completed an occurrence, or a
// record written before these fields existed). NextDueAt is computed for a
// daemon response from the cron clock and is never persisted.
type Schedule struct {
	Name      string     `json:"name"`
	Cron      string     `json:"cron"`
	Job       string     `json:"job,omitempty"`
	ArmedAt   time.Time  `json:"armed_at"`
	LastFired *time.Time `json:"last_fired,omitempty"`
	// LastOutcome is "done", "warm" (a no-job occurrence), "failed",
	// "blocked", "rejected", or "skipped".
	LastOutcome string     `json:"last_outcome,omitempty"`
	LastRunID   string     `json:"last_run_id,omitempty"`
	LastJobID   string     `json:"last_job_id,omitempty"`
	NextDueAt   *time.Time `json:"next_due_at,omitempty"`
}

// Terminal schedule outcomes. They name the result of one materialized
// occurrence; the durable event carries the detailed reason.
const (
	ScheduleDone     = "done"
	ScheduleWarm     = "warm"
	ScheduleFailed   = "failed"
	ScheduleBlocked  = "blocked"
	ScheduleRejected = "rejected"
	ScheduleSkipped  = "skipped"
)

// SetSchedules replaces a box's stored schedules with the declared list,
// preserving the arm time and last-fired clock of schedules whose name, cron,
// and job are unchanged. A new or edited schedule is armed at now. Handoff
// calls this with the contract the box just applied.
func (s *Store) SetSchedules(id string, declared []Schedule, now time.Time) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		existing := make(map[string]Schedule, len(box.Schedules))
		for _, sched := range box.Schedules {
			existing[sched.Name] = sched
		}
		next := make([]Schedule, 0, len(declared))
		for _, sched := range declared {
			if old, ok := existing[sched.Name]; ok && old.Cron == sched.Cron && old.Job == sched.Job {
				old.NextDueAt = nil // computed per response, never stored
				next = append(next, old)
				continue
			}
			next = append(next, Schedule{
				Name:    sched.Name,
				Cron:    sched.Cron,
				Job:     sched.Job,
				ArmedAt: now,
			})
		}
		box.Schedules = next
		return nil
	})
}

// AdvanceSchedule records how far a schedule's occurrences have been
// materialized or coalesced. It never moves the clock backwards.
func (s *Store) AdvanceSchedule(id, name string, at time.Time) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		for i := range box.Schedules {
			if box.Schedules[i].Name != name {
				continue
			}
			if box.Schedules[i].LastFired != nil && !at.After(*box.Schedules[i].LastFired) {
				return nil
			}
			t := at
			box.Schedules[i].LastFired = &t
			return nil
		}
		return fmt.Errorf("box %s: %w %q", ShortID(id), ErrNoSchedule, name)
	})
}

// SetScheduleOutcome records the terminal result of the most recent
// occurrence materialized for a schedule. It is separate from the cron clock:
// recording a manual occurrence's result must not re-arm or advance the
// schedule. It is a no-op when nothing changes, so a skipped schedule on a
// ticking loop does not rewrite the box record every interval.
func (s *Store) SetScheduleOutcome(id, name, outcome, runID, jobID string) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		for i := range box.Schedules {
			sched := &box.Schedules[i]
			if sched.Name != name {
				continue
			}
			if sched.LastOutcome == outcome && (runID == "" || sched.LastRunID == runID) && (jobID == "" || sched.LastJobID == jobID) {
				return errNoChange
			}
			sched.LastOutcome = outcome
			if runID != "" {
				sched.LastRunID = runID
			}
			if jobID != "" {
				sched.LastJobID = jobID
			}
			return nil
		}
		return fmt.Errorf("box %s: %w %q", ShortID(id), ErrNoSchedule, name)
	})
}
