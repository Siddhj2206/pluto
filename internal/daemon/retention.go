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
