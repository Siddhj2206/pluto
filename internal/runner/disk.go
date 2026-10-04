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
// use and injects the box's ssh key. It is idempotent, so a partially failed
// first attempt is repaired on the next up.
func prepareDisk(boxDir, imageDir string) error {
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	priv := filepath.Join(boxDir, "id")

	if _, err := os.Stat(disk); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
			return fmt.Errorf("create disk dir: %w", err)
		}
		if err := fsutil.CloneFile(filepath.Join(imageDir, "rootfs.img"), disk); err != nil {
			return fmt.Errorf("clone base image: %w", err)
		}
	}
	if _, err := os.Stat(priv); errors.Is(err, os.ErrNotExist) {
		if err := generateKey(priv); err != nil {
			return err
		}
	}
	return injectKey(boxDir, disk)
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
