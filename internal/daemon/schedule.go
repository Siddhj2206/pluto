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
	s.dispatchQueue(ctx)
	s.fireDueSchedules(ctx, s.now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatchQueue(ctx)
			s.fireDueSchedules(ctx, s.now())
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

// fireBoxSchedules fires each of a box's due schedules. A schedule is due
// when it has a matching minute after its last consumed one and at or before
// now. A box already running a job skips the occurrence — no queue, no
// concurrent run — and the skip consumes the occurrence like a fire would.
func (s *Server) fireBoxSchedules(ctx context.Context, box *state.Box, now time.Time) {
	for _, sched := range box.Schedules {
		cron, err := contract.ParseCron(sched.Cron)
		if err != nil {
			s.logf("schedule %s on box %s: invalid cron %q: %v", sched.Name, state.ShortID(box.ID), sched.Cron, err)
			continue
		}
		if !occurrenceAfter(cron, consumedThrough(sched), now) {
			continue
		}
		// Re-read: job state may have moved since the listing.
		fresh, err := s.store.Box(box.ID)
		if err != nil {
			continue
		}
		if fresh.JobRunning() {
			s.logf("skipped schedule %s on box %s: a job is already running", sched.Name, state.ShortID(fresh.ID))
			s.consumeOccurrence(fresh.ID, sched.Name, now)
			continue
		}
		if !s.beginFiring(fresh.ID, sched.Name) {
			continue
		}
		s.launchFire(ctx, fresh, sched, now)
	}
}

// launchFire runs one due schedule off the evaluation path: a long job must
// not hold up other boxes' schedules. The schedule's clock advances when the
// run is over, so a crash in between can duplicate the run — delivery is
// at-least-once (ADR 0003).
func (s *Server) launchFire(ctx context.Context, box *state.Box, sched state.Schedule, now time.Time) {
	go func() {
		defer s.endFiring(box.ID, sched.Name)
		if err := s.fire(ctx, box, sched); err != nil {
			s.logf("schedule %s on box %s: %v", sched.Name, state.ShortID(box.ID), err)
		}
		s.consumeOccurrence(box.ID, sched.Name, now)
	}()
}

// fire executes one due schedule: a warm-up only wakes the box, a job
// schedule runs the declared job, which ensures the box is up first. The
// job's outcome lands in the box's history like any other run.
func (s *Server) fire(ctx context.Context, box *state.Box, sched state.Schedule) error {
	if sched.Job == "" {
		_, err := s.runner.Up(ctx, box)
		return err
	}
	spec, err := resolveJob(box, sched.Job)
	if err != nil {
		return err
	}
	_, _, err = s.runner.RunJob(ctx, box, spec, nil)
	return err
}

// consumedThrough is how far a schedule's occurrences are accounted for: its
// last fire or skip, or the arm time before the first one. A schedule with
// neither has no known arming and never backfills.
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

// consumeOccurrence records that everything through at belongs to this
// schedule, whether it fired or was skipped.
func (s *Server) consumeOccurrence(boxID, name string, at time.Time) {
	if _, err := s.store.AdvanceSchedule(boxID, name, at); err != nil {
		s.logf("schedule %s on box %s: record last-fired: %v", name, state.ShortID(boxID), err)
	}
}

// beginFiring claims a schedule for one in-flight run, refusing while another
// is still starting, so two ticks cannot fire the same occurrence twice.
func (s *Server) beginFiring(boxID, name string) bool {
	s.firingMu.Lock()
	defer s.firingMu.Unlock()
	if s.firing == nil {
		s.firing = make(map[string]bool)
	}
	key := boxID + "\x00" + name
	if s.firing[key] {
		return false
	}
	s.firing[key] = true
	return true
}

func (s *Server) endFiring(boxID, name string) {
	s.firingMu.Lock()
	delete(s.firing, boxID+"\x00"+name)
	s.firingMu.Unlock()
}
