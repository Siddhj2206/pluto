package box_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// harness bundles the real store with controllable vcs and engine fakes.
type harness struct {
	m       *box.Manager
	st      *state.Store
	engine  *container.Fake
	adapter *vcs.Fake
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	engine := container.NewFake()
	adapter := &vcs.Fake{
		Workspace: vcs.Workspace{Path: "/src/app", Remote: "git@example.com:app"},
		Revision:  vcs.Revision{Commit: "abc123"},
	}
	return &harness{m: box.New(st, engine, adapter), st: st, engine: engine, adapter: adapter}
}

// TestEnsureCreatesOneRunningBoxPerWorkspace is the core acceptance slice: a
// workspace resolves through the vcs adapter to exactly one box, seeded from a
// resolved layer with its volumes ensured, created, started, and recorded.
func TestEnsureCreatesOneRunningBoxPerWorkspace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	created, isNew, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app", Image: "ubuntu:24.04"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !isNew {
		t.Fatal("first Ensure should create a box")
	}
	if created.State != state.StateRunning {
		t.Fatalf("state = %q, want running", created.State)
	}
	if created.WorkspacePath != "/src/app" || created.Repo != "git@example.com:app" || created.Revision != "abc123" {
		t.Fatalf("box = %+v", created)
	}
	if len(h.adapter.Calls) != 1 || h.adapter.Calls[0] != "/src/app" {
		t.Fatalf("vcs calls = %v, want the workspace path", h.adapter.Calls)
	}
	if len(created.Layers) != 1 || created.Layers[0].Kind != "base" {
		t.Fatalf("layers = %+v, want one base layer", created.Layers)
	}
	if len(created.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want workspace and home", created.Volumes)
	}
	if created.EngineID == "" {
		t.Fatal("engine id not recorded")
	}

	// Resolve then build then start: the seam verbs leave their record.
	calls := h.engine.Calls()
	for _, op := range []string{"resolve-layer", "build-layer", "ensure-volume", "create", "start"} {
		if !containsOp(calls, op) {
			t.Fatalf("engine calls %v missing %q", calls, op)
		}
	}

	// A workspace resolves to exactly one box.
	again, isNew, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app", Image: "ubuntu:99"})
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if isNew || again.ID != created.ID {
		t.Fatalf("second Ensure created a new box: %+v isNew=%v", again, isNew)
	}
	if again.State != state.StateRunning {
		t.Fatalf("second Ensure state = %q, want running", again.State)
	}
}

// TestEnsureBuildsMissingLayer proves a miss resolves to a build.
func TestEnsureBuildsMissingLayer(t *testing.T) {
	h := newHarness(t)
	if _, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: "/src/app", Image: "ubuntu:24.04"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	built := false
	for _, c := range h.engine.Calls() {
		if len(c) > len("build-layer") && c[:len("build-layer")] == "build-layer" {
			built = true
		}
	}
	if !built {
		t.Fatalf("calls = %v, want a build-layer", h.engine.Calls())
	}
}

// TestStopAndStartMoveTheRecordAndEngine proves stop/start drive both the
// record and the engine.
func TestStopAndStartMoveTheRecordAndEngine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	stopped, err := h.m.Stop(ctx, created.ID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.State != state.StateStopped {
		t.Fatalf("state = %q, want stopped", stopped.State)
	}
	if st, _ := h.engine.State(created.EngineID); st != container.StateStopped {
		t.Fatalf("engine state = %q, want stopped", st)
	}

	started, err := h.m.Start(ctx, created.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.State != state.StateRunning {
		t.Fatalf("state = %q, want running", started.State)
	}
	if st, _ := h.engine.State(created.EngineID); st != container.StateRunning {
		t.Fatalf("engine state = %q, want running", st)
	}
}

// TestStatusReconcilesWithTheEngine proves status reports the engine's truth
// even when the record lagged.
func TestStatusReconcilesWithTheEngine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := h.engine.Stop(ctx, created.EngineID); err != nil {
		t.Fatalf("engine.Stop: %v", err)
	}

	got, err := h.m.Status(ctx, created.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got.State != state.StateStopped {
		t.Fatalf("state = %q, want stopped", got.State)
	}
}

// TestRemoveDestroysContainerAndVolumes proves rm tears down the container and
// every durable volume, then the record.
func TestRemoveDestroysContainerAndVolumes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if err := h.m.Remove(ctx, created.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := h.st.Box(created.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("box after remove = %v, want ErrNotFound", err)
	}
	if _, ok := h.engine.State(created.EngineID); ok {
		t.Fatal("container still present after Remove")
	}
	removed := 0
	for _, c := range h.engine.Calls() {
		if len(c) > len("remove-volume") && c[:len("remove-volume")] == "remove-volume" {
			removed++
		}
	}
	if removed != len(created.Volumes) {
		t.Fatalf("removed %d volumes, want %d", removed, len(created.Volumes))
	}
}

// TestListReturnsBoxes proves list surfaces the boxes the manager created.
func TestListReturnsBoxes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	boxes, err := h.m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(boxes) != 1 || boxes[0].WorkspacePath != "/src/app" {
		t.Fatalf("boxes = %+v", boxes)
	}
}

// TestEnsurePropagatesResolveFailure proves a workspace the adapter cannot
// resolve creates nothing.
func TestEnsurePropagatesResolveFailure(t *testing.T) {
	h := newHarness(t)
	h.adapter.Err = errors.New("not a workspace")
	if _, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: "/nope"}); err == nil {
		t.Fatal("Ensure succeeded on an unresolvable workspace")
	}
	if boxes, _ := h.m.List(context.Background()); len(boxes) != 0 {
		t.Fatalf("boxes = %+v, want none", boxes)
	}
}

// TestExecStreamsThroughTheEngine proves the manager carries a byte stream to
// the engine.
func TestExecStreamsThroughTheEngine(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: "/src/app"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	h.engine.ExecFunc = func(_ string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		if len(spec.Argv) != 2 || spec.Argv[0] != "echo" {
			t.Errorf("exec argv = %v", spec.Argv)
		}
		_, _ = sink.Write([]byte("hi\n"))
		return container.ExecResult{ExitCode: 0}, nil
	}
	var sink bytes.Buffer
	if _, err := h.m.Exec(ctx, created.ID, container.ExecSpec{Argv: []string{"echo", "hi"}}, &sink); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if sink.String() != "hi\n" {
		t.Fatalf("sink = %q, want the streamed output", sink.String())
	}
}

// containsOp reports whether a recorded "op:id" call has the given op prefix.
func containsOp(calls []string, op string) bool {
	for _, c := range calls {
		if c == op || len(c) > len(op) && c[:len(op)+1] == op+":" {
			return true
		}
	}
	return false
}
