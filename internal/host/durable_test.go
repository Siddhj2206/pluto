package host

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

// putBox stores a durable box record and its container. inUse is what the
// idle check finds.
func (x *harness) putBox(name string, running, privileged, inUse bool) {
	x.t.Helper()
	must(x.t, x.h.Store.PutBox(context.Background(), store.Box{Name: name, Repo: testRepo, Image: "img", Privileged: privileged,
		WorkspaceVolume: name + "-workspace", HomeVolume: name + "-home", CreatedAt: x.now}))
	status := "0"
	if inUse {
		status = "1"
	}
	x.eng.boxes[name] = &fakeBox{running: running, status: status, spec: engine.Spec{Name: name, Durable: true, Privileged: privileged}}
}

func TestBoxReconcile(t *testing.T) {
	tests := []struct {
		name                 string
		running, priv, inUse bool
		granted, activeRun   bool
		wantStopped          bool
		wantRemoved          bool
	}{
		{name: "an idle running box stops", running: true, wantStopped: true},
		{name: "a box with a shell in it keeps running", running: true, inUse: true},
		{name: "a box with an active run is left alone", running: true, activeRun: true},
		{name: "a stopped box stays as it is", running: false},
		{name: "a granted privileged box just stops", running: true, priv: true, granted: true, wantStopped: true},
		{name: "a revoked privileged box loses its container", running: true, priv: true, wantRemoved: true},
		{name: "a stopped revoked box loses its container", running: false, priv: true, wantRemoved: true},
		{name: "a revoked box in use waits for its shell to end", running: true, priv: true, inUse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			x := newTaskHarness(t)
			x.putBox("pluto-x-box-a", tt.running, tt.priv, tt.inUse)
			if tt.granted {
				must(t, x.h.Store.Grant(ctx, testRepo, task.Privileged))
			}
			if tt.activeRun {
				x.putRun(store.Run{Box: "pluto-x-box-a", State: store.StateRunning})
			}
			must(t, x.h.reconcileBoxes(ctx))
			if got := slices.Contains(x.eng.stopped, "pluto-x-box-a"); got != tt.wantStopped {
				t.Errorf("stopped = %v, want %v", got, tt.wantStopped)
			}
			if got := slices.Contains(x.eng.removed, "pluto-x-box-a"); got != tt.wantRemoved {
				t.Errorf("removed = %v, want %v", got, tt.wantRemoved)
			}
			if len(x.eng.removedVols) != 0 {
				t.Errorf("volumes removed: %v", x.eng.removedVols)
			}
			if _, err := x.h.Store.BoxByName(ctx, "pluto-x-box-a"); err != nil {
				t.Errorf("the record went: %v", err)
			}
		})
	}
}

func TestADurableContainerWithNoRecordGoesAndItsVolumesStay(t *testing.T) {
	x := newTaskHarness(t)
	x.eng.boxes["pluto-x-box-gone"] = &fakeBox{spec: engine.Spec{Name: "pluto-x-box-gone", Durable: true}}
	x.putBox("pluto-x-box-kept", false, false, false)
	must(t, x.h.reconcileBoxes(context.Background()))
	if !slices.Equal(x.eng.removed, []string{"pluto-x-box-gone"}) || len(x.eng.removedVols) != 0 {
		t.Fatalf("removed %v, volumes %v", x.eng.removed, x.eng.removedVols)
	}
}

func TestAnEnteredBoxIsLeftUpForAMoment(t *testing.T) {
	x := newTaskHarness(t)
	x.putBox("pluto-x-box-a", false, false, false)
	x.h.noteEntered("pluto-x-box-a") // As pluto box enter and pluto enter do.
	x.eng.boxes["pluto-x-box-a"].running = true
	must(t, x.h.reconcileBoxes(context.Background()))
	if len(x.eng.stopped) != 0 {
		t.Fatal("a box was stopped just after someone asked to enter it")
	}
	x.now = x.now.Add(enterGrace)
	must(t, x.h.reconcileBoxes(context.Background()))
	if !slices.Contains(x.eng.stopped, "pluto-x-box-a") {
		t.Fatal("an idle box stayed up after the grace period")
	}
}

func TestARevokeReconcilesOnTheNextPass(t *testing.T) {
	ctx := context.Background()
	x := newTaskHarness(t)
	must(t, x.h.Store.Grant(ctx, testRepo, task.Privileged))
	x.putBox("pluto-x-box-a", false, true, false)
	x.tick() // The first pass sweeps; nothing to do while the grant stands.
	if slices.Contains(x.eng.removed, "pluto-x-box-a") {
		t.Fatal("removed a granted box")
	}
	rec := x.call(http.MethodDelete, "/v1/grants?repo="+testRepo+"&name="+task.Privileged)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	x.now = x.now.Add(3 * time.Second)
	must(t, x.h.Tick(ctx))
	if !slices.Contains(x.eng.removed, "pluto-x-box-a") {
		t.Fatal("the revoked box's container stayed until the next sweep")
	}
}

func TestARevokedBoxInUseIsCheckedEveryPassUntilItGoes(t *testing.T) {
	ctx := context.Background()
	x := newTaskHarness(t)
	x.putBox("pluto-x-box-a", true, true, true)
	must(t, x.h.reconcileBoxes(ctx))
	if slices.Contains(x.eng.removed, "pluto-x-box-a") {
		t.Fatal("removed a box with a shell open")
	}
	if !x.h.takeBoxesDue() {
		t.Fatal("the next pass won't check it again")
	}
	x.eng.boxes["pluto-x-box-a"].status = "0" // The shell ended.
	must(t, x.h.reconcileBoxes(ctx))
	if !slices.Contains(x.eng.removed, "pluto-x-box-a") {
		t.Fatal("the revoked box stayed after its shell ended")
	}
}
