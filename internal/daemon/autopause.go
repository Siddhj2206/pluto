package daemon

import (
	"context"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// AutoPauseInterval is how often the daemon looks for boxes whose idle window
// has elapsed. The window is minutes-to-hours long, so a half minute of
// granularity is plenty.
const AutoPauseInterval = 30 * time.Second

// autoPauseInfo is what a caller needs from one evaluation: when the box went
// idle and whether its window has elapsed.
type autoPauseInfo struct {
	IdleSince *time.Time
	Due       bool
}

// evaluateAutoPause records the box's idle clock and reports its auto-pause
// state. The caller supplies a freshly refreshed box: without a live view the
// daemon must not guess whether a client is attached. A box is busy while a
// client is attached, a job is running, or a declared session has burned
// CPU/IO since the daemon's last look; it goes idle — and the clock starts —
// only when every one of those facts is known to be quiet. An unknown client
// count or session reading keeps the box awake rather than guessing. It never
// pauses.
func (s *Server) evaluateAutoPause(box *state.Box, now time.Time) (*state.Box, autoPauseInfo) {
	window := s.runner.AutoPauseWindow(box)
	info := autoPauseInfo{}
	clients, clientsKnown := 0, false
	if box.Phases != nil && box.Phases.Clients != nil {
		clients, clientsKnown = *box.Phases.Clients, true
	}
	jobRunning := box.JobRunning()
	sessionsBusy, sessionsKnown := s.sessionBusy(box)

	// Cache the effective window on the record so `pluto status` can report
	// it without re-reading the contract.
	if want := autoPauseSetting(window); box.AutoPauseSetting != want {
		if updated, err := s.store.SetAutoPause(box.ID, want); err == nil {
			box = updated
		}
	}

	if window == 0 || !clientsKnown || clients > 0 || jobRunning || !sessionsKnown || sessionsBusy {
		if box.IdleSince != nil {
			if updated, err := s.store.SetIdleSince(box.ID, nil); err == nil {
				box = updated
			}
		}
		return box, info
	}
	if box.IdleSince == nil {
		since := now
		if updated, err := s.store.SetIdleSince(box.ID, &since); err == nil {
			box = updated
		}
		info.IdleSince = &since
	} else {
		info.IdleSince = box.IdleSince
	}
	info.Due = now.Sub(*info.IdleSince) >= window
	return box, info
}

// sessionNoiseFloorCPUUsec and sessionNoiseFloorIOBytes are the minimum
// growth in a session's cumulative cgroup counters that counts as work. Zero
// means any growth counts; a real noise floor (journald, sshd keepalives,
// indexers) is the policy knob the auto-pause-signals research parks for
// later, not a measurement problem.
const (
	sessionNoiseFloorCPUUsec int64 = 0
	sessionNoiseFloorIOBytes int64 = 0
)

// sessionBusy reports whether the box's declared sessions have burned CPU or
// IO since the daemon's last look, and whether that fact could be read at
// all. The agent supplies cumulative counters; the comparison lives here so
// the busy threshold stays daemon policy. A missing or failed reading is
// unknown — never idle — exactly as an unknown client count is: the daemon
// keeps the box awake and does not start the idle clock.
func (s *Server) sessionBusy(box *state.Box) (busy, known bool) {
	if box.Phases == nil || box.Phases.SessionUsage == nil {
		return false, false
	}
	current := *box.Phases.SessionUsage
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionSamples == nil {
		s.sessionSamples = make(map[string]state.SessionUsage)
	}
	previous, seen := s.sessionSamples[box.ID]
	s.sessionSamples[box.ID] = current
	if !seen {
		// No baseline yet: the box is not idle, but this reading cannot
		// prove work happened either. Start the clock at zero activity and
		// let the next look compare.
		return false, true
	}
	busy = current.CPUUsec-previous.CPUUsec > sessionNoiseFloorCPUUsec ||
		current.IOBytes-previous.IOBytes > sessionNoiseFloorIOBytes
	return busy, true
}

// autoPauseSetting renders an effective window the way the box record stores
// it: "off", or a canonical duration string.
func autoPauseSetting(window time.Duration) string {
	if window == 0 {
		return "off"
	}
	return window.String()
}

// AutoPauseLoop pauses boxes whose idle windows have elapsed. It evaluates
// once at startup and then every interval, until ctx is done.
func (s *Server) AutoPauseLoop(ctx context.Context, interval time.Duration) {
	s.pauseIdle(s.now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pauseIdle(s.now())
		}
	}
}

// pauseIdle evaluates every running box and pauses the ones that are due. A
// box whose agent cannot be reached is left alone: pausing on a guess could
// kill a session the daemon cannot see. A client or job that starts in the
// instant between evaluation and pause is still lost to the race; the window
// makes it vanishingly unlikely, and a pause is never destructive.
func (s *Server) pauseIdle(now time.Time) {
	boxes, _, err := s.store.Boxes()
	if err != nil {
		return
	}
	for _, box := range boxes {
		if reconciled, err := s.runner.Reconcile(box); err == nil {
			box = reconciled
		}
		if box.State != state.StateRunning {
			continue
		}
		refreshed, err := s.runner.Refresh(box)
		if err != nil {
			continue
		}
		updated, info := s.evaluateAutoPause(refreshed, now)
		if !info.Due {
			continue
		}
		idle := now.Sub(*info.IdleSince).Round(time.Second)
		if _, err := s.runner.Pause(updated); err != nil {
			s.logf("auto-pause box %s: %v", state.ShortID(box.ID), err)
			continue
		}
		s.logf("auto-paused box %s (idle %s)", state.ShortID(box.ID), idle)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}
