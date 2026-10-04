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

	"github.com/Siddhj2206/pluto/internal/runner"
	"github.com/Siddhj2206/pluto/internal/state"
)

type fakeSys struct {
	mu      sync.Mutex
	states  map[string]string
	started []string
	stopped []string
	reset   []string
	reloads int
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
	f.states[unit] = "inactive"
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

	got, err := h.r.Pause(context.Background(), mustBox(t, h.st, box.ID))
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

	got, err := h.r.Pause(context.Background(), mustBox(t, h.st, box.ID))
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

func TestPauseRefusesCreatedBox(t *testing.T) {
	h := newHarness(t)
	box := h.newBox(t)
	if _, err := h.r.Pause(context.Background(), box); err == nil {
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

	if err := h.r.Destroy(context.Background(), box.ID); err != nil {
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
