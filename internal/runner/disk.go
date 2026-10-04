package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/fsutil"
)

// prepareDisk makes a box's disk bootable: it clones the base image on first
// use and injects a per-box ssh key. It is idempotent.
func prepareDisk(boxDir, imageDir string) error {
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	priv := filepath.Join(boxDir, "id")
	_, diskErr := os.Stat(disk)
	_, keyErr := os.Stat(priv)

	if diskErr == nil && keyErr == nil {
		return nil
	}
	if diskErr != nil {
		if err := os.MkdirAll(filepath.Dir(disk), 0o755); err != nil {
			return fmt.Errorf("create disk dir: %w", err)
		}
		if err := fsutil.CloneFile(filepath.Join(imageDir, "rootfs.img"), disk); err != nil {
			return fmt.Errorf("clone base image: %w", err)
		}
	}
	if keyErr != nil {
		if err := generateKey(priv); err != nil {
			return err
		}
		if err := injectKey(disk, priv+".pub"); err != nil {
			return err
		}
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
// debugfs, without mounting the disk.
func injectKey(disk, pub string) error {
	cmds := []string{
		"write " + pub + " /home/dev/.ssh/authorized_keys",
		"sif /home/dev/.ssh/authorized_keys mode 0100600",
		"sif /home/dev/.ssh/authorized_keys uid 1000",
		"sif /home/dev/.ssh/authorized_keys gid 1000",
	}
	for _, c := range cmds {
		cmd := exec.Command("debugfs", "-w", "-R", c, disk)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("debugfs %q: %w (%s)", c, err, out)
		}
	}
	return nil
}
