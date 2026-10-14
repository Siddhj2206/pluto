package box

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// Freeze steps a running box down to frozen, preserving its memory. A frozen
// box thaws transparently when work arrives. Freezing keeps the idle clock, so
// the longer stop window keeps measuring from when the box went idle.
func (m *Manager) Freeze(ctx context.Context, id string) (*state.Box, error) {
	box, err := m.store.Box(id)
	if err != nil {
		return nil, err
	}
	if box.State == state.StateFrozen {
		return box, nil
	}
	if box.EngineID == "" {
		return nil, fmt.Errorf("box %s has no container", state.ShortID(box.ID))
	}
	if err := m.engine.Freeze(ctx, box.EngineID); err != nil {
		return nil, err
	}
	return m.store.TransitionIdle(box.ID, state.StateFrozen)
}

// ReapIdle advances every container box one step down its idle lifecycle: a
// running box freezes after freezeAfter idle, and a frozen box stops after
// stopAfter idle, keeping its volumes and record. Both windows are thresholds
// on the same idle clock, so a box freezes at the short window and stops at
// the long one; a non-positive window disables that step.
//
// Idle transitions happen only when nothing is attached and no run is active:
// a box with a gate holder (a run, an attach session, or a run waiting on the
// gate) is left running and its idle clock is cleared so a fresh window starts
// when the work drains. The legacy worktree-based boxes (no workspace) are left
// to the auto-pause loop.
//
// now is the caller's clock, so tests drive idle deterministically. A failure
// on one box does not stop the others; the errors are joined.
func (m *Manager) ReapIdle(ctx context.Context, now time.Time, freezeAfter, stopAfter time.Duration) error {
	boxes, _, err := m.store.Boxes()
	if err != nil {
		return err
	}
	var errs []error
	for _, box := range boxes {
		if box.WorkspacePath == "" {
			continue // the earlier worktree-based path has its own loop
		}
		switch box.State {
		case state.StateRunning:
			if err := m.reapRunning(ctx, box, now, freezeAfter); err != nil {
				errs = append(errs, err)
			}
		case state.StateFrozen:
			if err := m.reapFrozen(ctx, box, now, stopAfter); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) reapRunning(ctx context.Context, box *state.Box, now time.Time, freezeAfter time.Duration) error {
	if m.busy(box.ID) {
		return m.clearIdle(box.ID)
	}
	if freezeAfter <= 0 {
		return nil
	}
	since := box.IdleSince
	if since == nil {
		return m.setIdle(box.ID, now)
	}
	if now.Sub(*since) < freezeAfter {
		return nil
	}
	_, err := m.Freeze(ctx, box.ID)
	return err
}

func (m *Manager) reapFrozen(ctx context.Context, box *state.Box, now time.Time, stopAfter time.Duration) error {
	if m.busy(box.ID) {
		return nil
	}
	if stopAfter <= 0 {
		return nil
	}
	since := box.IdleSince
	if since == nil {
		return m.setIdle(box.ID, now)
	}
	if now.Sub(*since) < stopAfter {
		return nil
	}
	_, err := m.Stop(ctx, box.ID)
	return err
}

// busy reports whether a box has an active holder: a running or waiting run,
// or an attach session. Services run outside the gate and never keep a box
// awake; a frozen box's services resume with its memory.
func (m *Manager) busy(id string) bool {
	s := m.gate.Snapshot(id)
	return s.Shared > 0 || s.Exclusive || s.Waiting > 0
}

func (m *Manager) setIdle(id string, now time.Time) error {
	since := now
	_, err := m.store.SetIdleSince(id, &since)
	return err
}

func (m *Manager) clearIdle(id string) error {
	box, err := m.store.Box(id)
	if err != nil {
		return err
	}
	if box.IdleSince == nil {
		return nil
	}
	_, err = m.store.SetIdleSince(id, nil)
	return err
}
