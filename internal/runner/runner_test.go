package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/runner"
	"github.com/Siddhj2206/pluto/internal/state"
)

type fakeSys struct {
	mu       sync.Mutex
	states   map[string]string
	started  []string
	stopped  []string
	reset    []string
	reloads  int
	stubborn bool // Stop leaves the unit running
}

func newFakeSys() *fakeSys { return &fakeSys{states: map[string]string{}} }

func (f *fakeSys) set(unit, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[unit] = state
}

func (f *fakeSys) IsActive(unit string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.states[unit]; ok {
		return s, nil
	}
	return "inactive", nil
}

func (f *fakeSys) Start(unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, unit)
	f.states[unit] = "active"
	return nil
}

func (f *fakeSys) Stop(unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, unit)
	if !f.stubborn {
		f.states[unit] = "inactive"
	}
	return nil
}

func (f *fakeSys) ResetFailed(unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reset = append(f.reset, unit)
	if f.states[unit] == "failed" {
		f.states[unit] = "inactive"
	}
	return nil
}

func (f *fakeSys) DaemonReload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

type harness struct {
	r             *runner.Runner
	st            *state.Store
	sys           *fakeSys
	root          string
	ctrlAltDel    int
	ctrlAltDelErr error
	agent         *fakeAgent
	bundles       []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	sys := newFakeSys()
	h := &harness{st: st, sys: sys, root: st.Root()}
	r := runner.New(st, "/bin/pluto")
	r.Sys = sys
	r.UnitDir = filepath.Join(dir, "units")
	r.ReadyTimeout = time.Second
	r.CleanStopTimeout = 30 * time.Millisecond
	r.ForceStopTimeout = 30 * time.Millisecond
	r.PrepareDisk = func(boxDir, imageDir string) error {
		if err := os.MkdirAll(filepath.Join(boxDir, "disk"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(boxDir, "disk", "rootfs.img"), []byte("disk:"+filepath.Base(imageDir)), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(boxDir, "id"), []byte("key"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(boxDir, "id.pub"), []byte("pub"), 0o644)
	}
	r.WaitReady = func(ctx context.Context, uds string) error { return nil }
	r.CtrlAltDel = func(socket string) error {
		h.ctrlAltDel++
		return h.ctrlAltDelErr
	}
	h.agent = &fakeAgent{}
	r.NewAgent = func(vsockUDS string) runner.AgentClient { return h.agent }
	r.MakeBundle = func(ctx context.Context, worktree, out string) error {
		h.bundles = append(h.bundles, out)
		return os.WriteFile(out, []byte("bundle"), 0o644)
	}
	r.AgentTimeout = 300 * time.Millisecond
	h.r = r
	return h
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fakeImageDir writes a minimal artifact whose manifest matches its files,
// mirroring what images/build.sh produces.
func fakeImageDir(t *testing.T, dir, marker string) {
	t.Helper()
	write := func(name string, content []byte, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, content, mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	kernel := []byte("kernel-" + marker)
	rootfs := []byte("rootfs-" + marker)
	firecracker := []byte("firecracker-" + marker)
	write("vmlinuz", kernel, 0o644)
	write("rootfs.img", rootfs, 0o644)
	write(filepath.Join("cache", "firecracker"), firecracker, 0o755)
	manifest := map[string]any{
		"schema":      1,
		"built_at":    "2026-10-04T00:00:00Z",
		"kernel":      map[string]string{"sha256": sha(kernel)},
		"firecracker": map[string]string{"sha256": sha(firecracker)},
		"rootfs":      map[string]string{"sha256": sha(rootfs)},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	write("manifest.json", data, 0o644)
}

func (h *harness) importImage(t *testing.T, marker string) string {
	t.Helper()
	src := t.TempDir()
	fakeImageDir(t, src, marker)
	version, err := h.r.Import(src)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return version
}

func unitName(id string) string { return "pluto-box@" + id + ".service" }

func mustBox(t *testing.T, st *state.Store, id string) *state.Box {
	t.Helper()
	box, err := st.Box(id)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	return box
}

func (h *harness) newBox(t *testing.T) *state.Box {
	t.Helper()
	box, _, err := h.st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	return box
}

func TestImportInstallsVerifiedArtifact(t *testing.T) {
	h := newHarness(t)
	src := t.TempDir()
	fakeImageDir(t, src, "a")

	version, err := h.r.Import(src)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(version) != 16 {
		t.Fatalf("version = %q, want 16 hex chars", version)
	}
	for _, name := range []string{"vmlinuz", "rootfs.img", "firecracker", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(h.root, "images", version, name)); err != nil {
			t.Fatalf("imported image missing %s: %v", name, err)
		}
	}
	again, err := h.r.Import(src)
	if err != nil || again != version {
		t.Fatalf("second Import = %q, %v; want %q, nil", again, err, version)
	}
}

func TestImportRejectsTamperedArtifact(t *testing.T) {
	h := newHarness(t)
	src := t.TempDir()
	fakeImageDir(t, src, "a")
	if err := os.WriteFile(filepath.Join(src, "rootfs.img"), []byte("tampered"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := h.r.Import(src); err == nil {
		t.Fatal("Import should reject a rootfs that does not match its manifest")
	}
}

func TestCurrentImagePicksNewestImport(t *testing.T) {
	h := newHarness(t)
	first := h.importImage(t, "old")
	time.Sleep(20 * time.Millisecond)
	second := h.importImage(t, "new")
	got, err := h.r.CurrentImage()
	if err != nil {
		t.Fatalf("CurrentImage: %v", err)
	}
	if got != second {
		t.Fatalf("CurrentImage = %q, want newest %q (first was %q)", got, second, first)
	}
}

func TestCurrentImageWithoutImportsExplains(t *testing.T) {
	h := newHarness(t)
	_, err := h.r.CurrentImage()
	if err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("err = %v, want an import hint", err)
	}
}

func TestUpBootsFromCurrentImageAndWritesConfig(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	box := h.newBox(t)

	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
	if got.Image != version {
		t.Fatalf("image = %q, want %q", got.Image, version)
	}
	if len(h.sys.started) != 1 || h.sys.started[0] != unitName(box.ID) {
		t.Fatalf("started = %v, want %s", h.sys.started, unitName(box.ID))
	}

	data, err := os.ReadFile(filepath.Join(h.root, "boxes", box.ID, "fc.json"))
	if err != nil {
		t.Fatalf("read fc.json: %v", err)
	}
	for _, want := range []string{
		`"cache_type": "Writeback"`,
		`"uds_path"`,
		`"guest_cid"`,
		filepath.Join(h.root, "boxes", box.ID, "disk", "rootfs.img"),
		filepath.Join(h.root, "images", version, "vmlinuz"),
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("fc.json missing %q:\n%s", want, data)
		}
	}

	unit, err := os.ReadFile(filepath.Join(h.r.UnitDir, "pluto-box@.service"))
	if err != nil {
		t.Fatalf("unit template: %v", err)
	}
	if !strings.Contains(string(unit), "box run %i") {
		t.Fatalf("unit template = %s", unit)
	}
}

func TestUpBootsPinnedImageNotNewest(t *testing.T) {
	h := newHarness(t)
	pinned := h.importImage(t, "one")
	time.Sleep(20 * time.Millisecond)
	newest := h.importImage(t, "two")
	box := h.newBox(t)
	if _, err := h.st.SetImage(box.ID, pinned); err != nil {
		t.Fatalf("SetImage: %v", err)
	}

	if _, err := h.r.Up(context.Background(), mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Up: %v", err)
	}
	disk, err := os.ReadFile(filepath.Join(h.root, "boxes", box.ID, "disk", "rootfs.img"))
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(disk) != "disk:"+pinned {
		t.Fatalf("disk = %q, want the pinned image %s (newest is %s)", disk, pinned, newest)
	}
}

func TestUpWhenAlreadyActiveSkipsStart(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.SetImage(box.ID, version); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if _, err := h.st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	h.agent.status.BootID = "boot-1"
	if _, err := h.st.SetPhases(box.ID, state.Phases{Synced: true, BootID: "boot-1"}); err != nil {
		t.Fatalf("SetPhases: %v", err)
	}
	h.sys.set(unitName(box.ID), "active")

	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
	if len(h.sys.started) != 0 {
		t.Fatalf("started = %v, want no new start", h.sys.started)
	}
	if len(h.agent.applied) != 0 {
		t.Fatalf("applied = %d, want no wake on attach", len(h.agent.applied))
	}
}

func TestUpAppliesWhenGuestBootIDChanged(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	// The VMM restarted under us: the unit is active, but it is a new boot.
	if _, err := h.st.SetPhases(box.ID, state.Phases{Synced: true, BootID: "boot-1"}); err != nil {
		t.Fatalf("SetPhases: %v", err)
	}
	h.agent.status.BootID = "boot-2"
	h.sys.set(unitName(box.ID), "active")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(h.agent.applied) != 1 {
		t.Fatalf("applied = %d, want the contract applied for the new boot", len(h.agent.applied))
	}
}

func TestUpAppliesWhenActiveButNeverHandedOff(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	h.sys.set(unitName(box.ID), "active")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(h.agent.applied) != 1 {
		t.Fatalf("applied = %d, want the contract applied once", len(h.agent.applied))
	}
}

func TestUpMarksBoxFailedWhenNeverReady(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.r.WaitReady = func(ctx context.Context, uds string) error {
		return context.DeadlineExceeded
	}

	if _, err := h.r.Up(context.Background(), box); err == nil {
		t.Fatal("Up should fail when the box never becomes ready")
	}
	if got := mustBox(t, h.st, box.ID); got.State != state.StateFailed {
		t.Fatalf("state = %q, want failed", got.State)
	}
}

func TestUpClearsStaleSockets(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	boxDir := filepath.Join(h.root, "boxes", box.ID)
	for _, name := range []string{"firecracker.sock", "v.sock"} {
		if err := os.WriteFile(filepath.Join(boxDir, name), []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed stale %s: %v", name, err)
		}
	}

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, name := range []string{"firecracker.sock", "v.sock"} {
		if _, err := os.Stat(filepath.Join(boxDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale %s survived Up: %v", name, err)
		}
	}
}

func TestUpFailsFastWhenUnitDies(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.r.ReadyTimeout = 5 * time.Second
	h.r.WaitReady = func(ctx context.Context, uds string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.sys.set(unitName(box.ID), "failed")
	}()

	start := time.Now()
	if _, err := h.r.Up(context.Background(), box); err == nil {
		t.Fatal("Up should fail when the unit dies during startup")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Up took %s, want a fast failure once the unit is dead", elapsed)
	}
}

func TestPauseUsesCtrlAltDelAndWaitsForInactive(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := h.st.SetImage(box.ID, version); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	// The guest shuts down cleanly: the unit goes inactive after the request.
	h.r.CtrlAltDel = func(string) error {
		h.ctrlAltDel++
		h.sys.set(unitName(box.ID), "inactive")
		return nil
	}

	got, err := h.r.Pause(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", got.State)
	}
	if h.ctrlAltDel != 1 {
		t.Fatalf("ctrl-alt-del calls = %d, want 1", h.ctrlAltDel)
	}
	if len(h.sys.stopped) != 0 {
		t.Fatalf("force stop should not be needed: %v", h.sys.stopped)
	}
}

func TestPauseFallsBackToForceStop(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.r.CtrlAltDel = func(string) error { h.ctrlAltDel++; return nil }

	got, err := h.r.Pause(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if got.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", got.State)
	}
	if h.ctrlAltDel != 1 {
		t.Fatalf("ctrl-alt-del calls = %d, want 1", h.ctrlAltDel)
	}
	if len(h.sys.stopped) != 1 {
		t.Fatalf("stopped = %v, want one force stop", h.sys.stopped)
	}
}

func TestPauseRefusesWhenUnitWontStop(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.sys.stubborn = true
	h.r.CtrlAltDel = func(string) error { return nil }

	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err == nil {
		t.Fatal("Pause should fail when the unit refuses to stop")
	}
	if got := mustBox(t, h.st, box.ID); got.State != state.StateRunning {
		t.Fatalf("state = %q, want running (the machine is still up)", got.State)
	}
}

func TestPauseRefusesCreatedBox(t *testing.T) {
	h := newHarness(t)
	box := h.newBox(t)
	if _, err := h.r.Pause(box); err == nil {
		t.Fatal("Pause on a created box should fail")
	}
}

func TestReconcileStoppedUnitBecomesPaused(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.sys.set(unitName(box.ID), "inactive") // host rebooted or the unit was stopped

	got, err := h.r.Reconcile(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", got.State)
	}
}

func TestReconcileActiveUnitBecomesRunning(t *testing.T) {
	h := newHarness(t)
	box := h.newBox(t)
	h.sys.set(unitName(box.ID), "active")

	got, err := h.r.Reconcile(box)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != state.StateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
}

func TestReconcileFailedUnitBecomesFailed(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.sys.set(unitName(box.ID), "failed")

	got, err := h.r.Reconcile(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != state.StateFailed {
		t.Fatalf("state = %q, want failed", got.State)
	}
}

func TestDestroyStopsUnitAndRemovesBox(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if err := h.r.Destroy(box.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(h.sys.stopped) != 1 || h.sys.stopped[0] != unitName(box.ID) {
		t.Fatalf("stopped = %v, want %s", h.sys.stopped, unitName(box.ID))
	}
	if _, err := os.Stat(filepath.Join(h.root, "boxes", box.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("box dir still present: %v", err)
	}
	if _, err := h.st.Box(box.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("record after destroy = %v, want ErrNotFound", err)
	}
}

func TestDestroyRefusesWhenUnitWontStop(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.sys.stubborn = true

	if err := h.r.Destroy(box.ID); err == nil {
		t.Fatal("Destroy should refuse to remove a disk whose box is still running")
	}
	if _, err := os.Stat(filepath.Join(h.root, "boxes", box.ID)); err != nil {
		t.Fatalf("box dir should still exist: %v", err)
	}
}

func TestAttachWakesPausedBoxAndReturnsConnection(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.SetImage(box.ID, version); err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if _, err := h.st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	if _, err := h.st.Transition(box.ID, state.StatePaused); err != nil {
		t.Fatalf("paused: %v", err)
	}

	info, err := h.r.Attach(context.Background(), mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if info.User != "dev" || info.Port != 22 {
		t.Fatalf("info = %+v, want dev/22", info)
	}
	if !strings.HasSuffix(info.UDS, filepath.Join("boxes", box.ID, "v.sock")) {
		t.Fatalf("uds = %q", info.UDS)
	}
	if !strings.HasSuffix(info.Key, filepath.Join("boxes", box.ID, "id")) {
		t.Fatalf("key = %q", info.Key)
	}
	if got := mustBox(t, h.st, box.ID); got.State != state.StateRunning {
		t.Fatalf("state after attach = %q, want running", got.State)
	}
	if len(h.sys.started) != 1 {
		t.Fatalf("started = %v, want the box to be woken", h.sys.started)
	}
}

func TestImportReplacesPartialInstall(t *testing.T) {
	h := newHarness(t)
	src := t.TempDir()
	fakeImageDir(t, src, "a")
	version, err := h.r.Import(src)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := os.Remove(filepath.Join(h.root, "images", version, "vmlinuz")); err != nil {
		t.Fatalf("remove vmlinuz: %v", err)
	}
	again, err := h.r.Import(src)
	if err != nil || again != version {
		t.Fatalf("re-import = %q, %v; want %q, nil", again, err, version)
	}
	if _, err := os.Stat(filepath.Join(h.root, "images", version, "vmlinuz")); err != nil {
		t.Fatalf("partial import was not repaired: %v", err)
	}
}

func TestImagesListsImported(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	images, err := h.r.Images()
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	if len(images) != 1 || images[0].Version != version {
		t.Fatalf("images = %+v, want one entry %s", images, version)
	}
	if images[0].RootfsSHA256 == "" || images[0].KernelSHA256 == "" {
		t.Fatalf("image hashes missing: %+v", images[0])
	}
}

// fakeAgent stands in for the guest agent in runner tests.
type fakeAgent struct {
	mu        sync.Mutex
	pingErr   error
	status    state.Phases
	job       *state.Job
	applied   []*contract.Contract
	synced    []string
	logs      string
	jobLog    string
	logErr    error
	runErr    error
	runExit   int
	runChunks []string
	runPath   string
}

func (f *fakeAgent) Ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeAgent) Status() (state.Phases, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeAgent) JobStatus() (*state.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.job, nil
}

func (f *fakeAgent) Run(jobID string, argv []string, worktree string, emit func([]byte)) (*state.Job, error) {
	f.mu.Lock()
	chunks, exit, err := f.runChunks, f.runExit, f.runErr
	f.runPath = worktree
	for _, chunk := range chunks {
		f.jobLog += chunk
	}
	f.mu.Unlock()
	for _, chunk := range chunks {
		emit([]byte(chunk))
	}
	if err != nil {
		return nil, err
	}
	job := state.StartJob(jobID, argv)
	outcome := state.JobDone
	if exit != 0 {
		outcome = state.JobFailed
	}
	job.Finish(outcome, exit, "")
	f.mu.Lock()
	f.job = &job
	f.mu.Unlock()
	return &job, nil
}

func (f *fakeAgent) JobLog(jobID string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logErr != nil {
		return "", f.logErr
	}
	return f.jobLog, nil
}

func (f *fakeAgent) Sync(bundle, worktree, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = append(f.synced, worktree)
	f.status.Synced = true
	return nil
}

func (f *fakeAgent) Apply(ct *contract.Contract, worktree string) (state.Phases, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, ct)
	f.status.Worktree = worktree
	f.status.Provision = state.PhaseStatus{State: state.PhaseRunning}
	return f.status, nil
}

func (f *fakeAgent) Logs(phase, service string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs, nil
}

func (h *harness) newBoxAt(t *testing.T, worktree string) *state.Box {
	t.Helper()
	box, _, err := h.st.CreateBox("alpha", "main", worktree)
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	return box
}

func TestAutoPauseWindowReadsTheContract(t *testing.T) {
	h := newHarness(t)
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nauto_pause = \"5m\"\n")
	box := h.newBoxAt(t, worktree)

	if got := h.r.AutoPauseWindow(box); got != 5*time.Minute {
		t.Fatalf("window = %s, want 5m", got)
	}
}

func TestAutoPauseWindowDefaultsWithoutAContract(t *testing.T) {
	h := newHarness(t)
	box := h.newBox(t) // /src/alpha does not exist; no contract to read

	if got := h.r.AutoPauseWindow(box); got != contract.DefaultAutoPause {
		t.Fatalf("window = %s, want the default %s", got, contract.DefaultAutoPause)
	}
}

func TestAutoPauseWindowOffDisables(t *testing.T) {
	h := newHarness(t)
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nauto_pause = \"off\"\n")
	box := h.newBoxAt(t, worktree)

	if got := h.r.AutoPauseWindow(box); got != 0 {
		t.Fatalf("window = %s, want 0 (disabled)", got)
	}
}

func TestAutoPauseWindowUnreadableContractKeepsTheDefault(t *testing.T) {
	h := newHarness(t)
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nauto_pause = \"soon\"\n")
	box := h.newBoxAt(t, worktree)

	if got := h.r.AutoPauseWindow(box); got != contract.DefaultAutoPause {
		t.Fatalf("window = %s, want the default on a broken contract", got)
	}
}

func writeContract(t *testing.T, worktree, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, contract.FileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
}

func TestUpHandsOffContractAndPersistsPhases(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[wake]\ncommand = \"true\"\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	box := h.newBoxAt(t, worktree)

	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(h.agent.applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(h.agent.applied))
	}
	if ct := h.agent.applied[0]; ct.Wake == nil || ct.Wake.Command != "true" {
		t.Fatalf("contract = %+v", ct)
	}
	if len(h.bundles) != 1 {
		t.Fatalf("bundles = %v, want one", h.bundles)
	}
	if len(h.agent.synced) != 1 || h.agent.synced[0] != "/home/dev/work/alpha" {
		t.Fatalf("synced = %v, want /home/dev/work/alpha", h.agent.synced)
	}
	if got.Phases == nil || got.Phases.Provision.State != state.PhaseRunning {
		t.Fatalf("phases = %+v, want provision running", got.Phases)
	}
}

func TestUpSkipsSyncWhenAgentAlreadySynced(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	h.agent.status.Synced = true
	box := h.newBox(t)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(h.bundles) != 0 {
		t.Fatalf("bundles = %v, want none", h.bundles)
	}
	if len(h.agent.applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(h.agent.applied))
	}
}

func TestUpRecordsTheAppliedContractHash(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree)

	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	ct, err := contract.Load(worktree)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ContractHash != ct.Hash() {
		t.Fatalf("contract_hash = %q, want the applied hash %q", got.ContractHash, ct.Hash())
	}
	if h.r.ContractStale(got) {
		t.Fatal("the contract just applied reports stale")
	}
}

func TestContractStaleReportsAnEditedContract(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	applied := mustBox(t, h.st, box.ID)

	writeContract(t, worktree, "[wake]\ncommand = \"make test\"\n")
	if !h.r.ContractStale(applied) {
		t.Fatal("an edited contract is not reported stale")
	}
}

// Reformatting is not editing: comments, whitespace, key order, and table
// order parse to the same contract and must keep status quiet.
func TestContractStaleIgnoresReformatting(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nimage = \"ubuntu-24.04\"\n\n[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	applied := mustBox(t, h.st, box.ID)

	writeContract(t, worktree, "# touched by an editor\n[wake]\ncommand=\"true\"   # the fast path\n\n[box]\nimage=\"ubuntu-24.04\"\n")
	if h.r.ContractStale(applied) {
		t.Fatal("a formatting-only edit is reported stale")
	}
}

func TestContractStaleReportsARemovedContract(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	applied := mustBox(t, h.st, box.ID)

	if err := os.Remove(filepath.Join(worktree, contract.FileName)); err != nil {
		t.Fatalf("remove contract: %v", err)
	}
	if !h.r.ContractStale(applied) {
		t.Fatal("a removed contract is not reported stale")
	}
}

func TestContractStaleWithoutAnAppliedHashIsSilent(t *testing.T) {
	h := newHarness(t)
	worktree := t.TempDir()
	writeContract(t, worktree, "[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree) // never handed off: no hash recorded

	if h.r.ContractStale(box) {
		t.Fatal("a box with no applied hash is reported stale")
	}
}

func TestContractStaleOnUnreadableContractIsSilent(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[wake]\ncommand = \"true\"\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	applied := mustBox(t, h.st, box.ID)

	writeContract(t, worktree, "[wake]\ncommand = \n")
	if h.r.ContractStale(applied) {
		t.Fatal("an unparseable contract is reported stale")
	}
}

func TestUpHandoffFailureLeavesBoxRunning(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.agent.pingErr = errors.New("connection refused")

	_, err := h.r.Up(context.Background(), box)
	if err == nil || !strings.Contains(err.Error(), "handoff") {
		t.Fatalf("Up error = %v, want a handoff failure", err)
	}
	if got := mustBox(t, h.st, box.ID); got.State != state.StateRunning {
		t.Fatalf("state = %q, want running (the machine is up)", got.State)
	}
}

func TestRefreshPersistsAgentPhases(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.agent.status.Wake = state.PhaseStatus{State: state.PhaseDone, ExitCode: 0}

	got, err := h.r.Refresh(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Phases == nil || got.Phases.Wake.State != state.PhaseDone {
		t.Fatalf("phases = %+v", got.Phases)
	}
}

func TestLogsRequiresRunningBox(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Logs(box, "wake", "", 10); err == nil {
		t.Fatal("Logs on a created box should fail")
	}
	h.agent.logs = "wake output"
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	log, err := h.r.Logs(mustBox(t, h.st, box.ID), "wake", "", 10)
	if err != nil || log != "wake output" {
		t.Fatalf("Logs = %q, %v", log, err)
	}
}

func TestRunJobRunsInBoxAndRecordsOutcome(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.agent.runChunks = []string{"compiling\n", "ok\n"}

	var got []byte
	updated, job, err := h.r.RunJob(context.Background(), box, []string{"make", "test"}, func(data []byte) {
		got = append(got, data...)
	})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if string(got) != "compiling\nok\n" {
		t.Fatalf("streamed = %q, want the job's chunks", got)
	}
	if job.State != state.JobDone || job.ExitCode != 0 {
		t.Fatalf("job = %+v, want done", job)
	}

	// The box stays up and the outcome is on the durable record; the output
	// is on the host, so it is readable while the box sleeps.
	recorded := mustBox(t, h.st, box.ID)
	if recorded.State != state.StateRunning {
		t.Fatalf("box state = %q, want running", recorded.State)
	}
	if recorded.LatestJob() == nil || recorded.LatestJob().ID != job.ID || recorded.LatestJob().State != state.JobDone {
		t.Fatalf("recorded job = %+v, want the done job", recorded.LatestJob())
	}
	if recorded.LatestJob().Command != "make test" || recorded.LatestJob().Log == "" {
		t.Fatalf("recorded job = %+v, want a command and a log reference", recorded.LatestJob())
	}
	if updated.LatestJob().ID != job.ID {
		t.Fatalf("returned box job = %+v, want %s", updated.LatestJob(), job.ID)
	}
	if h.agent.runPath != "/home/dev/work/alpha" {
		t.Fatalf("job worktree = %q, want the box's worktree", h.agent.runPath)
	}
	log, err := h.r.JobLog(recorded, job.ID, 10)
	if err != nil {
		t.Fatalf("JobLog: %v", err)
	}
	if !strings.Contains(log, "compiling") || !strings.Contains(log, "ok") {
		t.Fatalf("job log = %q, want the streamed output", log)
	}
}

func TestRunJobRefusesConcurrentRun(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.BeginJob(box.ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}

	_, _, err := h.r.RunJob(context.Background(), box, []string{"make", "lint"}, func([]byte) {})
	if !errors.Is(err, state.ErrJobRunning) {
		t.Fatalf("RunJob error = %v, want ErrJobRunning", err)
	}
	if len(h.sys.started) != 0 {
		t.Fatalf("started = %v, want no boot for a refused run", h.sys.started)
	}
}

func TestRunJobRecordsFailureWhenBoxNeverBoots(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.r.WaitReady = func(ctx context.Context, uds string) error { return context.DeadlineExceeded }

	_, job, err := h.r.RunJob(context.Background(), box, []string{"make"}, func([]byte) {})
	if err != nil {
		t.Fatalf("a boot failure is a failed job, not a run error: %v", err)
	}
	if job.State != state.JobFailed || job.Error == "" {
		t.Fatalf("job = %+v, want failed with an error", job)
	}
	if recorded := mustBox(t, h.st, box.ID); recorded.LatestJob() == nil || recorded.LatestJob().State != state.JobFailed {
		t.Fatalf("recorded job = %+v, want failed", recorded.LatestJob())
	}
}

func TestRunJobRecordsStreamFailure(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.agent.runErr = errors.New("connection lost")

	_, job, err := h.r.RunJob(context.Background(), box, []string{"make"}, func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if job.State != state.JobFailed || job.Error != "connection lost" {
		t.Fatalf("job = %+v, want failed/connection lost", job)
	}
}

func TestJobLogRefusesUnknownJob(t *testing.T) {
	h := newHarness(t)
	box := h.newBox(t)
	if _, err := h.r.JobLog(box, state.NewID(), 10); err == nil {
		t.Fatal("JobLog for a box with no recorded job should fail")
	}
	if _, err := h.r.JobLog(box, "not-a-uuid", 10); err == nil {
		t.Fatal("JobLog with a malformed id should fail")
	}
}

func TestJobLogPrefersTheAgentCopy(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.agent.runChunks = []string{"complete output\n"}
	recorded, job, err := h.r.RunJob(context.Background(), box, []string{"make"}, func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	// Simulate a daemon that lost the stream: the host copy is partial, the
	// box's copy is whole.
	host := filepath.Join(h.root, "boxes", box.ID, "jobs", job.ID+".log")
	if err := os.WriteFile(host, []byte("partial\n"), 0o644); err != nil {
		t.Fatalf("truncate host log: %v", err)
	}

	log, err := h.r.JobLog(recorded, job.ID, 10)
	if err != nil {
		t.Fatalf("JobLog: %v", err)
	}
	if !strings.Contains(log, "complete output") {
		t.Fatalf("log = %q, want the agent's complete copy", log)
	}
}

func TestJobLogFallsBackToHostCopy(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	h.agent.runChunks = []string{"host copy\n"}
	recorded, job, err := h.r.RunJob(context.Background(), box, []string{"make"}, func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	// A paused box has no agent; the host copy is what remains.
	paused := *recorded
	paused.State = state.StatePaused
	log, err := h.r.JobLog(&paused, job.ID, 10)
	if err != nil {
		t.Fatalf("JobLog paused: %v", err)
	}
	if !strings.Contains(log, "host copy") {
		t.Fatalf("log = %q, want the host copy", log)
	}

	// A running box whose agent is unreachable also falls back.
	h.agent.logErr = errors.New("agent gone")
	log, err = h.r.JobLog(recorded, job.ID, 10)
	if err != nil {
		t.Fatalf("JobLog fallback: %v", err)
	}
	if !strings.Contains(log, "host copy") {
		t.Fatalf("log = %q, want the host copy", log)
	}
}

// TestJobLogReadsRetainedHistory pins that any retained job can be read, not
// just the latest: the agent's copy while the box runs, the host copy while
// it sleeps.
func TestJobLogReadsRetainedHistory(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)

	h.agent.runChunks = []string{"first output\n"}
	_, first, err := h.r.RunJob(context.Background(), box, []string{"first"}, func([]byte) {})
	if err != nil {
		t.Fatalf("first RunJob: %v", err)
	}
	h.agent.runChunks = []string{"second output\n"}
	recorded, second, err := h.r.RunJob(context.Background(), box, []string{"second"}, func([]byte) {})
	if err != nil {
		t.Fatalf("second RunJob: %v", err)
	}
	if recorded.LatestJob().ID != second.ID {
		t.Fatalf("latest = %+v, want the second job", recorded.LatestJob())
	}

	// The agent's copy covers any retained job while the box is up.
	log, err := h.r.JobLog(recorded, first.ID, 10)
	if err != nil {
		t.Fatalf("JobLog running: %v", err)
	}
	if !strings.Contains(log, "first output") {
		t.Fatalf("log = %q, want the older job's output", log)
	}

	// A paused box has no agent; the older job's host log remains readable.
	paused := *recorded
	paused.State = state.StatePaused
	log, err = h.r.JobLog(&paused, first.ID, 10)
	if err != nil {
		t.Fatalf("JobLog paused: %v", err)
	}
	if !strings.Contains(log, "first output") || strings.Contains(log, "second output") {
		t.Fatalf("log = %q, want only the older job's host copy", log)
	}

	if _, err := h.r.JobLog(recorded, state.NewID(), 10); err == nil {
		t.Fatal("JobLog for an unretained job should fail")
	}
}

func TestRefreshAdoptsAgentJobOutcome(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The daemon started the job and then lost contact; the agent knows how
	// it actually ended.
	job := state.StartJob(state.NewID(), []string{"make", "test"})
	if _, err := h.st.BeginJob(box.ID, job); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	done := job
	done.State = state.JobDone
	now := time.Now().UTC()
	done.FinishedAt = &now
	h.agent.job = &done

	got, err := h.r.Refresh(mustBox(t, h.st, box.ID))
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.LatestJob() == nil || got.LatestJob().State != state.JobDone {
		t.Fatalf("job after refresh = %+v, want the agent's outcome", got.LatestJob())
	}
}

func TestPauseFailsRunningJob(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := h.st.BeginJob(box.ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	h.r.CtrlAltDel = func(string) error {
		h.sys.set(unitName(box.ID), "inactive")
		return nil
	}

	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got := mustBox(t, h.st, box.ID)
	if got.LatestJob() == nil || got.LatestJob().State != state.JobFailed || got.LatestJob().Error == "" {
		t.Fatalf("job after pause = %+v, want failed", got.LatestJob())
	}
}

func TestReconcileAllClearsJobOnStoppedBox(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)
	if _, err := h.st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatalf("running: %v", err)
	}
	if _, err := h.st.BeginJob(box.ID, state.StartJob(state.NewID(), []string{"make"})); err != nil {
		t.Fatalf("BeginJob: %v", err)
	}
	// The host rebooted: the unit is gone, so the job cannot have survived it.

	h.r.ReconcileAll()
	got := mustBox(t, h.st, box.ID)
	if got.State != state.StatePaused {
		t.Fatalf("state = %q, want paused", got.State)
	}
	if got.LatestJob() == nil || got.LatestJob().State != state.JobFailed {
		t.Fatalf("job = %+v, want failed", got.LatestJob())
	}
}
