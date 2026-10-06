package daemon

import (
	"context"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// SchedulerInterval is how often the daemon looks for due schedules. Cron
// resolution is one minute, so a half minute of granularity is plenty.
const SchedulerInterval = 30 * time.Second

// SchedulerLoop fires due schedules. It evaluates once at startup, catching
// up whatever was missed while the daemon was down, then every interval until
// ctx is done. Firing never blocks evaluation: each due schedule runs on its
// own goroutine, and the store refuses a second concurrent job.
func (s *Server) SchedulerLoop(ctx context.Context, interval time.Duration) {
	if err := s.store.RecoverQueue(s.now()); err != nil {
		s.logf("queue recovery: %v", err)
	}
	s.fireDueSchedules(ctx, s.now())
	s.dispatchQueue(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fireDueSchedules(ctx, s.now())
			s.dispatchQueue(ctx)
		}
	}
}

// dispatchQueue starts requests while host and per-box capacity allow it.
func (s *Server) dispatchQueue(ctx context.Context) {
	s.queueDispatchMu.Lock()
	defer s.queueDispatchMu.Unlock()
	if s.queueReservedBoxes == nil {
		s.queueReservedBoxes = make(map[string]bool)
	}
	boxes, _, err := s.store.Boxes()
	if err != nil {
		s.logf("queue: list boxes: %v", err)
		return
	}
	running := 0
	byID := make(map[string]*state.Box, len(boxes))
	for _, b := range boxes {
		byID[b.ID] = b
		if b.State == state.StateRunning {
			running++
		}
	}
	for {
		item, err := s.store.NextQueueItem(s.now(), s.QueueAgingInterval)
		if err != nil {
			s.logf("queue: claim: %v", err)
			return
		}
		if item == nil {
			return
		}
		box, ok := byID[item.BoxID]
		if !ok {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueueFailed, "", "box no longer exists", s.now())
			continue
		}
		if s.queueReservedBoxes[item.BoxID] || (box.JobRunning() && item.Job != "") {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueuePending, "", "", s.now())
			return
		}
		needsSlot := box.State != state.StateRunning
		if needsSlot && running+s.queueRunning >= s.MaxRunningBoxes {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueuePending, "", "", s.now())
			return
		}
		s.queueReservedBoxes[item.BoxID] = true
		if needsSlot {
			s.queueRunning++
			running++
		}
		go func(q state.QueueItem, b *state.Box, reserves bool) {
			s.executeQueued(ctx, q, b)
			s.queueDispatchMu.Lock()
			delete(s.queueReservedBoxes, q.BoxID)
			if reserves {
				s.queueRunning--
			}
			s.queueDispatchMu.Unlock()
		}(*item, box, needsSlot)
	}
}

func (s *Server) executeQueued(ctx context.Context, item state.QueueItem, box *state.Box) {
	_, err := s.store.UpdateQueueItem(item.ID, state.QueueRunning, "", "", s.now())
	if err != nil {
		s.logf("queue %s: %v", state.ShortID(item.ID), err)
		return
	}
	jobID := ""
	if item.Job == "" && len(item.Argv) == 0 {
		_, err = s.runner.Up(ctx, box)
	} else {
		var spec contract.Exec
		spec, err = resolveRun(box, api.RunRequest{Job: item.Job, Argv: item.Argv})
		if err == nil {
			if item.Event.Kind != "" {
				if spec.Env == nil {
					spec.Env = make(map[string]string)
				}
				spec.Env["PLUTO_EVENT_KIND"] = item.Event.Kind
				spec.Env["PLUTO_EVENT_ACTION"] = item.Event.Action
				spec.Env["PLUTO_EVENT_REPO"] = item.Event.Repo
				spec.Env["PLUTO_EVENT_REF"] = item.Event.Ref
				spec.Env["PLUTO_EVENT_OBJECT_ID"] = item.Event.ObjectID
				spec.Env["PLUTO_EVENT_URL"] = item.Event.URL
			}
			var job *state.Job
			_, job, err = s.runner.RunJob(ctx, box, spec, nil)
			if job != nil {
				jobID = job.ID
				_, _ = s.store.UpdateQueueItem(item.ID, state.QueueRunning, jobID, "", s.now())
			}
		}
	}
	if err != nil {
		_, _ = s.store.UpdateQueueItem(item.ID, state.QueueFailed, "", err.Error(), s.now())
		s.logf("queue %s: %v", state.ShortID(item.ID), err)
		return
	}
	_, _ = s.store.UpdateQueueItem(item.ID, state.QueueDone, jobID, "", s.now())
	if item.ScheduleName != "" {
		if _, err := s.store.AdvanceSchedule(item.BoxID, item.ScheduleName, s.now()); err != nil {
			s.logf("schedule %s on box %s: record last-fired: %v", item.ScheduleName, state.ShortID(item.BoxID), err)
		}
	}
}

// now is the daemon's view of the current time. Tests replace Server.Now to
// drive the scheduler deterministically.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fireDueSchedules evaluates every box's stored schedules against now.
func (s *Server) fireDueSchedules(ctx context.Context, now time.Time) {
	boxes, _, err := s.store.Boxes()
	if err != nil {
		s.logf("scheduler: list boxes: %v", err)
		return
	}
	for _, box := range boxes {
		s.fireBoxSchedules(ctx, box, now)
	}
}

// fireBoxSchedules materializes each due schedule as durable host queue work.
// The queue coalesces repeat ticks for a schedule while its item is active.
func (s *Server) fireBoxSchedules(_ context.Context, box *state.Box, now time.Time) {
	for _, sched := range box.Schedules {
		cron, err := contract.ParseCron(sched.Cron)
		if err != nil {
			s.logf("schedule %s on box %s: invalid cron %q: %v", sched.Name, state.ShortID(box.ID), sched.Cron, err)
			continue
		}
		if !occurrenceAfter(cron, consumedThrough(sched), now) {
			continue
		}
		item := state.QueueItem{Source: state.QueueScheduled, BoxID: box.ID, Job: sched.Job, ScheduleName: sched.Name}
		_, enqueueErr := s.store.EnqueueScheduled(item, s.QueueCapacity, now)
		if enqueueErr != nil && enqueueErr != state.ErrQueueFull {
			s.logf("schedule %s on box %s: queue: %v", sched.Name, state.ShortID(box.ID), enqueueErr)
			continue
		}
		// The durable queue item (or durable rejection) now represents this
		// due occurrence. Advancing here makes a crash between enqueue and
		// schedule update harmless: EnqueueScheduled returns the same item.
		if _, err := s.store.AdvanceSchedule(box.ID, sched.Name, now); err != nil {
			s.logf("schedule %s on box %s: record last-fired: %v", sched.Name, state.ShortID(box.ID), err)
		}
	}
}

// consumedThrough is how far a schedule's occurrences have been materialized
// into its durable queue item, or the arm time before the first one. A
// schedule with neither has no known arming and never backfills.
func consumedThrough(sched state.Schedule) time.Time {
	if sched.LastFired != nil && sched.LastFired.After(sched.ArmedAt) {
		return *sched.LastFired
	}
	return sched.ArmedAt
}

// occurrenceAfter reports whether cron has a matching minute strictly after
// last and at or before now. Minute resolution answers whether an occurrence
// was missed, and the first match ends the scan, so a long outage costs one
// scan to its first missed minute.
func occurrenceAfter(cron contract.Cron, last, now time.Time) bool {
	if last.IsZero() {
		return false
	}
	m := last.UTC().Truncate(time.Minute).Add(time.Minute)
	end := now.UTC().Truncate(time.Minute)
	for !m.After(end) {
		if cron.Matches(m) {
			return true
		}
		m = m.Add(time.Minute)
	}
	return false
}
