package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/fsutil"
)

// prepareDisk makes a box's disk bootable: it clones the base image on first
// use, grows the clone to the declared size, and injects the box's ssh key. It
// is idempotent, so a partially failed first attempt is repaired on the next
// up. diskMiB is the declared rootfs size in MiB; zero keeps the base image's
// size. An existing disk is never resized (recreate-only).
func prepareDisk(boxDir, imageDir string, diskMiB int) error {
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	if err := createDisk(disk, filepath.Join(imageDir, "rootfs.img"), diskMiB, growDisk); err != nil {
		return err
	}
	priv := filepath.Join(boxDir, "id")
	if _, err := os.Stat(priv); errors.Is(err, os.ErrNotExist) {
		if err := generateKey(priv); err != nil {
			return err
		}
	}
	return injectKey(boxDir, disk)
}

// createDisk clones base to disk on first use and grows the fresh clone to
// diskMiB (zero keeps the base image's size). The grow step is injected so
// unit tests need no real image. An existing disk is never touched: it belongs
// to an earlier incarnation, so changing the declared size cannot resize it in
// place (recreate-only).
func createDisk(disk, base string, diskMiB int, grow func(string, int) error) error {
	switch _, err := os.Stat(disk); {
	case err == nil:
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("stat %s: %w", disk, err)
	}
	if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
		return fmt.Errorf("create disk dir: %w", err)
	}
	if err := fsutil.CloneFile(base, disk); err != nil {
		return fmt.Errorf("clone base image: %w", err)
	}
	if diskMiB > 0 {
		if err := grow(disk, diskMiB); err != nil {
			// A failed grow leaves a half-built disk; remove it so the next
			// up retries from a clean clone instead of booting a broken one.
			_ = os.Remove(disk)
			return fmt.Errorf("grow disk: %w", err)
		}
	}
	return nil
}

// growDisk extends a freshly cloned ext4 rootfs to sizeMiB: truncate grows the
// image file, then resize2fs grows the filesystem to fill it. Growing is the
// only safe direction; a declared size below the base image is refused rather
// than silently keeping the base size (a box must not quietly ignore its
// contract).
func growDisk(disk string, sizeMiB int) error {
	info, err := os.Stat(disk)
	if err != nil {
		return fmt.Errorf("stat %s: %w", disk, err)
	}
	want := int64(sizeMiB) << 20
	switch {
	case want == info.Size():
		return nil
	case want < info.Size():
		return fmt.Errorf("declared disk %d MiB is smaller than the base image %d MiB; shrinking is not supported", sizeMiB, info.Size()>>20)
	}
	if err := os.Truncate(disk, want); err != nil {
		return fmt.Errorf("truncate %s: %w", disk, err)
	}
	cmd := exec.Command("resize2fs", disk)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("resize2fs %s: %w (%s)", disk, err, out)
	}
	return nil
}

// generateKey writes the box's ed25519 identity; the private key is what
// attach authenticates with.
func generateKey(priv string) error {
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", priv)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen: %w (%s)", err, out)
	}
	return nil
}

// injectKey writes the public key into the guest's authorized_keys with
// debugfs, without mounting the disk. The debugfs command refers to the key
// by a name relative to the box directory, so a state directory with spaces
// cannot break the command string.
func injectKey(boxDir, disk string) error {
	// A partial authorized_keys from an earlier attempt would make `write`
	// fail; its absence is fine.
	_ = runDebugfs(boxDir, disk, "rm /home/dev/.ssh/authorized_keys")
	for _, command := range []string{
		"write id.pub /home/dev/.ssh/authorized_keys",
		"sif /home/dev/.ssh/authorized_keys mode 0100600",
		"sif /home/dev/.ssh/authorized_keys uid 1000",
		"sif /home/dev/.ssh/authorized_keys gid 1000",
	} {
		if err := runDebugfs(boxDir, disk, command); err != nil {
			return err
		}
	}
	return nil
}

func runDebugfs(dir, disk, command string) error {
	cmd := exec.Command("debugfs", "-w", "-R", command, disk)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("debugfs %q: %w (%s)", command, err, out)
	}
	return nil
}
