package box_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/contract"
)

// waitForGate polls until cond holds, failing the test after a short bound. The
// gated operations it waits on are driven by channels; polling only observes
// the gate's own bookkeeping, never a sleep-tuned race.
func waitForGate(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestGateSharedHoldersRunConcurrently proves two shared holders may hold the
// same box at once.
func TestGateSharedHoldersRunConcurrently(t *testing.T) {
	g := box.NewGate()

	first, ok := g.TryHold("box-1", contract.LockShared)
	if !ok {
		t.Fatal("first shared hold was refused")
	}
	second, ok := g.TryHold("box-1", contract.LockShared)
	if !ok {
		t.Fatal("second shared hold was refused; shared work must run concurrently")
	}
	if snap := g.Snapshot("box-1"); snap.Shared != 2 || snap.Exclusive || snap.Waiting != 0 {
		t.Fatalf("snapshot = %+v, want two shared holders and no waiter", snap)
	}

	first()
	second()
	if snap := g.Snapshot("box-1"); snap.Shared != 0 {
		t.Fatalf("snapshot after release = %+v, want no holders", snap)
	}
}

// TestGateExclusiveWaitsForSharedToDrain proves an exclusive holder is refused
// while shared work holds the box, and admitted once it drains.
func TestGateExclusiveWaitsForSharedToDrain(t *testing.T) {
	g := box.NewGate()

	shared, ok := g.TryHold("box-1", contract.LockShared)
	if !ok {
		t.Fatal("shared hold was refused")
	}
	if _, ok := g.TryHold("box-1", contract.LockExclusive); ok {
		t.Fatal("exclusive hold succeeded while shared work held the box")
	}

	shared()
	exclusive, ok := g.TryHold("box-1", contract.LockExclusive)
	if !ok {
		t.Fatal("exclusive hold was refused after shared work drained")
	}
	exclusive()
}

// TestGateSharedWaitsWhileExclusiveRuns proves no shared holder can join while
// an exclusive holder owns the box.
func TestGateSharedWaitsWhileExclusiveRuns(t *testing.T) {
	g := box.NewGate()

	exclusive, ok := g.TryHold("box-1", contract.LockExclusive)
	if !ok {
		t.Fatal("exclusive hold was refused")
	}
	if _, ok := g.TryHold("box-1", contract.LockShared); ok {
		t.Fatal("shared hold succeeded while an exclusive holder owned the box")
	}
	exclusive()
	if _, ok := g.TryHold("box-1", contract.LockShared); !ok {
		t.Fatal("shared hold was refused after the exclusive holder released")
	}
}

// TestGateSharedWaitsWhileExclusiveWaits proves the gate is fair: once an
// exclusive holder is waiting, a later shared holder waits too, so a stream of
// interactive work cannot starve a batch run.
func TestGateSharedWaitsWhileExclusiveWaits(t *testing.T) {
	g := box.NewGate()

	shared, ok := g.TryHold("box-1", contract.LockShared)
	if !ok {
		t.Fatal("shared hold was refused")
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := g.Hold(context.Background(), "box-1", contract.LockExclusive)
		if err != nil {
			t.Errorf("exclusive Hold: %v", err)
			acquired <- func() {}
			return
		}
		acquired <- release
	}()
	waitForGate(t, "the exclusive holder to wait", func() bool {
		return g.Snapshot("box-1").Waiting == 1
	})
	if _, ok := g.TryHold("box-1", contract.LockShared); ok {
		t.Fatal("a shared holder joined while an exclusive holder was waiting")
	}

	shared()
	exclusive := <-acquired
	// The exclusive holder now owns the box; shared work still waits.
	if _, ok := g.TryHold("box-1", contract.LockShared); ok {
		t.Fatal("a shared holder joined while the exclusive holder ran")
	}
	exclusive()
	if _, ok := g.TryHold("box-1", contract.LockShared); !ok {
		t.Fatal("shared hold was refused after the exclusive holder released")
	}
}

// TestGateHoldHonoursContextCancellation proves a blocked holder gives up when
// its context is done, instead of holding the gate forever.
func TestGateHoldHonoursContextCancellation(t *testing.T) {
	g := box.NewGate()

	exclusive, ok := g.TryHold("box-1", contract.LockExclusive)
	if !ok {
		t.Fatal("exclusive hold was refused")
	}
	defer exclusive()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		release, err := g.Hold(ctx, "box-1", contract.LockShared)
		if release != nil {
			release()
		}
		done <- err
	}()
	waitForGate(t, "the shared holder to wait", func() bool {
		return g.Snapshot("box-1").Waiting == 1
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Hold error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Hold did not return after its context was cancelled")
	}
	if snap := g.Snapshot("box-1"); snap.Waiting != 0 {
		t.Fatalf("snapshot after cancellation = %+v, want no waiter", snap)
	}
}
