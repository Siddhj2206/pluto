package daemon

import (
	"context"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// SchedulerInterval is how often the daemon looks for due schedules. Cron
// resolution is one minute, so a half minute of granularity is plenty.
const SchedulerInterval = 30 * time.Second

// SchedulerLoop fires due schedules. It evaluates once at startup, catching
// up whatever was missed while the daemon was down, then every interval until
// ctx is done. Firing never blocks evaluation: each due schedule runs on its
// own goroutine, and the store keeps one pending run per task.
func (s *Server) SchedulerLoop(ctx context.Context, interval time.Duration) {
	s.schedulerTick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.schedulerTick(ctx)
		}
	}
}

// schedulerTick accepts each box's due task schedules and starts each task's
// pending run once (#147).
func (s *Server) schedulerTick(ctx context.Context) {
	now := s.now()
	s.acceptScheduledRuns(now)
	s.dispatchScheduledRuns(ctx, now)
}

// consumedThrough is the latest occurrence materialized as a run, or the arm
// time before the first one. A schedule with neither has no known arming and
// never backfills.
func consumedThrough(sched state.Schedule) time.Time {
	if sched.LastFired != nil && sched.LastFired.After(sched.ArmedAt) {
		return *sched.LastFired
	}
	return sched.ArmedAt
}

// nextOccurrenceAfter returns the first cron match strictly after last and no
// later than now. A zero last has no known arming, so nothing is due.
func nextOccurrenceAfter(cron contract.Cron, last, now time.Time) (time.Time, bool) {
	if last.IsZero() {
		return time.Time{}, false
	}
	m := last.UTC().Truncate(time.Minute).Add(time.Minute)
	end := now.UTC().Truncate(time.Minute)
	for !m.After(end) {
		if cron.Matches(m) {
			return m, true
		}
		m = m.Add(time.Minute)
	}
	return time.Time{}, false
}
