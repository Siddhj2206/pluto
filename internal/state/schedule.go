package state

import (
	"errors"
	"fmt"
	"time"
)

// ErrNoSchedule reports an operation on a schedule a box does not carry.
var ErrNoSchedule = errors.New("no such schedule")

// Schedule is one durable alarm on a box record: the parsed schedule entry
// applied at the box's last handoff, plus how far its occurrences have been
// materialized into durable work (ADR 0003). ArmedAt is when the definition was
// stored, so a new schedule never backfills occurrences from before it existed;
// LastFired is nil until its first due occurrence is durably represented.
//
// A schedule targets a declared pluto.yml task (Task). Job is retained only to
// read records written before the contract cutover; the scheduler never sets it
// and skips entries that carry one.
type Schedule struct {
	Name      string     `json:"name"`
	Cron      string     `json:"cron"`
	Job       string     `json:"job,omitempty"`
	Task      string     `json:"task,omitempty"`
	ArmedAt   time.Time  `json:"armed_at"`
	LastFired *time.Time `json:"last_fired,omitempty"`
}

// SetSchedules replaces a box's stored schedules with the declared list,
// preserving the arm time and last-fired clock of schedules whose name, cron,
// and target are unchanged. A new or edited schedule is armed at now. Handoff
// calls this with the contract the box just applied.
func (s *Store) SetSchedules(id string, declared []Schedule, now time.Time) (*Box, error) {
	return s.mutate(id, func(box *Box) error {
		existing := make(map[string]Schedule, len(box.Schedules))
		for _, sched := range box.Schedules {
			existing[sched.Name] = sched
		}
		next := make([]Schedule, 0, len(declared))
		for _, sched := range declared {
			if old, ok := existing[sched.Name]; ok && old.Cron == sched.Cron && old.Job == sched.Job && old.Task == sched.Task {
				next = append(next, old)
				continue
			}
			next = append(next, Schedule{
				Name:    sched.Name,
				Cron:    sched.Cron,
				Job:     sched.Job,
				Task:    sched.Task,
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
