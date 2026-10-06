// Package runner boots and manages boxes on a host: it prepares disks from
// image artifacts, renders per-box systemd units, drives systemd, and
// performs the lifecycle operations behind the daemon's API. VMM processes
// are never daemon children — each box runs as its own pluto-box@<uuid>.service
// (ADR 0002).
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/agent"
	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/fsutil"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/systemd"
	"github.com/Siddhj2206/pluto/internal/vsock"
)

const (
	guestUser = "dev"
	guestPort = 22

	tapFc    = "tap-fc"
	tapSlirp = "tap-slirp"
	bridge   = "br0"
)

// Systemctl is the slice of systemd the runner drives.
type Systemctl interface {
	IsActive(unit string) (string, error)
	Start(unit string) error
	Stop(unit string) error
	ResetFailed(unit string) error
	DaemonReload() error
}

// Runner owns the box lifecycle on one host.
type Runner struct {
	Store   *state.Store
	Root    string
	Exe     string
	Sys     Systemctl
	UnitDir string

	ReadyTimeout     time.Duration
	CleanStopTimeout time.Duration
	ForceStopTimeout time.Duration
	AgentTimeout     time.Duration

	// OS seams, replaceable in tests.
	// PrepareDisk creates a box's rootfs from the base image, growing it to
	// diskMiB when diskMiB is positive; zero keeps the base image's size.
	PrepareDisk func(boxDir, imageDir string, diskMiB int) error
	// PrepareLayerDisk creates a box's rootfs from a published environment
	// layer instead of the base image, and marks the guest agent's provision
	// complete so a cache hit never re-runs the full setup.
	PrepareLayerDisk func(boxDir, layerDir string, diskMiB int) error
	// ScrubEnvironment removes per-box worktree, agent, and identity state from
	// an offline rootfs copy before it is published as a reusable layer.
	ScrubEnvironment func(image string) error
	// EnvironmentCache is the host-local store of published layers. Zero Root
	// disables caching.
	EnvironmentCache envcache.Cache
	WaitReady        func(ctx context.Context, uds string) error
	CtrlAltDel       func(socketPath string) error
	NewAgent         func(vsockUDS string) AgentClient
	MakeBundle       func(ctx context.Context, worktree, out string) error
	WorktreeRemotes  func(worktree string) ([]state.Remote, error)

	mu sync.Mutex
	// layerMu guards layerBuilds, the in-flight environment-layer builds keyed
	// by cache key, so concurrent boxes requesting the same missing layer
	// coordinate one build instead of duplicating provisioning.
	layerMu     sync.Mutex
	layerBuilds map[string]string
}

// New builds a runner for a state directory. exe is the pluto binary that
// per-box units execute.
func New(store *state.Store, exe string) *Runner {
	unitDir := ""
	if path, err := systemd.BoxUnitPath(); err == nil {
		unitDir = filepath.Dir(path)
	}
	return &Runner{
		Store:            store,
		Root:             store.Root(),
		Exe:              exe,
		Sys:              ExecSystemctl{},
		UnitDir:          unitDir,
		ReadyTimeout:     60 * time.Second,
		CleanStopTimeout: 30 * time.Second,
		ForceStopTimeout: 10 * time.Second,
		AgentTimeout:     20 * time.Second,
		PrepareDisk:      prepareDisk,
		PrepareLayerDisk: prepareLayerDisk,
		ScrubEnvironment: scrubEnvironment,
		EnvironmentCache: envcache.Cache{Root: filepath.Join(store.Root(), "environment")},
		WaitReady:        defaultWaitReady,
		CtrlAltDel:       sendCtrlAltDel,
		NewAgent:         func(vsockUDS string) AgentClient { return agent.NewClient(vsockUDS) },
		MakeBundle:       makeBundle,
		WorktreeRemotes:  worktreeRemotes,
		layerBuilds:      make(map[string]string),
	}
}

// Up ensures the box is running: it pins an image if the box has none,
// prepares the disk, starts the unit, and waits for sshd over vsock.
func (r *Runner) Up(ctx context.Context, box *state.Box) (*state.Box, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	box, err := r.Store.Box(box.ID)
	if err != nil {
		return nil, err
	}
	if err := validateSocketPaths(r.boxDir(box.ID)); err != nil {
		return nil, err
	}
	unit := unitName(box.ID)
	started := false
	if st, err := r.Sys.IsActive(unit); err != nil || !isLive(st) {
		if err := r.startLocked(box, unit); err != nil {
			// A coordinated layer build that is still running must not leave a
			// stale claim behind, and a failed start must free its claim so a
			// later box can retry.
			r.releaseBoxLayerBuild(box.ID)
			return nil, err
		}
		started = true
	}
	// Refresh the record the start may have changed: pinning an image is the
	// one mutation a boot performs, and the layer key depends on it.
	if box, err = r.Store.Box(box.ID); err != nil {
		return nil, err
	}

	boxDir := r.boxDir(box.ID)
	waitCtx, cancel := context.WithTimeout(ctx, r.ReadyTimeout)
	defer cancel()
	// Fail fast when the unit dies instead of waiting out the timeout.
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-waitCtx.Done():
				return
			case <-ticker.C:
				if st, err := r.Sys.IsActive(unit); err == nil && !isLive(st) {
					cancel()
					return
				}
			}
		}
	}()
	if err := r.WaitReady(waitCtx, vsockPath(boxDir)); err != nil {
		r.releaseBoxLayerBuild(box.ID)
		_, _ = r.Store.Transition(box.ID, state.StateFailed)
		return nil, fmt.Errorf("box %s did not become ready: %w (see %s)", shortID(box.ID), err, filepath.Join(boxDir, "serial.log"))
	}
	box, err = r.Store.Transition(box.ID, state.StateRunning)
	if err != nil {
		return nil, err
	}
	// Wake runs on every start, not on every attach. Hand off when this call
	// started the machine, when no contract was ever applied, or when the
	// guest has rebooted since the last handoff (a new boot id).
	needHandoff := started || box.Phases == nil || box.Phases.BootID == ""
	if !needHandoff {
		if status, err := r.NewAgent(vsockPath(boxDir)).Status(); err == nil {
			if status.BootID != box.Phases.BootID {
				needHandoff = true
			} else {
				box, _ = r.Store.SetPhases(box.ID, status)
			}
		}
	}
	if needHandoff {
		if err := r.handoff(ctx, box, boxDir); err != nil {
			r.releaseBoxLayerBuild(box.ID)
			return nil, fmt.Errorf("box %s is running, but the agent handoff failed: %w", shortID(box.ID), err)
		}
		return r.Store.Box(box.ID)
	}
	return box, nil
}

// startLocked pins the image if needed and starts the box's unit; the caller
// holds the runner mutex.
func (r *Runner) startLocked(box *state.Box, unit string) error {
	version := box.Image
	if version == "" {
		var err error
		version, err = r.CurrentImage()
		if err != nil {
			return err
		}
		if box, err = r.Store.SetImage(box.ID, version); err != nil {
			return err
		}
	}
	imageDir := r.imageDir(version)
	if err := checkImage(imageDir); err != nil {
		return err
	}
	// Freeze the machine size before the disk is created and the first config
	// is written: a box created before resources were recorded picks them up
	// here, once. The disk is sized from the same frozen record, so editing
	// the contract cannot resize an existing disk.
	var err error
	if box, err = r.ensureResources(box); err != nil {
		return err
	}
	boxDir := r.boxDir(box.ID)
	if err := r.prepareBoxDisk(box, imageDir, version); err != nil {
		return err
	}
	if err := clearSockets(boxDir); err != nil {
		return err
	}
	machine := machineSize(box.Resources)
	if err := writeConfig(boxDir, imageDir, box.ID, machine); err != nil {
		return err
	}
	changedCgroup, err := writeCgroupDropIn(r.UnitDir, box.ID, machine)
	if err != nil {
		return err
	}
	if err := r.ensureUnit(); err != nil {
		return err
	}
	if changedCgroup {
		if err := r.Sys.DaemonReload(); err != nil {
			return fmt.Errorf("reload systemd after cgroup limits for %s: %w", unit, err)
		}
	}
	_ = r.Sys.ResetFailed(unit)
	if err := r.Sys.Start(unit); err != nil {
		_, _ = r.Store.Transition(box.ID, state.StateFailed)
		return fmt.Errorf("start %s: %w", unit, err)
	}
	return nil
}

// ensureResources freezes a box's resources at its first start: it reads
// [box].resources from the worktree, records the machine size and the rootfs
// size, and leaves them alone on every later start. That is what makes
// resources recreate-only — editing the contract and waking the box cannot
// resize the running machine or its disk. A record written before the field
// existed is upgraded in place on its next start. A malformed contract is not
// fatal here; the handoff reports it, and the box boots at the defaults.
func (r *Runner) ensureResources(box *state.Box) (*state.Box, error) {
	if box.Resources != nil {
		return box, nil
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		ct = &contract.Contract{}
	}
	memMiB, _ := contract.ParseMemoryMiB(ct.Box.Resources.Memory) // Load validated it
	diskMiB, _ := contract.ParseDiskMiB(ct.Box.Resources.Disk)    // Load validated it
	return r.Store.SetResources(box.ID, &state.Resources{
		CPUs:      ct.Box.Resources.CPUs,
		MemoryMiB: memMiB,
		DiskMiB:   diskMiB,
	})
}

// diskSizeMiB resolves a box's recorded rootfs size; zero (unset, or a record
// that predates the field) keeps the base image's size.
func diskSizeMiB(res *state.Resources) int {
	if res == nil {
		return 0
	}
	return res.DiskMiB
}

// Pause stops the machine cleanly: the guest is asked to shut down through
// Firecracker's SendCtrlAltDel, and a bounded force stop lands if it does
// not. It refuses to report a box paused while its unit is still running.
// When the box opted into layer reuse and provisioned successfully, a clean
// stop publishes its scrubbed disk as a reusable environment layer.
func (r *Runner) Pause(box *state.Box) (*state.Box, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	box, err := r.Store.Box(box.ID)
	if err != nil {
		return nil, err
	}
	if box.State == state.StateCreated {
		return nil, fmt.Errorf("box %s has not been started", shortID(box.ID))
	}
	unit := unitName(box.ID)
	st, err := r.Sys.IsActive(unit)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", unit, err)
	}
	boxDir := r.boxDir(box.ID)
	ct, _ := contract.Load(box.Worktree)
	layerKey, publishable, cached := r.environmentLayerKey(box, box.Image, ct)
	// Provision is asynchronous in the guest: the only way to know it finished
	// is to ask the live agent. Read it before the shutdown request.
	provisionDone := false
	if cached {
		if status, statusErr := r.NewAgent(vsockPath(boxDir)).Status(); statusErr == nil {
			provisionDone = status.Provision.State == state.PhaseDone
		}
	}
	cleanShutdown := false
	if isLive(st) {
		if err := r.CtrlAltDel(apiSockPath(boxDir)); err != nil {
			// The guest may already be gone; the force stop below still lands.
		}
		if r.waitInactive(unit, r.CleanStopTimeout) {
			cleanShutdown = true
		} else {
			_ = r.Sys.Stop(unit)
			if !r.waitInactive(unit, r.ForceStopTimeout) {
				return nil, fmt.Errorf("box %s did not stop; it is still running", shortID(box.ID))
			}
		}
	}
	_ = r.Sys.ResetFailed(unit)
	r.failRunningJob(box.ID, "box paused")
	box, err = r.Store.Transition(box.ID, state.StatePaused)
	if err != nil {
		return nil, err
	}
	// The machine is gone, so its sessions are stopped; keep their names so
	// status still lists them. Services (active/inactive) carry the same
	// staleness, but reporting them is out of this fix's scope.
	box, err = r.Store.StopSessions(box.ID)
	if err != nil {
		return nil, err
	}
	if cached {
		// Publish only a completed, cleanly stopped provision. Anything else
		// leaves no layer and frees the build claim for a later retry.
		if publishable && cleanShutdown && provisionDone {
			r.publishEnvironmentLayer(box.ID, layerKey)
		}
		r.releaseLayerBuild(layerKey, box.ID)
	}
	return box, nil
}

// Attach ensures the box is running and returns the ssh connection details.
func (r *Runner) Attach(ctx context.Context, box *state.Box) (api.AttachInfo, error) {
	box, err := r.Up(ctx, box)
	if err != nil {
		return api.AttachInfo{}, err
	}
	boxDir := r.boxDir(box.ID)
	return api.AttachInfo{
		User: guestUser,
		UDS:  vsockPath(boxDir),
		Key:  keyPath(boxDir),
		Port: guestPort,
	}, nil
}

// Destroy stops the box's unit and removes its record and disk. It works
// from the id alone so corrupt records have a repair path, and it refuses to
// delete a disk whose unit is still running.
func (r *Runner) Destroy(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !state.ValidID(id) {
		return fmt.Errorf("invalid box id %q", id)
	}
	unit := unitName(id)
	_ = r.Sys.Stop(unit) // a no-op for inactive units
	st, err := r.Sys.IsActive(unit)
	if err != nil {
		return fmt.Errorf("check %s: %w", unit, err)
	}
	if isLive(st) {
		return fmt.Errorf("box %s did not stop; refusing to remove its disk", shortID(id))
	}
	_ = r.Sys.ResetFailed(unit)
	if err := r.removeCgroupDropIn(id); err != nil {
		return err
	}
	// The box can no longer publish its build; free the claim so another box
	// may retry instead of waiting on a stopped builder.
	r.releaseBoxLayerBuild(id)
	return r.Store.DestroyBox(id)
}

// Reconcile aligns a box record with the unit's real state: a running record
// with no unit becomes paused (host reboot), an active unit becomes running,
// and a failed unit becomes failed.
func (r *Runner) Reconcile(box *state.Box) (*state.Box, error) {
	st, err := r.Sys.IsActive(unitName(box.ID))
	if err != nil {
		return box, nil
	}
	switch {
	case isLive(st):
		if box.State != state.StateRunning {
			return r.Store.Transition(box.ID, state.StateRunning)
		}
	case st == "failed":
		if box.State == state.StateRunning {
			return r.Store.Transition(box.ID, state.StateFailed)
		}
	default:
		if box.State == state.StateRunning {
			return r.Store.Transition(box.ID, state.StatePaused)
		}
	}
	return box, nil
}

// ReconcileAll aligns every record with its unit. It is best effort and is
// called when the daemon starts. A job recorded as running on a box whose
// unit is down died with the machine and is marked failed; a live box keeps
// its running job for Refresh to resolve against the agent.
func (r *Runner) ReconcileAll() {
	boxes, _, err := r.Store.Boxes()
	if err != nil {
		return
	}
	for _, box := range boxes {
		reconciled, err := r.Reconcile(box)
		if err != nil {
			continue
		}
		if !reconciled.JobRunning() {
			continue
		}
		if st, err := r.Sys.IsActive(unitName(reconciled.ID)); err == nil && !isLive(st) {
			r.failRunningJob(reconciled.ID, "box is not running")
		}
	}
}

// Import copies a built artifact into the image store and returns its
// content-derived version. Hashes are verified against the manifest before
// anything is installed, so a tampered artifact is refused.
func (r *Runner) Import(srcDir string) (string, error) {
	srcDir = filepath.Clean(srcDir)
	manifest, err := readManifest(filepath.Join(srcDir, "manifest.json"))
	if err != nil {
		return "", err
	}
	fcSrc, err := findFirecracker(srcDir)
	if err != nil {
		return "", err
	}
	for _, f := range []struct{ path, want, label string }{
		{filepath.Join(srcDir, "rootfs.img"), manifest.Rootfs.SHA256, "rootfs"},
		{filepath.Join(srcDir, "vmlinuz"), manifest.Kernel.SHA256, "kernel"},
		{fcSrc, manifest.Firecracker.SHA256, "firecracker"},
	} {
		got, err := hashFile(f.path)
		if err != nil {
			return "", err
		}
		if got != f.want {
			return "", fmt.Errorf("%s %s does not match its manifest (got %s, want %s)", f.label, f.path, got, f.want)
		}
	}
	version := manifest.version()
	dst := r.imageDir(version)
	if imageComplete(dst) {
		return version, nil
	}
	// A partial import from an earlier failure is replaced.
	os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("create image dir: %w", err)
	}
	for _, c := range []struct{ src, name string }{
		{filepath.Join(srcDir, "vmlinuz"), "vmlinuz"},
		{filepath.Join(srcDir, "rootfs.img"), "rootfs.img"},
		{fcSrc, "firecracker"},
		{filepath.Join(srcDir, "manifest.json"), "manifest.json"},
	} {
		if err := fsutil.CloneFile(c.src, filepath.Join(dst, c.name)); err != nil {
			os.RemoveAll(dst)
			return "", fmt.Errorf("install %s: %w", c.name, err)
		}
	}
	return version, nil
}

// CurrentImage returns the most recently imported image version.
func (r *Runner) CurrentImage() (string, error) {
	entries, err := os.ReadDir(r.imageRoot())
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoImage()
	}
	if err != nil {
		return "", err
	}
	var newest string
	var newestMod time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = e.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return "", errNoImage()
	}
	return newest, nil
}

// Images lists the imported image versions.
func (r *Runner) Images() ([]api.ImageInfo, error) {
	entries, err := os.ReadDir(r.imageRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []api.ImageInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest, err := readManifest(filepath.Join(r.imageRoot(), e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		out = append(out, api.ImageInfo{
			Version:         e.Name(),
			SourceDateEpoch: manifest.SourceDateEpoch,
			KernelSHA256:    manifest.Kernel.SHA256,
			RootfsSHA256:    manifest.Rootfs.SHA256,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func errNoImage() error {
	return errors.New("no image imported: run 'pluto image import <artifact-dir>' first")
}

func (r *Runner) waitInactive(unit string, timeout time.Duration) bool {
	poll := 250 * time.Millisecond
	if timeout < time.Second {
		poll = timeout / 4
	}
	if poll < 5*time.Millisecond {
		poll = 5 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		if st, err := r.Sys.IsActive(unit); err == nil && !isLive(st) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(poll)
	}
}

func (r *Runner) ensureUnit() error {
	if r.UnitDir == "" {
		return errors.New("cannot locate the systemd user unit directory")
	}
	path := filepath.Join(r.UnitDir, "pluto-box@.service")
	want := systemd.BoxUnit(r.Exe, r.Root)
	if current, err := os.ReadFile(path); err == nil && string(current) == want {
		return nil
	}
	if err := os.MkdirAll(r.UnitDir, 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return fmt.Errorf("write box unit: %w", err)
	}
	return r.Sys.DaemonReload()
}

func (r *Runner) imageRoot() string { return filepath.Join(r.Root, "images") }
func (r *Runner) imageDir(version string) string {
	return filepath.Join(r.imageRoot(), version)
}
func (r *Runner) boxDir(id string) string { return filepath.Join(r.Root, "boxes", id) }

func unitName(id string) string      { return "pluto-box@" + id + ".service" }
func vsockPath(boxDir string) string { return filepath.Join(boxDir, "v.sock") }
func apiSockPath(boxDir string) string {
	return filepath.Join(boxDir, "firecracker.sock")
}

// Linux sockaddr_un.sun_path holds at most 108 bytes including its trailing
// NUL, so pathname sockets must be shorter than 108 bytes.
const maxUnixSocketPathBytes = 107

func validateSocketPaths(boxDir string) error {
	for _, path := range []string{apiSockPath(boxDir), vsockPath(boxDir)} {
		if len(path) > maxUnixSocketPathBytes {
			return fmt.Errorf("state-dir produces a socket path too long for Firecracker (%d bytes; maximum %d): use a shorter --state-dir", len(path), maxUnixSocketPathBytes)
		}
	}
	return nil
}
func keyPath(boxDir string) string { return filepath.Join(boxDir, "id") }

func isLive(state string) bool {
	return state == "active" || state == "activating" || state == "deactivating"
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func checkImage(dir string) error {
	for _, name := range []string{"vmlinuz", "rootfs.img", "firecracker"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("image %s is incomplete: %w", filepath.Base(dir), err)
		}
	}
	return nil
}

// imageComplete reports whether an imported image has all of its files.
func imageComplete(dir string) bool {
	for _, name := range []string{"vmlinuz", "rootfs.img", "firecracker", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// clearSockets removes the vsock and API sockets a previous VMM left behind:
// Firecracker refuses to bind a path that already exists. It also drops the
// previous run's metrics sink, which Firecracker opens without truncating, so a
// shorter run cannot leave a stale snapshot as the file's last line.
func clearSockets(boxDir string) error {
	for _, name := range []string{"firecracker.sock", "v.sock", "metrics.json"} {
		path := filepath.Join(boxDir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	return nil
}

type imageManifest struct {
	SourceDateEpoch int64 `json:"source_date_epoch"`
	Kernel          struct {
		SHA256 string `json:"sha256"`
	} `json:"kernel"`
	Firecracker struct {
		SHA256 string `json:"sha256"`
	} `json:"firecracker"`
	Rootfs struct {
		SHA256 string `json:"sha256"`
	} `json:"rootfs"`
}

// version is the image's content address: the kernel and rootfs hashes.
func (m imageManifest) version() string {
	sum := sha256.Sum256([]byte(m.Kernel.SHA256 + "\n" + m.Rootfs.SHA256))
	return hex.EncodeToString(sum[:])[:16]
}

func readManifest(path string) (imageManifest, error) {
	var m imageManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, fmt.Errorf("read image manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parse image manifest %s: %w", path, err)
	}
	if m.Kernel.SHA256 == "" || m.Rootfs.SHA256 == "" {
		return m, fmt.Errorf("image manifest %s is missing hashes", path)
	}
	return m, nil
}

func findFirecracker(srcDir string) (string, error) {
	for _, candidate := range []string{
		filepath.Join(srcDir, "firecracker"),
		filepath.Join(srcDir, "cache", "firecracker"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no firecracker binary under %s", srcDir)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func guestCID(id string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(id))
	return 3 + h.Sum32()%(1<<31-4)
}

type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
	CacheType    string `json:"cache_type"`
}

type fcMachineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMiB int `json:"mem_size_mib"`
}

type fcVsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type fcNetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
	GuestMAC    string `json:"guest_mac"`
}

type fcLogger struct {
	LogPath string `json:"log_path"`
	Level   string `json:"level"`
}

type fcMetrics struct {
	MetricsPath string `json:"metrics_path"`
}

// fcConfig is the pre-boot Firecracker config. It uses exactly Firecracker's
// minimal device set (virtio-net, virtio-block, virtio-vsock, serial, i8042);
// no devices are added or removed. Seccomp is left at Firecracker's default
// (most restrictive) filters: pluto passes neither --no-seccomp nor a custom
// filter, so the VMM's per-thread filters stay on.
type fcConfig struct {
	BootSource        fcBootSource         `json:"boot-source"`
	Drives            []fcDrive            `json:"drives"`
	MachineConfig     fcMachineConfig      `json:"machine-config"`
	Vsock             fcVsock              `json:"vsock"`
	NetworkInterfaces []fcNetworkInterface `json:"network-interfaces"`
	Logger            fcLogger             `json:"logger"`
	Metrics           fcMetrics            `json:"metrics"`
}

// bootArgs is pluto's kernel command line: the explicit serial console and root
// device it needs, plus Firecracker's default hardening and boot flags
// (nomodule and the i8042/swiotlb settings). 8250.nr_uarts=0 is deliberately
// omitted: it would disable the serial console pluto's boot log depends on
// (docs/research/firecracker-operation.md §4 A1).
const bootArgs = "console=ttyS0 root=/dev/vda rw reboot=k panic=1 " +
	"nomodule i8042.noaux i8042.nomux i8042.dumbkbd swiotlb=noforce"

func writeConfig(boxDir, imageDir, id string, machine systemd.BoxResources) error {
	var cfg fcConfig
	cfg.BootSource = fcBootSource{
		KernelImagePath: filepath.Join(imageDir, "vmlinuz"),
		BootArgs:        bootArgs,
	}
	cfg.Drives = []fcDrive{{
		DriveID:      "rootfs",
		PathOnHost:   filepath.Join(boxDir, "disk", "rootfs.img"),
		IsRootDevice: true,
		IsReadOnly:   false,
		CacheType:    "Writeback",
	}}
	cpus, memMiB := machine.CPUs, machine.MemoryMiB
	cfg.MachineConfig = fcMachineConfig{VCPUCount: cpus, MemSizeMiB: memMiB}
	cfg.Vsock = fcVsock{GuestCID: guestCID(id), UDSPath: vsockPath(boxDir)}
	cfg.NetworkInterfaces = []fcNetworkInterface{{
		IfaceID:     "eth0",
		HostDevName: tapFc,
		GuestMAC:    "06:00:AC:10:00:0F",
	}}
	// Structured logs go through a named pipe the runner drains into a bounded
	// file, so fc.log cannot grow without limit. Metrics flush to a per-box
	// file the daemon reads.
	cfg.Logger = fcLogger{LogPath: fcLogPipe(boxDir), Level: "Warning"}
	cfg.Metrics = fcMetrics{MetricsPath: metricsPath(boxDir)}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode firecracker config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(boxDir, "fc.json"), data, 0o644); err != nil {
		return fmt.Errorf("write firecracker config: %w", err)
	}
	return nil
}

// machineSize resolves a box's recorded resources to a concrete machine size.
// A nil record, or a zero field, falls back to the defaults (2 vCPU /
// 1024 MiB); a partial declaration fills only what it names.
func machineSize(res *state.Resources) systemd.BoxResources {
	machine := systemd.BoxResources{CPUs: contract.DefaultCPUs, MemoryMiB: contract.DefaultMemoryMiB}
	if res == nil {
		return machine
	}
	if res.CPUs > 0 {
		machine.CPUs = res.CPUs
	}
	if res.MemoryMiB > 0 {
		machine.MemoryMiB = res.MemoryMiB
	}
	return machine
}

// writeCgroupDropIn writes a per-instance systemd drop-in that caps the box's
// service cgroup. systemd already owns that cgroup (each box runs as its own
// pluto-box@<id>.service under the user manager), so a drop-in stays rootless:
// no direct cgroup v2 writes. MemoryMax is the kernel memory cap and CPUQuota
// is CPU bandwidth, a percentage of one CPU, so N vCPUs is N*100%. It reports
// whether the file changed, so the caller only reloads systemd when needed.
func writeCgroupDropIn(unitDir, id string, machine systemd.BoxResources) (bool, error) {
	if unitDir == "" {
		return false, errors.New("cannot locate the systemd user unit directory")
	}
	text := systemd.BoxResourcesDropIn(machine)
	path := cgroupDropInPath(unitDir, id)
	if current, err := os.ReadFile(path); err == nil && string(current) == text {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create box cgroup dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write box cgroup limits: %w", err)
	}
	return true, nil
}

// removeCgroupDropIn removes a destroyed box's per-instance caps, so a later
// box that reuses the directory does not inherit the old size.
func (r *Runner) removeCgroupDropIn(id string) error {
	if r.UnitDir == "" {
		return nil
	}
	dir := filepath.Dir(cgroupDropInPath(r.UnitDir, id))
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove box cgroup limits: %w", err)
	}
	return nil
}

// cgroupDropInPath is where systemd reads a box instance's drop-ins.
func cgroupDropInPath(unitDir, id string) string {
	return filepath.Join(unitDir, "pluto-box@"+id+".service.d", "resources.conf")
}

// defaultWaitReady waits for the box's sshd banner over vsock.
func defaultWaitReady(ctx context.Context, uds string) error {
	_, err := vsock.WaitReady(ctx, uds, guestPort)
	return err
}
