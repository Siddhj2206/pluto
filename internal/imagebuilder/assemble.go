package imagebuilder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Fixed ext4 identity. The UUID, label, and directory hash seed make the
// filesystem bytes stable across rebuilds; without the hash seed mke2fs picks
// a random one (reproducible-builds.org system-images).
const (
	DefaultUUID     = "0f15a7e1-8b1c-4a53-9c2e-706c75746f00"
	DefaultLabel    = "pluto-root"
	DefaultHashSeed = "035cb65d-0a86-404a-bad7-19c88d05e400"
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
	// SourceDateEpoch clamps every file and filesystem timestamp to a pinned
	// value, so the image carries no build-clock field. Required.
	SourceDateEpoch int64
	// UUID, Label, and HashSeed fix the filesystem identity (defaulted when
	// empty).
	UUID     string
	Label    string
	HashSeed string
}

// Assemble turns an exported rootfs tar into a reproducible rootfs.img:
// extract with numeric ownership, install the guest agent as root, clamp every
// file timestamp to the pinned SOURCE_DATE_EPOCH, then mkfs.ext4 from the
// extracted tree with a fixed UUID, label, and hash seed.
//
// Ownership comes from the tar's numeric ids (dev is uid 1000, system files
// are uid 0); mke2fs walks the tree in sorted name order, so the tar's own
// entry order does not leak into the image. The host's e2fsprogs may be built
// without libarchive (Fedora's is), so the tree is handed to `mkfs.ext4 -d` as
// a directory rather than a tarball; clamping mtimes explicitly replaces the
// tar --clamp-mtime step from the reproducible-builds recipe.
//
// Each step runs through `podman unshare` so the numeric owners survive into
// the ext4 image as a rootless user. The final `rm -rf` is namespaced too,
// because the extracted tree is owned by the subuid range and the caller
// cannot remove it directly.
func Assemble(ctx context.Context, sh Shell, op AssembleOptions) error {
	if op.DiskMB <= 0 {
		return fmt.Errorf("assemble: disk size must be positive, got %d MiB", op.DiskMB)
	}
	if op.SourceDateEpoch <= 0 {
		return fmt.Errorf("assemble: source date epoch must be positive, got %d", op.SourceDateEpoch)
	}
	if op.UUID == "" {
		op.UUID = DefaultUUID
	}
	if op.Label == "" {
		op.Label = DefaultLabel
	}
	if op.HashSeed == "" {
		op.HashSeed = DefaultHashSeed
	}
	epoch := strconv.FormatInt(op.SourceDateEpoch, 10)
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
	// Clamp every mtime (and symlink mtime) to the pinned epoch, after the
	// agent install so the agent's own mtime is normalized too.
	if err := unshare("find", rootfs, "-exec", "touch", "-h", "-d", "@"+epoch, "{}", "+"); err != nil {
		return fmt.Errorf("assemble: clamp mtimes: %w", err)
	}
	if err := sizeImage(op.Image, op.DiskMB); err != nil {
		return err
	}
	if err := sh.Run(ctx, Command{
		Name: "podman",
		Args: []string{"unshare", "mkfs.ext4", "-q", "-F", "-L", op.Label, "-U", op.UUID, "-E", "hash_seed=" + op.HashSeed, "-d", rootfs, op.Image},
		Env:  []string{"SOURCE_DATE_EPOCH=" + epoch},
	}); err != nil {
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
