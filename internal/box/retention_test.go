package box_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/box"
)

// retained creates and retains an ephemeral box at at, returning its id.
func retained(t *testing.T, h *harness, at time.Time) string {
	t.Helper()
	b, err := h.m.CreateEphemeral(context.Background(), h.adapter.Workspace.Path, "")
	if err != nil {
		t.Fatalf("CreateEphemeral: %v", err)
	}
	if _, err := h.m.Retain(b.ID, at); err != nil {
		t.Fatalf("Retain: %v", err)
	}
	return b.ID
}

// TestReapRetainedEvictsBeyondTheCountBound proves the count bound keeps only
// the newest retained boxes (#148).
func TestReapRetainedEvictsBeyondTheCountBound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		ids = append(ids, retained(t, h, base.Add(-time.Duration(i)*time.Hour)))
	}

	evicted, err := h.m.ReapRetained(ctx, base, 0, 5)
	if err != nil {
		t.Fatalf("ReapRetained: %v", err)
	}
	if evicted != 1 {
		t.Fatalf("evicted = %d, want the sixth-newest box", evicted)
	}
	if _, err := h.st.Box(ids[5]); err == nil {
		t.Fatal("the oldest retained box survived the count bound")
	}
	for _, id := range ids[:5] {
		if _, err := h.st.Box(id); err != nil {
			t.Fatalf("box %s was evicted by the count bound: %v", id, err)
		}
	}
}

// TestReapRetainedEvictsPastTheAgeBound proves the age bound evicts a retained
// box older than the window even when the count would keep it (#148).
func TestReapRetainedEvictsPastTheAgeBound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	fresh := retained(t, h, base.Add(-time.Hour))
	aged := retained(t, h, base.Add(-49*time.Hour))

	evicted, err := h.m.ReapRetained(ctx, base, 48*time.Hour, 0)
	if err != nil {
		t.Fatalf("ReapRetained: %v", err)
	}
	if evicted != 1 {
		t.Fatalf("evicted = %d, want the aged box only", evicted)
	}
	if _, err := h.st.Box(aged); err == nil {
		t.Fatal("the aged box survived the age bound")
	}
	if _, err := h.st.Box(fresh); err != nil {
		t.Fatalf("the fresh box was evicted: %v", err)
	}
}

// TestPinExemptsARetainedBoxFromEviction proves a pinned box is never evicted,
// and unpinning makes it evictable again (#148).
func TestPinExemptsARetainedBoxFromEviction(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	pinned := retained(t, h, base.Add(-time.Hour))
	other := retained(t, h, base.Add(-2*time.Hour))
	if _, err := h.m.Pin(pinned); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// Both are past the age window; only the unpinned box goes.
	if _, err := h.m.ReapRetained(ctx, base, time.Hour, 0); err != nil {
		t.Fatalf("ReapRetained: %v", err)
	}
	if _, err := h.st.Box(pinned); err != nil {
		t.Fatalf("the pinned box was evicted: %v", err)
	}
	if _, err := h.st.Box(other); err == nil {
		t.Fatal("the unpinned box survived")
	}

	// Unpin, then it is evictable.
	if _, err := h.m.Unpin(pinned); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if _, err := h.m.ReapRetained(ctx, base, time.Hour, 0); err != nil {
		t.Fatalf("ReapRetained after unpin: %v", err)
	}
	if _, err := h.st.Box(pinned); err == nil {
		t.Fatal("the unpinned box survived")
	}
}

// TestReapOrphansCollectsOnlyGoneStoppedUnpinnedAgedBoxes proves each orphan
// condition is required: the checkout must be gone, the box stopped, unpinned,
// and unchanged past the grace window (#148).
func TestReapOrphansCollectsOnlyGoneStoppedUnpinnedAgedBoxes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	h.adapter.Workspace.Path = dir
	h.adapter.Workspace.Remote = ""

	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: dir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// aged is a clock well past the grace window from any recent update.
	aged := func() time.Time { return time.Now().UTC().Add(2 * time.Hour) }

	// Running: left alone even with the checkout gone.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove checkout: %v", err)
	}
	if n, err := h.m.ReapOrphans(ctx, aged(), time.Hour); err != nil || n != 0 {
		t.Fatalf("ReapOrphans while running = %d (%v), want 0", n, err)
	}

	if _, err := h.m.Stop(ctx, created.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stopped but unchanged inside the grace window: left alone.
	if n, err := h.m.ReapOrphans(ctx, time.Now().UTC(), time.Hour); err != nil || n != 0 {
		t.Fatalf("ReapOrphans inside grace = %d (%v), want 0", n, err)
	}
	// Stopped, aged, but pinned: left alone.
	if _, err := h.m.Pin(created.ID); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if n, err := h.m.ReapOrphans(ctx, aged(), time.Hour); err != nil || n != 0 {
		t.Fatalf("ReapOrphans while pinned = %d (%v), want 0", n, err)
	}
	if _, err := h.m.Unpin(created.ID); err != nil {
		t.Fatalf("Unpin: %v", err)
	}

	// Gone, stopped, unpinned, aged: collected with its workspace record.
	if n, err := h.m.ReapOrphans(ctx, aged(), time.Hour); err != nil || n != 1 {
		t.Fatalf("ReapOrphans = %d (%v), want 1", n, err)
	}
	if _, err := h.st.Box(created.ID); err == nil {
		t.Fatal("the orphaned box survived")
	}
	if _, err := h.st.WorkspaceByPath(dir); err == nil {
		t.Fatal("the dangling workspace record survived the collection")
	}
}

// TestReapOrphansLeavesALiveCheckout proves a box whose checkout still exists is
// never collected, however long it has been stopped.
func TestReapOrphansLeavesALiveCheckout(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := t.TempDir()
	h.adapter.Workspace.Path = dir
	h.adapter.Workspace.Remote = ""

	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: dir})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := h.m.Stop(ctx, created.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n, err := h.m.ReapOrphans(ctx, time.Now().UTC().Add(365*24*time.Hour), time.Hour); err != nil || n != 0 {
		t.Fatalf("ReapOrphans with a live checkout = %d (%v), want 0", n, err)
	}
	if _, err := h.st.Box(created.ID); err != nil {
		t.Fatalf("box with a live checkout was collected: %v", err)
	}
}

// TestCreateEphemeralIsFreshAndVolumeFree proves each CI box is a new record
// with no durable volumes, distinct from the workspace's durable box (#148).
func TestCreateEphemeralIsFreshAndVolumeFree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	durable, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.adapter.Workspace.Path})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first, err := h.m.CreateEphemeral(ctx, h.adapter.Workspace.Path, "")
	if err != nil {
		t.Fatalf("CreateEphemeral: %v", err)
	}
	second, err := h.m.CreateEphemeral(ctx, h.adapter.Workspace.Path, "")
	if err != nil {
		t.Fatalf("CreateEphemeral: %v", err)
	}
	if first.ID == second.ID || first.ID == durable.ID || second.ID == durable.ID {
		t.Fatalf("ephemeral boxes are not fresh: %s %s durable=%s", first.ID, second.ID, durable.ID)
	}
	if !first.Ephemeral || !second.Ephemeral {
		t.Fatalf("ephemeral flag not set: %+v %+v", first, second)
	}
	if len(first.Volumes) != 0 || len(second.Volumes) != 0 {
		t.Fatalf("ephemeral boxes mounted volumes: %+v %+v", first.Volumes, second.Volumes)
	}
	// The durable box keeps its volumes and its own container.
	if len(durable.Volumes) != 2 {
		t.Fatalf("durable volumes = %+v, want two", durable.Volumes)
	}
	if first.EngineID == durable.EngineID {
		t.Fatalf("ephemeral box reused the durable container %s", durable.EngineID)
	}
}
