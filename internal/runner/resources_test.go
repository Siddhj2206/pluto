package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// machineConfig reads the vcpu/memory pair the runner wrote for a box.
func machineConfig(t *testing.T, root, id string) (cpus, memMiB int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "boxes", id, "fc.json"))
	if err != nil {
		t.Fatalf("read fc.json: %v", err)
	}
	var cfg struct {
		MachineConfig struct {
			VCPUCount  int `json:"vcpu_count"`
			MemSizeMiB int `json:"mem_size_mib"`
		} `json:"machine-config"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse fc.json: %v", err)
	}
	return cfg.MachineConfig.VCPUCount, cfg.MachineConfig.MemSizeMiB
}

func dropInPath(unitDir, id string) string {
	return filepath.Join(unitDir, "pluto-box@"+id+".service.d", "resources.conf")
}

// TestUpSizesMachineFromContractResources is the headline: a contract that
// declares cpus/memory produces a box whose Firecracker machine config matches,
// and the size is recorded on the box for later starts.
func TestUpSizesMachineFromContractResources(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 4, memory = \"8GiB\" }\n")
	box := h.newBoxAt(t, worktree)

	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	cpus, memMiB := machineConfig(t, h.root, box.ID)
	if cpus != 4 || memMiB != 8192 {
		t.Fatalf("machine config = %d vCPU / %d MiB, want 4 / 8192", cpus, memMiB)
	}
	if got.Resources == nil || got.Resources.CPUs != 4 || got.Resources.MemoryMiB != 8192 {
		t.Fatalf("recorded resources = %+v, want 4 / 8192", got.Resources)
	}
}

// TestUpWithPartialResourcesFillsDefaults checks that declaring only one field
// leaves the other at today's default.
func TestUpWithPartialResourcesFillsDefaults(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 8 }\n")
	box := h.newBoxAt(t, worktree)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	cpus, memMiB := machineConfig(t, h.root, box.ID)
	if cpus != 8 || memMiB != contract.DefaultMemoryMiB {
		t.Fatalf("machine config = %d / %d, want 8 / %d", cpus, memMiB, contract.DefaultMemoryMiB)
	}
}

// TestUpSizesDiskFromContractResources is the disk headline: a contract that
// declares disk prepares the rootfs at that size, and the size is recorded on
// the box for later starts.
func TestUpSizesDiskFromContractResources(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { disk = \"40GiB\" }\n")
	box := h.newBoxAt(t, worktree)

	prepared := -1
	h.r.PrepareDisk = func(boxDir, imageDir string, diskMiB int) error {
		prepared = diskMiB
		return nil
	}
	got, err := h.r.Up(context.Background(), box)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if prepared != 40*1024 {
		t.Fatalf("PrepareDisk size = %d MiB, want 40960", prepared)
	}
	if got.Resources == nil || got.Resources.DiskMiB != 40*1024 {
		t.Fatalf("recorded resources = %+v, want DiskMiB 40960", got.Resources)
	}
}

// TestUpWithoutDiskKeepsBaseSize pins the default: a contract with no disk (or
// no resources at all) passes zero, which tells disk preparation to keep the
// base image's size.
func TestUpWithoutDiskKeepsBaseSize(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 4 }\n")
	box := h.newBoxAt(t, worktree)

	prepared := -1
	h.r.PrepareDisk = func(boxDir, imageDir string, diskMiB int) error {
		prepared = diskMiB
		return nil
	}
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if prepared != 0 {
		t.Fatalf("PrepareDisk size = %d MiB, want 0 (keep the base image's size)", prepared)
	}
}

// TestUpDoesNotResizeDiskOnContractEdit pins recreate-only semantics for disk:
// the size is frozen at first start, so editing [box].resources.disk and
// restarting the box keeps preparing the original disk.
func TestUpDoesNotResizeDiskOnContractEdit(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { disk = \"40GiB\" }\n")
	box := h.newBoxAt(t, worktree)

	var prepared []int
	h.r.PrepareDisk = func(boxDir, imageDir string, diskMiB int) error {
		prepared = append(prepared, diskMiB)
		return nil
	}
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// The user shrinks the declared disk; a restart must keep the created size.
	writeContract(t, worktree, "[box]\nresources = { disk = \"10GiB\" }\n")
	h.sys.set(unitName(box.ID), "inactive")
	if _, err := h.r.Up(context.Background(), mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	for i, got := range prepared {
		if got != 40*1024 {
			t.Fatalf("PrepareDisk call %d size = %d MiB, want the created 40960", i, got)
		}
	}
}

// TestUpWithoutResourcesKeepsDefaults pins the no-contract and empty-resources
// cases: today's 2 vCPU / 1024 MiB.
func TestUpWithoutResourcesKeepsDefaults(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t) // worktree does not exist: no contract at all

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	cpus, memMiB := machineConfig(t, h.root, box.ID)
	if cpus != contract.DefaultCPUs || memMiB != contract.DefaultMemoryMiB {
		t.Fatalf("machine config = %d / %d, want %d / %d", cpus, memMiB, contract.DefaultCPUs, contract.DefaultMemoryMiB)
	}
}

// TestUpDoesNotResizeOnContractEdit pins recreate-only semantics: the size is
// frozen at first start, so editing [box].resources and restarting the box
// leaves the machine as created.
func TestUpDoesNotResizeOnContractEdit(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 4, memory = \"8GiB\" }\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// The user shrinks the contract; a restart must not resize the box.
	writeContract(t, worktree, "[box]\nresources = { cpus = 1, memory = \"256MiB\" }\n")
	h.sys.set(unitName(box.ID), "inactive")
	if _, err := h.r.Up(context.Background(), mustBox(t, h.st, box.ID)); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	cpus, memMiB := machineConfig(t, h.root, box.ID)
	if cpus != 4 || memMiB != 8192 {
		t.Fatalf("machine config after edit = %d / %d, want the created 4 / 8192", cpus, memMiB)
	}
}

// TestUpAppliesCgroupLimits writes a per-instance systemd drop-in, the
// rootless cgroup mechanism: systemd already owns each box's cgroup, so a
// drop-in sets MemoryMax and CPUQuota without touching cgroupfs by hand.
func TestUpAppliesCgroupLimits(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 3, memory = \"6GiB\" }\n")
	box := h.newBoxAt(t, worktree)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	data, err := os.ReadFile(dropInPath(h.r.UnitDir, box.ID))
	if err != nil {
		t.Fatalf("read cgroup drop-in: %v", err)
	}
	for _, want := range []string{"[Service]", "MemoryMax=6912M", "CPUQuota=300%"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("drop-in missing %q:\n%s", want, data)
		}
	}
}

// TestUpCgroupLimitsDefault keeps the cgroup caps honest when the contract
// declares no resources.
func TestUpCgroupLimitsDefault(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	box := h.newBox(t)

	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	data, err := os.ReadFile(dropInPath(h.r.UnitDir, box.ID))
	if err != nil {
		t.Fatalf("read cgroup drop-in: %v", err)
	}
	for _, want := range []string{"MemoryMax=1280M", "CPUQuota=200%"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("drop-in missing %q:\n%s", want, data)
		}
	}
}

// TestDestroyRemovesCgroupDropIn cleans up the per-instance caps so a
// recreated box does not inherit the old size.
func TestDestroyRemovesCgroupDropIn(t *testing.T) {
	h := newHarness(t)
	h.importImage(t, "a")
	worktree := t.TempDir()
	writeContract(t, worktree, "[box]\nresources = { cpus = 4, memory = \"8GiB\" }\n")
	box := h.newBoxAt(t, worktree)
	if _, err := h.r.Up(context.Background(), box); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := os.Stat(dropInPath(h.r.UnitDir, box.ID)); err != nil {
		t.Fatalf("drop-in not written: %v", err)
	}

	if err := h.r.Destroy(box.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(dropInPath(h.r.UnitDir, box.ID)); !os.IsNotExist(err) {
		t.Fatalf("drop-in survived Destroy: %v", err)
	}
}
