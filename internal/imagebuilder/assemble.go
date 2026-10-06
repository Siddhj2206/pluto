package imagebuilder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Fixed ext4 identity. The UUID, label, and directory hash seed make the
// filesystem bytes stable across rebuilds; without the hash seed mke2fs picks
// a random one (reproducible-builds.org system-images).
const (
	DefaultUUID     = "0f15a7e1-8b1c-4a53-9c2e-706c75746f00"
	DefaultLabel    = "pluto-root"
	DefaultHashSeed = "035cb65d-0a86-404a-bad7-19c88d05e400"
)

// MkfsTimePinning selects how Assemble pins the timestamps mke2fs writes.
type MkfsTimePinning int

const (
	// TimePinningSourceDateEpoch is the native path: e2fsprogs >= 1.47.1 reads
	// SOURCE_DATE_EPOCH and clamps the superblock and inode timestamps.
	TimePinningSourceDateEpoch MkfsTimePinning = iota
	// TimePinningFaketime runs mkfs.ext4 under faketime for hosts whose
	// e2fsprogs predates 1.47.1 and ignores SOURCE_DATE_EPOCH.
	TimePinningFaketime
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
	// TimePinning selects how the mkfs step pins timestamps: natively via
	// SOURCE_DATE_EPOCH (the zero value) or under faketime for hosts whose
	// e2fsprogs predates 1.47.1. Callers get it from chooseMkfsTimePinning;
	// the zero value is the preferred native path.
	TimePinning MkfsTimePinning
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
	mkfs := []string{"mkfs.ext4", "-q", "-F", "-L", op.Label, "-U", op.UUID, "-E", "hash_seed=" + op.HashSeed, "-d", rootfs, op.Image}
	env := []string{"SOURCE_DATE_EPOCH=" + epoch}
	if op.TimePinning == TimePinningFaketime {
		// Older e2fsprogs ignores SOURCE_DATE_EPOCH, so freeze the clock
		// instead. FAKETIME_FMT=%s lets faketime take the pinned epoch
		// directly; its default format is "YYYY-MM-DD hh:mm:ss", and an
		// "@<epoch>" start-at would let the clock advance and vary the bytes.
		mkfs = append([]string{"faketime", "-f", epoch}, mkfs...)
		env = append(env, "FAKETIME_FMT=%s")
	}
	if err := sh.Run(ctx, Command{
		Name: "podman",
		Args: append([]string{"unshare"}, mkfs...),
		Env:  env,
	}); err != nil {
		return fmt.Errorf("assemble: mkfs.ext4: %w", err)
	}
	if err := unshare("rm", "-rf", rootfs); err != nil {
		return fmt.Errorf("assemble: clean rootfs: %w", err)
	}
	return nil
}

// mkfsMinE2fsprogs is the first e2fsprogs release whose mke2fs honors
// SOURCE_DATE_EPOCH. Older releases silently ignore it and stamp the build
// clock into the superblock and inodes, so the rootfs stops being
// reproducible. images/README.md documents this; the probe below picks the
// native path or the faketime fallback.
var mkfsMinE2fsprogs = [3]int{1, 47, 1}

// chooseMkfsTimePinning probes the host once, before any costly work, and picks
// how Assemble pins filesystem timestamps. The native path (e2fsprogs >=
// 1.47.1, which honors SOURCE_DATE_EPOCH) is preferred. When the host's
// mkfs.ext4 is older it falls back to faketime, which freezes the clock for the
// mkfs run. Only when neither is available does it fail.
func chooseMkfsTimePinning(ctx context.Context, sh Shell) (MkfsTimePinning, error) {
	// `mkfs.ext4 -V` (mke2fs) writes its version to stderr, so capture both
	// streams rather than trusting stdout.
	var stderr bytes.Buffer
	out, err := sh.Output(ctx, Command{Name: "mkfs.ext4", Args: []string{"-V"}, Stderr: &stderr})
	if err != nil {
		return 0, fmt.Errorf("image builder: probe mkfs.ext4 (-V): %w (need e2fsprogs >= 1.47.1)", err)
	}
	version, ok := parseE2fsprogsVersion(string(out) + stderr.String())
	if ok && !versionLess(version, mkfsMinE2fsprogs) {
		return TimePinningSourceDateEpoch, nil
	}
	if err := probeFaketime(ctx, sh); err == nil {
		return TimePinningFaketime, nil
	}
	if ok {
		return 0, fmt.Errorf("image builder: host e2fsprogs is %d.%d.%d, which ignores SOURCE_DATE_EPOCH, and faketime is not available; install e2fsprogs >= 1.47.1 or faketime (e.g. `apt-get install faketime`) so mkfs.ext4 runs at the pinned time",
			version[0], version[1], version[2])
	}
	return 0, fmt.Errorf("image builder: cannot read the e2fsprogs version from %q and faketime is not available; install e2fsprogs >= 1.47.1 or faketime so mkfs.ext4 runs at the pinned time",
		strings.TrimSpace(string(out)+stderr.String()))
}

// probeFaketime reports whether a working faketime is on PATH by running it
// with a valid frozen timestamp. A functional probe is deliberate: `--version`
// would prove the wrapper exists but not that libfaketime can preload and exec
// the command it needs to time-pin.
func probeFaketime(ctx context.Context, sh Shell) error {
	_, err := sh.Output(ctx, Command{Name: "faketime", Args: []string{"-f", "1970-01-01 00:00:00", "true"}})
	return err
}

// parseE2fsprogsVersion finds the first dotted version in `mkfs.ext4 -V`
// output, which reads "mke2fs 1.47.4 (6-Mar-2025)".
func parseE2fsprogsVersion(out string) ([3]int, bool) {
	for _, field := range strings.Fields(out) {
		parts := strings.Split(strings.Trim(field, "()"), ".")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		var v [3]int
		ok := true
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				ok = false
				break
			}
			v[i] = n
		}
		if ok {
			return v, true
		}
	}
	return [3]int{}, false
}

// versionLess reports whether a is older than b, comparing major, minor, then
// patch.
func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
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
