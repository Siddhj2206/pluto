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
	PrepareDisk    func(boxDir, imageDir string) error
	WaitReady      func(ctx context.Context, uds string) error
	CtrlAltDel     func(socketPath string) error
	NewAgent       func(vsockUDS string) AgentClient
	MakeBundle     func(ctx context.Context, worktree, out string) error
	WorktreeRemote func(worktree string) (string, error)

	mu sync.Mutex
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
		WaitReady:        defaultWaitReady,
		CtrlAltDel:       sendCtrlAltDel,
		NewAgent:         func(vsockUDS string) AgentClient { return agent.NewClient(vsockUDS) },
		MakeBundle:       makeBundle,
		WorktreeRemote:   worktreeRemote,
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
	unit := unitName(box.ID)
	started := false
	if st, err := r.Sys.IsActive(unit); err != nil || !isLive(st) {
		if err := r.startLocked(box, unit); err != nil {
			return nil, err
		}
		started = true
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
	boxDir := r.boxDir(box.ID)
	if err := r.PrepareDisk(boxDir, imageDir); err != nil {
		return err
	}
	if err := clearSockets(boxDir); err != nil {
		return err
	}
	if err := writeConfig(boxDir, imageDir, box.ID); err != nil {
		return err
	}
	if err := r.ensureUnit(); err != nil {
		return err
	}
	_ = r.Sys.ResetFailed(unit)
	if err := r.Sys.Start(unit); err != nil {
		_, _ = r.Store.Transition(box.ID, state.StateFailed)
		return fmt.Errorf("start %s: %w", unit, err)
	}
	return nil
}

// Pause stops the machine cleanly: the guest is asked to shut down through
// Firecracker's SendCtrlAltDel, and a bounded force stop lands if it does
// not. It refuses to report a box paused while its unit is still running.
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
	if isLive(st) {
		boxDir := r.boxDir(box.ID)
		if err := r.CtrlAltDel(apiSockPath(boxDir)); err != nil {
			// The guest may already be gone; the force stop below still lands.
		}
		if !r.waitInactive(unit, r.CleanStopTimeout) {
			_ = r.Sys.Stop(unit)
			if !r.waitInactive(unit, r.ForceStopTimeout) {
				return nil, fmt.Errorf("box %s did not stop; it is still running", shortID(box.ID))
			}
		}
	}
	_ = r.Sys.ResetFailed(unit)
	r.failRunningJob(box.ID, "box paused")
	return r.Store.Transition(box.ID, state.StatePaused)
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
			Version:      e.Name(),
			BuiltAt:      manifest.BuiltAt,
			KernelSHA256: manifest.Kernel.SHA256,
			RootfsSHA256: manifest.Rootfs.SHA256,
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
// Firecracker refuses to bind a path that already exists.
func clearSockets(boxDir string) error {
	for _, name := range []string{"firecracker.sock", "v.sock"} {
		path := filepath.Join(boxDir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	return nil
}

type imageManifest struct {
	BuiltAt string `json:"built_at"`
	Kernel  struct {
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

type fcConfig struct {
	BootSource        fcBootSource         `json:"boot-source"`
	Drives            []fcDrive            `json:"drives"`
	MachineConfig     fcMachineConfig      `json:"machine-config"`
	Vsock             fcVsock              `json:"vsock"`
	NetworkInterfaces []fcNetworkInterface `json:"network-interfaces"`
	Logger            fcLogger             `json:"logger"`
}

func writeConfig(boxDir, imageDir, id string) error {
	var cfg fcConfig
	cfg.BootSource = fcBootSource{
		KernelImagePath: filepath.Join(imageDir, "vmlinuz"),
		BootArgs:        "console=ttyS0 root=/dev/vda rw reboot=k panic=1",
	}
	cfg.Drives = []fcDrive{{
		DriveID:      "rootfs",
		PathOnHost:   filepath.Join(boxDir, "disk", "rootfs.img"),
		IsRootDevice: true,
		IsReadOnly:   false,
		CacheType:    "Writeback",
	}}
	cfg.MachineConfig = fcMachineConfig{VCPUCount: 2, MemSizeMiB: 1024}
	cfg.Vsock = fcVsock{GuestCID: guestCID(id), UDSPath: vsockPath(boxDir)}
	cfg.NetworkInterfaces = []fcNetworkInterface{{
		IfaceID:     "eth0",
		HostDevName: tapFc,
		GuestMAC:    "06:00:AC:10:00:0F",
	}}
	cfg.Logger = fcLogger{LogPath: filepath.Join(boxDir, "fc.log"), Level: "Warning"}

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

// defaultWaitReady waits for the box's sshd banner over vsock.
func defaultWaitReady(ctx context.Context, uds string) error {
	_, err := vsock.WaitReady(ctx, uds, guestPort)
	return err
}
