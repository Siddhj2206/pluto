package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPrepareDiskIsIdempotentAndInjectsKey exercises the real disk seam on a
// tiny ext4 image: cloning, growing to the declared size, key generation, and
// debugfs injection, twice.
func TestPrepareDiskIsIdempotentAndInjectsKey(t *testing.T) {
	for _, tool := range []string{"mkfs.ext4", "debugfs", "resize2fs", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable: %v", tool, err)
		}
	}
	dir := t.TempDir()
	boxDir := filepath.Join(dir, "box")
	imageDir := filepath.Join(dir, "image")
	rootfsTree := filepath.Join(dir, "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfsTree, "home/dev/.ssh"), 0o755); err != nil {
		t.Fatalf("mkdir rootfs tree: %v", err)
	}
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	base := filepath.Join(imageDir, "rootfs.img")
	f, err := os.Create(base)
	if err != nil {
		t.Fatalf("create base image: %v", err)
	}
	if err := f.Truncate(8 << 20); err != nil {
		t.Fatalf("truncate base image: %v", err)
	}
	f.Close()
	if out, err := exec.Command("mkfs.ext4", "-q", "-F", "-d", rootfsTree, base).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v (%s)", err, out)
	}

	const diskMiB = 16
	for run := 1; run <= 2; run++ {
		if err := prepareDisk(boxDir, imageDir, diskMiB); err != nil {
			t.Fatalf("prepareDisk run %d: %v", run, err)
		}
		info, err := os.Stat(filepath.Join(boxDir, "disk", "rootfs.img"))
		if err != nil {
			t.Fatalf("stat disk after run %d: %v", run, err)
		}
		if info.Size() != int64(diskMiB)<<20 {
			t.Fatalf("disk size after run %d = %d, want %d", run, info.Size(), int64(diskMiB)<<20)
		}
	}
	pub, err := os.ReadFile(filepath.Join(boxDir, "id.pub"))
	if err != nil {
		t.Fatalf("read id.pub: %v", err)
	}
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	out, err := exec.Command("debugfs", "-R", "cat /home/dev/.ssh/authorized_keys", disk).Output()
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(pub)) {
		t.Fatalf("authorized_keys = %q, want the box public key %q", out, pub)
	}
}

// TestCreateDiskGrowsFreshClone proves the declared size reaches the fresh
// clone, with the real grow step injected so no image or resize2fs is needed.
func TestCreateDiskGrowsFreshClone(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.img")
	if err := os.WriteFile(base, []byte("base-image-bytes"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	disk := filepath.Join(dir, "disk", "rootfs.img")

	var calls []int
	grow := func(path string, sizeMiB int) error {
		if path != disk {
			t.Errorf("grow path = %q, want %q", path, disk)
		}
		calls = append(calls, sizeMiB)
		return nil
	}
	if err := createDisk(disk, base, 40960, grow); err != nil {
		t.Fatalf("createDisk: %v", err)
	}
	if len(calls) != 1 || calls[0] != 40960 {
		t.Fatalf("grow calls = %v, want [40960]", calls)
	}
	if data, err := os.ReadFile(disk); err != nil || string(data) != "base-image-bytes" {
		t.Fatalf("cloned disk = %q, %v; want the base bytes", data, err)
	}
}

// TestCreateDiskNeverResizesExisting pins recreate-only for disk: an existing
// rootfs is left exactly as it is, whatever size the contract now declares.
func TestCreateDiskNeverResizesExisting(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.img")
	if err := os.WriteFile(base, []byte("base-image-bytes"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	disk := filepath.Join(dir, "disk", "rootfs.img")
	if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
		t.Fatalf("mkdir disk dir: %v", err)
	}
	if err := os.WriteFile(disk, []byte("existing-disk"), 0o644); err != nil {
		t.Fatalf("write existing disk: %v", err)
	}

	grew := false
	grow := func(string, int) error { grew = true; return nil }
	if err := createDisk(disk, base, 40960, grow); err != nil {
		t.Fatalf("createDisk: %v", err)
	}
	if grew {
		t.Fatal("existing disk was grown; recreate-only semantics broken")
	}
	if data, err := os.ReadFile(disk); err != nil || string(data) != "existing-disk" {
		t.Fatalf("existing disk = %q, %v; want it untouched", data, err)
	}
}

// TestCreateDiskWithoutSizeKeepsBase checks the default: no declared disk
// clones the base and never runs the grow step.
func TestCreateDiskWithoutSizeKeepsBase(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.img")
	if err := os.WriteFile(base, []byte("base-image-bytes"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	disk := filepath.Join(dir, "disk", "rootfs.img")

	grew := false
	grow := func(string, int) error { grew = true; return nil }
	if err := createDisk(disk, base, 0, grow); err != nil {
		t.Fatalf("createDisk: %v", err)
	}
	if grew {
		t.Fatal("unset disk still ran the grow step")
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatalf("disk not cloned: %v", err)
	}
}

// TestCreateDiskGrowFailureRemovesDisk makes a failed first boot retryable: a
// grow error must not leave a half-built disk that the next up would boot.
func TestCreateDiskGrowFailureRemovesDisk(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.img")
	if err := os.WriteFile(base, []byte("base-image-bytes"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	disk := filepath.Join(dir, "disk", "rootfs.img")

	grow := func(string, int) error { return os.ErrInvalid }
	if err := createDisk(disk, base, 40960, grow); err == nil {
		t.Fatal("createDisk should report the grow failure")
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Fatalf("half-built disk survived the failed grow: %v", err)
	}
}

// TestGrowDiskRejectsShrinking refuses a declared size below the base image
// rather than silently keeping the base size.
func TestGrowDiskRejectsShrinking(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "rootfs.img")
	f, err := os.Create(disk)
	if err != nil {
		t.Fatalf("create disk: %v", err)
	}
	if err := f.Truncate(2 << 20); err != nil {
		t.Fatalf("truncate disk: %v", err)
	}
	f.Close()

	if err := growDisk(disk, 1); err == nil {
		t.Fatal("growDisk should refuse a size below the image")
	}
}

// TestGrowDiskEqualSizeIsNoOp keeps a retried first boot from touching a disk
// that is already the declared size.
func TestGrowDiskEqualSizeIsNoOp(t *testing.T) {
	disk := filepath.Join(t.TempDir(), "rootfs.img")
	if err := os.WriteFile(disk, nil, 0o644); err != nil {
		t.Fatalf("create disk: %v", err)
	}
	if err := os.Truncate(disk, 1<<20); err != nil {
		t.Fatalf("truncate disk: %v", err)
	}
	if err := growDisk(disk, 1); err != nil {
		t.Fatalf("growDisk to the current size: %v", err)
	}
}
