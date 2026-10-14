package daemon

import (
	"context"
	"time"
)

// IdleInterval is how often the daemon evaluates container-box idle windows.
// The windows are minutes long, so a half minute of granularity is plenty.
const IdleInterval = 30 * time.Second

// Default idle windows (#146): a running box freezes after a short idle with
// its memory preserved, and a frozen box stops after a longer idle, keeping
// its volumes and record. Both are configurable.
const (
	DefaultFreezeWindow = 5 * time.Minute
	DefaultStopWindow   = 30 * time.Minute
)

// idleReaper is the container lifecycle's idle surface. *box.Manager
// implements it; the daemon holds it by assertion so the BoxManager interface
// stays focused.
type idleReaper interface {
	ReapIdle(ctx context.Context, now time.Time, freezeAfter, stopAfter time.Duration) error
}

// IdleLoop steps container boxes down their idle lifecycle. It evaluates once
// at startup and then every interval, until ctx is done. It is separate from
// AutoPauseLoop, which keeps serving the earlier worktree-based path.
func (s *Server) IdleLoop(ctx context.Context, interval, freezeAfter, stopAfter time.Duration) {
	s.IdleStep(s.now(), freezeAfter, stopAfter)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.IdleStep(s.now(), freezeAfter, stopAfter)
		}
	}
}

// IdleStep evaluates every container box's idle window once, using now as the
// clock. A running box freezes after freezeAfter idle; a frozen box stops
// after stopAfter idle. A box with attached work or an active run is left
// running. It is exported so tests drive idle deterministically.
func (s *Server) IdleStep(now time.Time, freezeAfter, stopAfter time.Duration) {
	reaper, ok := s.boxes.(idleReaper)
	if !ok {
		return
	}
	if err := reaper.ReapIdle(context.Background(), now, freezeAfter, stopAfter); err != nil {
		s.logf("idle: %v", err)
	}
}
