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
// CPU/IO since the daemon's last loop sample; it goes idle — and the clock
// starts — only when every one of those facts is known to be quiet. An
// unknown client count or session reading keeps the box awake rather than
// guessing. It never pauses, and the session comparison never advances the
// stored sample: rememberSessions does that on the loop tick.
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

// sessionNoiseFloorCPU is the least CPU a declared session must burn between
// two daemon samples to count as work. It is a policy threshold, not a
// measurement: observing a session (Agent.Status reaches tmux) spends CPU
// inside the measured cgroup, and idle guests keep burning a little on
// journald, sshd and indexers, so with no floor every session reads busy and
// the box never auto-pauses. 50 ms over a sample absorbs that
// self-perturbation; a real turn or build clears it instantly. The exact
// value is parked for tuning (docs/research/auto-pause-signals.md §2, §5) and
// ADR 0010 makes thresholds and hysteresis policy.
const sessionNoiseFloorCPU = 50 * time.Millisecond

// sessionNoiseFloorIOBytes is the matching IO threshold. The observation above
// costs CPU, not IO, and a missing io.stat reads as zero (cgroupUsage), so no
// IO floor is needed: any real read or write counts as work.
const sessionNoiseFloorIOBytes int64 = 0

// sessionBusy reports whether the box's declared sessions have burned CPU or
// IO since the daemon's last loop sample, and whether that fact could be read
// at all. The agent supplies cumulative counters; the comparison lives here so
// the busy threshold stays daemon policy. A missing or failed reading is
// unknown — never idle — exactly as an unknown client count is: the daemon
// keeps the box awake and does not start the idle clock.
//
// The comparison is read-only. Only rememberSessions, called by the auto-pause
// loop after evaluation, advances the baseline; a status look must not, or
// every poll would reset it and the next tick would judge a working session on
// too small a delta (issue #58).
func (s *Server) sessionBusy(box *state.Box) (busy, known bool) {
	if box.Phases == nil || box.Phases.SessionUsage == nil {
		return false, false
	}
	current := *box.Phases.SessionUsage
	s.sessionMu.Lock()
	previous, seen := s.sessionSamples[box.ID]
	s.sessionMu.Unlock()
	if !seen {
		// No baseline yet: the box is not idle, but this reading cannot
		// prove work happened either. Start the clock at zero activity and
		// let the next loop tick compare.
		return false, true
	}
	busy = current.CPUUsec-previous.CPUUsec > sessionNoiseFloorCPU.Microseconds() ||
		current.IOBytes-previous.IOBytes > sessionNoiseFloorIOBytes
	return busy, true
}

// rememberSessions advances the stored session sample for a box to its latest
// reading. It is the only writer of sessionSamples, and only the auto-pause
// loop calls it, so the baseline is one loop tick wide no matter how many
// status reads happen in between.
func (s *Server) rememberSessions(box *state.Box) {
	if box.Phases == nil || box.Phases.SessionUsage == nil {
		return
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionSamples == nil {
		s.sessionSamples = make(map[string]state.SessionUsage)
	}
	s.sessionSamples[box.ID] = *box.Phases.SessionUsage
}

// forgetSessions drops a box's stored sample when the box is gone, so the map
// tracks live boxes instead of growing once per box ever created.
func (s *Server) forgetSessions(id string) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	delete(s.sessionSamples, id)
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
		// Advance the sample once per tick, after the comparison, so reads in
		// between ticks compare against this tick's baseline (issue #58).
		s.rememberSessions(updated)
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
