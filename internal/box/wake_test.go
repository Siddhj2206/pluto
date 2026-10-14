package box_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// wakeHarness is a manager over a real workspace directory, so contract.LoadProject
// (which reads pluto.yml from the workspace path) resolves the declared wake phase.
type wakeHarness struct {
	*harness
	workspace string
	mu        sync.Mutex
	execs     []container.ExecSpec
}

func newWakeHarness(t *testing.T, yml string, exec func(container.ExecSpec) (container.ExecResult, error)) *wakeHarness {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pluto.yml"), []byte(yml), 0o644); err != nil {
		t.Fatalf("write pluto.yml: %v", err)
	}
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	engine := container.NewFake()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: dir, Remote: "git@example.com:app"}, Revision: vcs.Revision{Commit: "abc123"}}
	h := &wakeHarness{harness: &harness{m: box.New(st, engine, adapter), st: st, engine: engine, adapter: adapter}, workspace: dir}
	engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		h.mu.Lock()
		h.execs = append(h.execs, spec)
		h.mu.Unlock()
		if exec != nil {
			return exec(spec)
		}
		return container.ExecResult{}, nil
	}
	return h
}

// wakeExecs returns the execs whose argv matches the wake phase's command.
func (h *wakeHarness) wakeExecs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.execs {
		if len(e.Argv) == 3 && e.Argv[0] == "/bin/sh" && e.Argv[2] == "echo wake" {
			n++
		}
	}
	return n
}

// TestWakePhaseRunsOnEveryStart proves the wake phase runs every time the box
// starts: on first use, after a stop, and after a wake failure retry — but a
// thaw of a frozen box is transparent and runs no wake.
func TestWakePhaseRunsOnEveryStart(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, "version: 1\nwake:\n  run: \"echo wake\"\n", nil)

	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := h.wakeExecs(); got != 1 {
		t.Fatalf("wake execs after create = %d, want 1", got)
	}

	if _, err := h.m.Stop(ctx, created.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	started, err := h.m.Start(ctx, created.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.wakeExecs(); got != 2 {
		t.Fatalf("wake execs after restart = %d, want 2", got)
	}

	// Freezing and waking the box thaws it transparently: no wake phase runs.
	frozen, err := h.m.Freeze(ctx, started.ID)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if frozen.State != state.StateFrozen {
		t.Fatalf("state = %q, want frozen", frozen.State)
	}
	thawed, err := h.m.Start(ctx, frozen.ID)
	if err != nil {
		t.Fatalf("Start (thaw): %v", err)
	}
	if thawed.State != state.StateRunning {
		t.Fatalf("thawed state = %q, want running", thawed.State)
	}
	if got := h.wakeExecs(); got != 2 {
		t.Fatalf("wake execs after thaw = %d, want still 2 (thaw runs no wake)", got)
	}
	if !containsOp(h.engine.Calls(), "thaw") {
		t.Fatalf("engine calls %v, want a thaw", h.engine.Calls())
	}
}

// TestFailedWakeEntersWakeFailedAndSurfacesStep proves a non-zero wake command
// leaves the box in wake_failed with the failing step and exit code recorded,
// and the box stays retained.
func TestFailedWakeEntersWakeFailedAndSurfacesStep(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, "version: 1\nwake:\n  run: \"echo wake\"\n", func(spec container.ExecSpec) (container.ExecResult, error) {
		if len(spec.Argv) == 3 && spec.Argv[2] == "echo wake" {
			return container.ExecResult{ExitCode: 7}, nil
		}
		return container.ExecResult{}, nil
	})

	_, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err == nil {
		t.Fatal("Ensure succeeded despite a failing wake phase")
	}

	box, err := h.m.List(ctx)
	if err != nil || len(box) != 1 {
		t.Fatalf("boxes = %v (%v), want the retained box", box, err)
	}
	if box[0].State != state.StateWakeFailed {
		t.Fatalf("state = %q, want wake_failed", box[0].State)
	}
	if box[0].Phases == nil || box[0].Phases.Wake.State != state.PhaseFailed {
		t.Fatalf("wake phase = %+v, want failed", box[0].Phases)
	}
	if box[0].Phases.Wake.ExitCode != 7 {
		t.Fatalf("wake exit code = %d, want 7", box[0].Phases.Wake.ExitCode)
	}
}

// TestIdleFreezesThenStopsAndWorkWakes pins the two idle windows: a running box
// freezes at the short window, a frozen box stops at the long window, and the
// next work wakes it with the wake phase.
func TestIdleFreezesThenStopsAndWorkWakes(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, "version: 1\nwake:\n  run: \"echo wake\"\n", nil)

	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := h.wakeExecs(); got != 1 {
		t.Fatalf("wake execs after create = %d, want 1", got)
	}

	const freezeAfter, stopAfter = 5 * time.Minute, 30 * time.Minute
	base := time.Now().UTC()

	// First reap starts the idle clock; no transition yet.
	if err := h.m.ReapIdle(ctx, base, freezeAfter, stopAfter); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if got := mustBox(t, h.st, created.ID).State; got != state.StateRunning {
		t.Fatalf("state after first reap = %q, want running", got)
	}

	// At the short window the box freezes, keeping its volumes and record.
	if err := h.m.ReapIdle(ctx, base.Add(freezeAfter), freezeAfter, stopAfter); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	frozen := mustBox(t, h.st, created.ID)
	if frozen.State != state.StateFrozen {
		t.Fatalf("state at freeze window = %q, want frozen", frozen.State)
	}
	if len(frozen.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want them kept through the freeze", frozen.Volumes)
	}

	// At the long window it stops, keeping its volumes and record.
	if err := h.m.ReapIdle(ctx, base.Add(stopAfter), freezeAfter, stopAfter); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	stopped := mustBox(t, h.st, created.ID)
	if stopped.State != state.StateStopped {
		t.Fatalf("state at stop window = %q, want stopped", stopped.State)
	}
	if len(stopped.Volumes) != 2 {
		t.Fatalf("volumes = %+v, want them kept through the stop", stopped.Volumes)
	}

	// The next work wakes it and runs the wake phase again.
	woken, err := h.m.Start(ctx, created.ID)
	if err != nil {
		t.Fatalf("Start (wake): %v", err)
	}
	if woken.State != state.StateRunning {
		t.Fatalf("state after wake = %q, want running", woken.State)
	}
	if got := h.wakeExecs(); got != 2 {
		t.Fatalf("wake execs after wake = %d, want 2", got)
	}
}

// TestIdleLeavesBusyBoxesRunning proves a box with an active run is never
// frozen or stopped: idle transitions happen only when nothing is attached and
// no run is active.
func TestIdleLeavesBusyBoxesRunning(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, "version: 1\ntasks:\n  hold: { run: \"hold\", lock: exclusive }\n", nil)

	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	h.engine.ExecFunc = func(_ string, spec container.ExecSpec, _ io.Writer) (container.ExecResult, error) {
		close(entered)
		<-release
		return container.ExecResult{}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := h.m.Run(ctx, created.ID, contract.LockExclusive, container.ExecSpec{Argv: []string{"hold"}}, io.Discard)
		done <- err
	}()
	<-entered

	base := time.Now().UTC()
	if err := h.m.ReapIdle(ctx, base, 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if err := h.m.ReapIdle(ctx, base.Add(time.Hour), 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if got := mustBox(t, h.st, created.ID).State; got != state.StateRunning {
		t.Fatalf("busy box state = %q, want running", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestRebaseSwapsLayersAndKeepsVolumes proves wake recomputes the desired
// layers and swaps them beneath the box's volumes: a changed provision rebuilds
// the package layer, recreates the container with the same volumes, and leaves
// the volumes and record intact.
func TestRebaseSwapsLayersAndKeepsVolumes(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, provisionYML("nodejs"), nil)
	if err := os.WriteFile(filepath.Join(h.workspace, "pnpm-lock.yaml"), []byte("lockfileVersion: 9\n"), 0o644); err != nil {
		t.Fatalf("write lockfile: %v", err)
	}
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(created.Layers) != 2 {
		t.Fatalf("layers = %+v, want base and package", created.Layers)
	}
	firstPackage := created.Layers[1].Key
	if _, err := h.m.Stop(ctx, created.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// The lockfile changes: the package layer's content address moves.
	if err := os.WriteFile(filepath.Join(h.workspace, "pnpm-lock.yaml"), []byte("lockfileVersion: 10\n"), 0o644); err != nil {
		t.Fatalf("rewrite lockfile: %v", err)
	}
	started, err := h.m.Start(ctx, created.ID)
	if err != nil {
		t.Fatalf("Start (wake): %v", err)
	}
	if len(started.Layers) != 2 || started.Layers[1].Key == firstPackage {
		t.Fatalf("layers after wake = %+v, want the rebuilt package layer", started.Layers)
	}
	// The volumes are untouched: same names, same count.
	if len(started.Volumes) != 2 || started.Volumes[0] != created.Volumes[0] || started.Volumes[1] != created.Volumes[1] {
		t.Fatalf("volumes after wake = %+v, want the original %+v", started.Volumes, created.Volumes)
	}
	if started.EngineID == created.EngineID {
		t.Fatal("rebase did not recreate the container")
	}
	if st, ok := h.engine.State(started.EngineID); !ok || st != container.StateRunning {
		t.Fatalf("engine state = %q (ok=%v), want running", st, ok)
	}
}

// provisionYML is a pluto.yml with a package layer installing pkg on ubuntu.
func provisionYML(pkg string) string {
	return "version: 1\nprovision:\n  base: ubuntu:24.04\n  packages: [" + pkg + "]\n  watch: [pnpm-lock.yaml]\n"
}

// TestIdleLeavesAttachedBoxRunning proves an attached session — a shared
// holder on the gate, the seam #144 registers through — keeps a box awake: idle
// transitions happen only when nothing is attached.
func TestIdleLeavesAttachedBoxRunning(t *testing.T) {
	ctx := context.Background()
	h := newWakeHarness(t, "version: 1\n", nil)
	created, _, err := h.m.Ensure(ctx, box.CreateRequest{Workspace: h.workspace})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	release, err := h.m.AcquireShared(ctx, created.ID)
	if err != nil {
		t.Fatalf("AcquireShared: %v", err)
	}
	base := time.Now().UTC()
	if err := h.m.ReapIdle(ctx, base, 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if err := h.m.ReapIdle(ctx, base.Add(time.Hour), 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if got := mustBox(t, h.st, created.ID).State; got != state.StateRunning {
		t.Fatalf("attached box state = %q, want running", got)
	}
	release()
	// With the attachment gone the clock starts, then the box freezes.
	if err := h.m.ReapIdle(ctx, base.Add(time.Hour), 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if err := h.m.ReapIdle(ctx, base.Add(2*time.Hour), 5*time.Minute, 30*time.Minute); err != nil {
		t.Fatalf("ReapIdle: %v", err)
	}
	if got := mustBox(t, h.st, created.ID).State; got != state.StateFrozen {
		t.Fatalf("state after detach = %q, want frozen", got)
	}
}

func mustBox(t *testing.T, st *state.Store, id string) *state.Box {
	t.Helper()
	b, err := st.Box(id)
	if err != nil {
		t.Fatalf("Box(%s): %v", id, err)
	}
	return b
}
