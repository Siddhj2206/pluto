package daemon

import (
	"context"
	"time"
)

// RetentionInterval is how often the host sweeps expired run records and their
// logs in production. Tests drive the loop with a shorter interval.
const RetentionInterval = time.Hour

// RetentionLoop prunes run records and their retained step logs past the
// configured window (Server.RunRetention), once at startup and every interval
// after. A non-positive window keeps everything and the loop returns at once.
func (s *Server) RetentionLoop(ctx context.Context, interval time.Duration) {
	if s.RunRetention <= 0 {
		return
	}
	s.pruneRuns(s.now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pruneRuns(s.now())
		}
	}
}

// BoxRetentionInterval is how often the host sweeps retained failed CI boxes
// and orphaned durable boxes in production. Tests drive the step directly.
const BoxRetentionInterval = time.Hour

// BoxRetentionLoop evicts retained failed CI boxes and collects orphaned
// durable boxes (#148), once at startup and every interval after.
func (s *Server) BoxRetentionLoop(ctx context.Context, interval time.Duration) {
	s.BoxRetentionStep(s.now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.BoxRetentionStep(s.now())
		}
	}
}

// BoxRetentionStep evaluates box retention once, using now as the clock: it
// evicts retained failed CI boxes at the earlier of the age and count bounds,
// then collects orphaned durable boxes past the grace window. It is exported so
// tests drive retention deterministically. Without a box lifecycle it is a
// no-op.
func (s *Server) BoxRetentionStep(now time.Time) {
	reaper, ok := s.boxes.(boxReaper)
	if !ok {
		return
	}
	if evicted, err := reaper.ReapRetained(context.Background(), now, s.RetainedMaxAge, s.RetainedMaxCount); err != nil {
		s.logf("box retention: %v", err)
	} else if evicted > 0 {
		s.logf("box retention: evicted %d retained box(es)", evicted)
	}
	if collected, err := reaper.ReapOrphans(context.Background(), now, s.OrphanGrace); err != nil {
		s.logf("orphan gc: %v", err)
	} else if collected > 0 {
		s.logf("orphan gc: collected %d orphaned box(es)", collected)
	}
}

// pruneRuns removes records and logs finished before the retention window.
func (s *Server) pruneRuns(now time.Time) {
	if s.RunRetention <= 0 {
		return
	}
	pruned, err := s.store.PruneRuns(now.Add(-s.RunRetention))
	if err != nil {
		s.logf("run retention: %v", err)
		return
	}
	if pruned > 0 {
		s.logf("run retention: pruned %d expired run(s)", pruned)
	}
}
