package imagebuilder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Fixed ext4 identity, matching the original bash assembly. The UUID and label
// make the artifact's filesystem stable across rebuilds; the hash seed and
// tarball input that complete the deterministic recipe belong to #71.
const (
	DefaultUUID  = "0f15a7e1-8b1c-4a53-9c2e-706c75746f00"
	DefaultLabel = "pluto-root"
)

// AssembleOptions describes one rootfs assembly.
type AssembleOptions struct {
	// Tar is the podman-exported rootfs tar.
	Tar string
	// Dir is the artifact directory; extraction happens in Dir/rootfs.
	Dir string
	// Agent is the built pluto-agent binary to install into the rootfs.
	Agent string
	// Image is the rootfs.img to create.
	Image string
	// DiskMB is the image size in MiB.
	DiskMB int
	// UUID and Label fix the filesystem identity (defaulted when empty).
	UUID  string
	Label string
}

// Assemble turns an exported rootfs tar into rootfs.img: extract with numeric
// ownership, install the guest agent as root, size the image, and mkfs.ext4
// from the extracted tree. Each step runs through `podman unshare` so the
// numeric owners in the container image (dev is uid 1000, system files are
// uid 0) survive into the ext4 image as a rootless user. The final `rm -rf`
// is namespaced too, because the extracted tree is owned by the subuid range
// and the caller cannot remove it directly.
func Assemble(ctx context.Context, sh Shell, op AssembleOptions) error {
	if op.DiskMB <= 0 {
		return fmt.Errorf("assemble: disk size must be positive, got %d MiB", op.DiskMB)
	}
	if op.UUID == "" {
		op.UUID = DefaultUUID
	}
	if op.Label == "" {
		op.Label = DefaultLabel
	}
	rootfs := filepath.Join(op.Dir, "rootfs")
	unshare := func(name string, args ...string) error {
		return sh.Run(ctx, Command{Name: "podman", Args: append([]string{"unshare", name}, args...)})
	}
	if err := unshare("rm", "-rf", rootfs); err != nil {
		return fmt.Errorf("assemble: clear rootfs: %w", err)
	}
	if err := unshare("mkdir", "-p", rootfs); err != nil {
		return fmt.Errorf("assemble: create rootfs: %w", err)
	}
	if err := unshare("tar", "-x", "--numeric-owner", "-f", op.Tar, "-C", rootfs); err != nil {
		return fmt.Errorf("assemble: extract %s: %w", op.Tar, err)
	}
	agentDst := filepath.Join(rootfs, "usr", "local", "bin", "pluto-agent")
	if err := unshare("install", "-m", "0755", "-o", "root", "-g", "root", op.Agent, agentDst); err != nil {
		return fmt.Errorf("assemble: install agent: %w", err)
	}
	if err := sizeImage(op.Image, op.DiskMB); err != nil {
		return err
	}
	if err := unshare("mkfs.ext4", "-q", "-F", "-L", op.Label, "-U", op.UUID, "-d", rootfs, op.Image); err != nil {
		return fmt.Errorf("assemble: mkfs.ext4: %w", err)
	}
	if err := unshare("rm", "-rf", rootfs); err != nil {
		return fmt.Errorf("assemble: clean rootfs: %w", err)
	}
	return nil
}

// sizeImage replaces path with a sparse file of diskMB MiB.
func sizeImage(path string, diskMB int) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("assemble: remove old image: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("assemble: create image: %w", err)
	}
	defer f.Close()
	if err := f.Truncate(int64(diskMB) << 20); err != nil {
		return fmt.Errorf("assemble: size image: %w", err)
	}
	return nil
}
