package runner_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

const layerProjectURL = "https://example.test/acme/app.git"

// layerRecorder captures how the layer seams were exercised: base is how many
// times the base image was cloned, prepared the layer directories used, and
// scrubbed the images a publish passed through the scrubber.
type layerRecorder struct {
	mu       sync.Mutex
	base     int
	prepared []string
	scrubbed []string
}

func (r *layerRecorder) baseClones() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.base
}

// withLayerHarness replaces the disk and scrub seams so layer behavior is
// observable without KVM or a real ext4 image.
func (h *harness) withLayerHarness(t *testing.T) *layerRecorder {
	t.Helper()
	rec := &layerRecorder{}
	base := h.r.PrepareDisk
	h.r.PrepareDisk = func(boxDir, imageDir string, diskMiB int) error {
		rec.mu.Lock()
		rec.base++
		rec.mu.Unlock()
		return base(boxDir, imageDir, diskMiB)
	}
	h.r.PrepareLayerDisk = func(boxDir, layerDir string, diskMiB int) error {
		rec.mu.Lock()
		rec.prepared = append(rec.prepared, layerDir)
		rec.mu.Unlock()
		if err := os.MkdirAll(filepath.Join(boxDir, "disk"), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(layerDir, "rootfs.img"))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(boxDir, "disk", "rootfs.img"), data, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(boxDir, "id"), []byte("key"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(boxDir, "id.pub"), []byte("pub"), 0o644)
	}
	h.r.ScrubEnvironment = func(image string) error {
		rec.mu.Lock()
		rec.scrubbed = append(rec.scrubbed, image)
		rec.mu.Unlock()
		return nil
	}
	return rec
}

func (h *harness) layerBox(t *testing.T, worktree, trust string) *state.Box {
	t.Helper()
	box := h.newBoxAt(t, worktree)
	if _, err := h.st.SetPrimaryRepoURL(box.ID, layerProjectURL); err != nil {
		t.Fatalf("SetPrimaryRepoURL: %v", err)
	}
	if trust != "" && trust != state.TrustClassTrusted {
		if _, err := h.st.SetTrustClass(box.ID, trust); err != nil {
			t.Fatalf("SetTrustClass: %v", err)
		}
	}
	return mustBox(t, h.st, box.ID)
}

func layerKey(t *testing.T, worktree, version string, trust envcache.Trust) string {
	t.Helper()
	ct, err := contract.Load(worktree)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key, err := envcache.Key(envcache.Inputs{Project: layerProjectURL, Setup: ct.SetupHash(), Image: version, Trust: trust})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	return key
}

func publishLayer(t *testing.T, h *harness, key, content string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "rootfs.img")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatalf("write layer source: %v", err)
	}
	if err := h.r.EnvironmentCache.Publish(key, src, func(string) error { return nil }); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func writeCacheContract(t *testing.T, worktree, body string) {
	t.Helper()
	writeContract(t, worktree, body)
}

// With no opt-in, a box boots from the base image and nothing is ever
// published, even after a successful provision and clean stop.
func TestUpUsesBaseImageWhenCacheDisabled(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\n")
	box := h.layerBox(t, worktree, "")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if rec.baseClones() != 1 || len(rec.prepared) != 0 {
		t.Fatalf("base=%d prepared=%v, want one base clone and no layer", rec.baseClones(), rec.prepared)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if len(rec.scrubbed) != 0 {
		t.Fatalf("scrubbed = %v, want no layer published without opt-in", rec.scrubbed)
	}
}

// A ready layer is cloned instead of the base image, and the box's disk is
// that layer's bytes.
func TestUpClonesLayerOnCacheHit(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, "")
	key := layerKey(t, worktree, version, envcache.Trusted)
	publishLayer(t, h, key, "layer-rootfs-bytes")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if rec.baseClones() != 0 {
		t.Fatalf("base clones = %d, want the cache hit to skip the base image", rec.baseClones())
	}
	layerDir, err := h.r.EnvironmentCache.LayerDir(key)
	if err != nil {
		t.Fatalf("LayerDir: %v", err)
	}
	if len(rec.prepared) != 1 || rec.prepared[0] != layerDir {
		t.Fatalf("prepared = %v, want [%s]", rec.prepared, layerDir)
	}
	data, err := os.ReadFile(filepath.Join(h.root, "boxes", box.ID, "disk", "rootfs.img"))
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(data) != "layer-rootfs-bytes" {
		t.Fatalf("disk = %q, want the layer's bytes", data)
	}
}

// A coordinated miss clones the base image, and a successful provision plus a
// clean stop publishes a scrubbed layer under the box's key.
func TestPausePublishesAfterProvisionAndCleanStop(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, "")
	key := layerKey(t, worktree, version, envcache.Trusted)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if len(rec.scrubbed) != 1 {
		t.Fatalf("scrubbed = %v, want the layer scrubbed once", rec.scrubbed)
	}
	layerDir, err := h.r.EnvironmentCache.LayerDir(key)
	if err != nil {
		t.Fatalf("LayerDir after publish: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(layerDir, "rootfs.img"))
	if err != nil {
		t.Fatalf("read layer: %v", err)
	}
	if string(data) != "disk:"+version {
		t.Fatalf("layer = %q, want the box disk snapshot", data)
	}
}

// An unclean stop (the guest ignored the shutdown request and was force
// stopped) publishes nothing.
func TestPauseDoesNotPublishOnUncleanStop(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, "")
	key := layerKey(t, worktree, version, envcache.Trusted)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { return nil } // guest never shuts down
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(key); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir after unclean stop = %v, want ErrMiss", err)
	}
	if len(rec.scrubbed) != 0 {
		t.Fatalf("scrubbed = %v, want no publish after an unclean stop", rec.scrubbed)
	}
}

// A provision that never finished publishes nothing: the layer must represent
// a complete setup.
func TestPauseDoesNotPublishWhenProvisionNotDone(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, "")
	key := layerKey(t, worktree, version, envcache.Trusted)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseRunning}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(key); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir after unfinished provision = %v, want ErrMiss", err)
	}
	if len(rec.scrubbed) != 0 {
		t.Fatalf("scrubbed = %v, want no publish from an unfinished provision", rec.scrubbed)
	}
}

// An untrusted pull-request box never publishes, even on a successful, clean
// provision, and never consumes a trusted layer.
func TestUntrustedBoxNeverPublishesOrConsumesTrusted(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, state.TrustClassUntrusted)

	trusted := layerKey(t, worktree, version, envcache.Trusted)
	publishLayer(t, h, trusted, "trusted-secret-layer")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if rec.baseClones() != 1 || len(rec.prepared) != 0 {
		t.Fatalf("base=%d prepared=%v, want the untrusted box to miss the trusted layer and provision fresh", rec.baseClones(), rec.prepared)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(layerKey(t, worktree, version, envcache.Untrusted)); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("untrusted box published a layer: %v", err)
	}
}

// A trusted box that opts into the secret-free scope publishes to the
// untrusted class, which an untrusted box can then consume.
func TestSharedSecretFreeLayerIsPublishedAndConsumed(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\nshare_untrusted = true\n")
	sharer := h.layerBox(t, worktree, state.TrustClassTrusted)

	if _, err := h.r.Up(context.Background(), sharer); err != nil {
		t.Fatalf("Up sharer: %v", err)
	}
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(sharer.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, sharer.ID)); err != nil {
		t.Fatalf("Pause sharer: %v", err)
	}
	shared := layerKey(t, worktree, version, envcache.Untrusted)
	sharedDir, err := h.r.EnvironmentCache.LayerDir(shared)
	if err != nil {
		t.Fatalf("shared layer not published: %v", err)
	}
	// The shared layer must have been scrubbed before it became visible.
	if len(rec.scrubbed) != 1 {
		t.Fatalf("scrubbed = %v, want the shared layer scrubbed once before publishing", rec.scrubbed)
	}
	sharedBytes, err := os.ReadFile(filepath.Join(sharedDir, "rootfs.img"))
	if err != nil {
		t.Fatalf("read shared layer: %v", err)
	}
	if string(sharedBytes) != "disk:"+version {
		t.Fatalf("shared layer = %q, want the box's scrubbed disk snapshot", sharedBytes)
	}

	consumer := h.layerBox(t, t.TempDir(), state.TrustClassUntrusted)
	writeCacheContract(t, consumer.Worktree, "[provision]\ncommand = \"make setup\"\ncache = true\nshare_untrusted = true\n")
	rec.prepared = nil
	if _, err := h.r.Up(context.Background(), consumer); err != nil {
		t.Fatalf("Up consumer: %v", err)
	}
	if len(rec.prepared) != 1 {
		t.Fatalf("prepared = %v, want the untrusted box to consume the shared layer", rec.prepared)
	}
}

// A trusted layer is not exposed to a box resolving a different class.
func TestTrustedLayerIsNotConsumedByUntrustedKey(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, state.TrustClassTrusted)
	publishLayer(t, h, layerKey(t, worktree, version, envcache.Untrusted), "untrusted-layer")

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(rec.prepared) != 0 || rec.baseClones() != 1 {
		t.Fatalf("prepared=%v base=%d, want the trusted box to ignore the untrusted layer", rec.prepared, rec.baseClones())
	}
}

// Concurrent boxes requesting the same missing layer coordinate one build: the
// second is told a build is in flight, and after the first publishes, it reuses
// the layer instead of provisioning again.
func TestConcurrentMissCoordinatesOneBuild(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	for _, dir := range []string{firstDir, secondDir} {
		writeCacheContract(t, dir, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	}
	first := h.layerBox(t, firstDir, "")
	second := h.layerBox(t, secondDir, "")

	if _, err := h.r.Up(context.Background(), first); err != nil {
		t.Fatalf("Up first: %v", err)
	}
	if _, err := h.r.Up(context.Background(), second); !errors.Is(err, envcache.ErrBuilding) {
		t.Fatalf("Up second error = %v, want ErrBuilding", err)
	}
	if rec.baseClones() != 1 {
		t.Fatalf("base clones = %d, want exactly one coordinated build", rec.baseClones())
	}

	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(first.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, first.ID)); err != nil {
		t.Fatalf("Pause first: %v", err)
	}
	key := layerKey(t, firstDir, version, envcache.Trusted)
	if _, err := h.r.EnvironmentCache.LayerDir(key); err != nil {
		t.Fatalf("first box did not publish: %v", err)
	}

	if _, err := h.r.Up(context.Background(), second); err != nil {
		t.Fatalf("Up second after publish: %v", err)
	}
	if len(rec.prepared) != 1 {
		t.Fatalf("prepared = %v, want the second box to reuse the layer", rec.prepared)
	}
	if rec.baseClones() != 1 {
		t.Fatalf("base clones = %d, want no duplicate provision", rec.baseClones())
	}
}

// A failed start frees the build claim so a later box can retry.
func TestFailedStartReleasesLayerBuild(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	first := h.layerBox(t, worktree, "")
	h.r.WaitReady = func(context.Context, string) error { return context.DeadlineExceeded }
	if _, err := h.r.Up(context.Background(), first); err == nil {
		t.Fatal("Up should fail when the box never becomes ready")
	}
	h.r.WaitReady = func(context.Context, string) error { return nil }

	// The abandoned claim must not block a second box for the same key.
	second := h.layerBox(t, t.TempDir(), "")
	writeCacheContract(t, second.Worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	if _, err := h.r.Up(context.Background(), second); err != nil {
		t.Fatalf("Up after a failed start = %v, want the claim released", err)
	}
	if rec.baseClones() != 2 {
		t.Fatalf("base clones = %d, want the retry to build fresh", rec.baseClones())
	}
}

// The layer identity is frozen when the disk is created. Editing the worktree
// contract afterwards must not move the publish key or leak the old claim.
func TestLayerKeyIsFrozenAtDiskCreation(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	const original = "[provision]\ncommand = \"make setup\"\ncache = true\n"
	writeCacheContract(t, worktree, original)
	box := h.layerBox(t, worktree, "")
	keyAtCreate := layerKey(t, worktree, version, envcache.Trusted)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// The contract changes after the disk exists: the setup key changes with it.
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make different setup\"\ncache = true\n")
	changedKey := layerKey(t, worktree, version, envcache.Trusted)
	if changedKey == keyAtCreate {
		t.Fatal("test setup: editing the contract must change the key")
	}

	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseDone}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(keyAtCreate); err != nil {
		t.Fatalf("layer not published under the frozen key: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(changedKey); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir(changed key) = %v, want ErrMiss (no publish under the edited contract)", err)
	}

	// The claim held on the frozen key was released: a peer with the original
	// setup can consume the published layer instead of waiting on a ghost.
	consumer := h.layerBox(t, t.TempDir(), "")
	writeCacheContract(t, consumer.Worktree, original)
	rec.prepared = nil
	if _, err := h.r.Up(context.Background(), consumer); err != nil {
		t.Fatalf("Up consumer = %v, want the frozen claim released", err)
	}
	if len(rec.prepared) != 1 {
		t.Fatalf("prepared = %v, want the consumer to reuse the published layer", rec.prepared)
	}
}

// A clean stop before provision finished is recorded, so a later pause that
// finds the unit already inactive publishes once provision is done.
func TestPausePublishesAfterProvisionCompletesOnInactiveBox(t *testing.T) {
	h := newHarness(t)
	version := h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	worktree := t.TempDir()
	writeCacheContract(t, worktree, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	box := h.layerBox(t, worktree, "")
	key := layerKey(t, worktree, version, envcache.Trusted)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	// First pause: provision still running, stop is clean. No publish, but the
	// clean stop is recorded.
	h.agent.status.Provision = state.PhaseStatus{State: state.PhaseRunning}
	h.r.CtrlAltDel = func(string) error { h.sys.set(unitName(box.ID), "inactive"); return nil }
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause first: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(key); !errors.Is(err, envcache.ErrMiss) {
		t.Fatalf("LayerDir after unfinished provision = %v, want ErrMiss", err)
	}
	// Provision completes while the box is paused; a second pause finds the
	// unit already inactive and must still publish.
	if _, err := h.st.SetPhases(box.ID, state.Phases{Provision: state.PhaseStatus{State: state.PhaseDone}}); err != nil {
		t.Fatalf("SetPhases: %v", err)
	}
	if _, err := h.r.Pause(mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("Pause second: %v", err)
	}
	if _, err := h.r.EnvironmentCache.LayerDir(key); err != nil {
		t.Fatalf("layer not published after provision completed on an inactive box: %v", err)
	}
	if len(rec.scrubbed) != 1 {
		t.Fatalf("scrubbed = %v, want the layer scrubbed once", rec.scrubbed)
	}
}

// A build claim is not held forever: once its lease expires a peer may take it
// over, so a builder that never pauses cannot starve the cache.
func TestStaleLayerClaimIsTakenOver(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	rec := h.withLayerHarness(t)
	firstDir, secondDir := t.TempDir(), t.TempDir()
	for _, dir := range []string{firstDir, secondDir} {
		writeCacheContract(t, dir, "[provision]\ncommand = \"make setup\"\ncache = true\n")
	}
	first := h.layerBox(t, firstDir, "")
	second := h.layerBox(t, secondDir, "")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	h.r.Now = func() time.Time { return now }
	h.r.LayerClaimLease = time.Hour

	if _, err := h.r.Up(context.Background(), first); err != nil {
		t.Fatalf("Up first: %v", err)
	}
	if _, err := h.r.Up(context.Background(), second); !errors.Is(err, envcache.ErrBuilding) {
		t.Fatalf("Up second error = %v, want ErrBuilding before the lease expires", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := h.r.Up(context.Background(), second); err != nil {
		t.Fatalf("Up second after lease = %v, want a takeover", err)
	}
	if rec.baseClones() != 2 {
		t.Fatalf("base clones = %d, want the takeover to build a second time", rec.baseClones())
	}
}
