package box

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// Retention bounds how long a failed CI box is kept and how many are kept at
// once (#148). Defaults match the spec's retention section: the earlier of 48
// hours or 5 boxes.
const (
	DefaultRetainedMaxAge   = 48 * time.Hour
	DefaultRetainedMaxCount = 5
	// DefaultOrphanGrace is how long an orphaned durable box must be unchanged
	// before it is collected. It gives a checkout that is briefly missing (a
	// rebase, a move) time to come back.
	DefaultOrphanGrace = 24 * time.Hour
)

// ReapRetained evicts retained failed ephemeral boxes at the earlier of an age
// and a count: a box older than maxAge, or one beyond the newest maxCount, is
// removed. A pinned box is never evicted. A non-positive maxAge or maxCount
// disables that bound. now is the caller's clock, so tests drive eviction
// deterministically. It returns how many boxes it evicted.
func (m *Manager) ReapRetained(ctx context.Context, now time.Time, maxAge time.Duration, maxCount int) (int, error) {
	boxes, _, err := m.store.Boxes()
	if err != nil {
		return 0, err
	}
	retained := make([]*state.Box, 0, len(boxes))
	for _, box := range boxes {
		if box.Ephemeral && box.RetainedAt != nil && !box.Pinned {
			retained = append(retained, box)
		}
	}
	// Newest retained first, so the count bound keeps the most recent failures.
	sort.Slice(retained, func(i, j int) bool {
		return retained[i].RetainedAt.After(*retained[j].RetainedAt)
	})
	evicted := 0
	var errs []error
	for i, box := range retained {
		overCount := maxCount > 0 && i >= maxCount
		overAge := maxAge > 0 && now.Sub(*box.RetainedAt) >= maxAge
		if !overCount && !overAge {
			continue
		}
		if err := m.Remove(ctx, box.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		evicted++
	}
	return evicted, errors.Join(errs...)
}

// ReapOrphans collects durable boxes whose workspace checkout is gone. A box is
// collected only when every condition holds: the checkout no longer exists, the
// box is stopped, it is unpinned, and it has been unchanged past grace. The
// workspace record is dropped with the box, so a re-created checkout starts
// clean. It returns how many boxes it collected.
func (m *Manager) ReapOrphans(ctx context.Context, now time.Time, grace time.Duration) (int, error) {
	boxes, _, err := m.store.Boxes()
	if err != nil {
		return 0, err
	}
	collected := 0
	var errs []error
	for _, box := range boxes {
		if box.Ephemeral || box.WorkspacePath == "" || box.Pinned {
			continue
		}
		if box.State != state.StateStopped {
			continue
		}
		if grace > 0 && now.Sub(box.UpdatedAt) < grace {
			continue
		}
		// Collect only when the checkout is confirmed gone: a permission error
		// or any other stat failure leaves the box alone.
		if _, statErr := os.Stat(box.WorkspacePath); !errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if err := m.Remove(ctx, box.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		if ws, err := m.store.WorkspaceByPath(box.WorkspacePath); err == nil {
			_ = m.store.RemoveWorkspace(ws.ID)
		}
		collected++
	}
	return collected, errors.Join(errs...)
}
