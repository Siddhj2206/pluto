package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPrepareDiskIsIdempotentAndInjectsKey exercises the real disk seam on a
// tiny ext4 image: cloning, key generation, and debugfs injection, twice.
func TestPrepareDiskIsIdempotentAndInjectsKey(t *testing.T) {
	for _, tool := range []string{"mkfs.ext4", "debugfs", "ssh-keygen"} {
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

	for run := 1; run <= 2; run++ {
		if err := prepareDisk(boxDir, imageDir); err != nil {
			t.Fatalf("prepareDisk run %d: %v", run, err)
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
