//go:build host

// Host-only determinism test. It needs rootless podman, unprivileged user
// namespaces, and network access to the pinned kernel and snapshot mirrors, so
// it is gated behind the `host` build tag and never runs in CI:
//
//	go test -tags host ./internal/imagebuilder/ -run TestDoubleBuildDeterminism -v -timeout 30m
//
// PLUTO_DETERMINISM_DIR picks the scratch parent (default: a temp dir under the
// repository root, on the same filesystem as the checkout rather than a small
// tmpfs).
package imagebuilder_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
)

// TestDoubleBuildDeterminism builds the base image twice into separate, clean
// output directories — each with its own download cache and its own podman
// storage root — and requires byte-identical vmlinuz, rootfs.img, and
// manifest.json. It is the Tier 1 determinism bar for #71: same pins, same
// source, identical bytes. Isolating podman storage means the second build
// cannot reuse a layer the first produced, so a non-deterministic layer would
// show up as a diff rather than being masked by the cache.
func TestDoubleBuildDeterminism(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman unavailable: %v", err)
	}
	root := repoRoot(t)
	pins, err := imagebuilder.LoadPins(filepath.Join(root, "images", "pins.yaml"))
	if err != nil {
		t.Fatalf("LoadPins: %v", err)
	}

	base := os.Getenv("PLUTO_DETERMINISM_DIR")
	if base == "" {
		base, err = os.MkdirTemp(root, ".determinism-")
		if err != nil {
			t.Fatalf("scratch dir: %v", err)
		}
	} else if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("scratch dir: %v", err)
	}
	t.Cleanup(func() {
		// Each build's podman graphroot holds subuid-owned files the test user
		// cannot remove directly; clean inside `podman unshare`. Fall back to
		// a plain remove when podman is unavailable.
		if err := exec.Command("podman", "unshare", "rm", "-rf", base).Run(); err != nil {
			os.RemoveAll(base)
		}
	})

	buildOnce := func(name string) string {
		out := filepath.Join(base, name)
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatalf("clean out dir %s: %v", out, err)
		}
		b := imagebuilder.New(pins, out, root, filepath.Join(root, "images"))
		b.DiskMB = 512
		// Give this build its own podman graphroot and runroot, so neither
		// build can reuse the other's pulled base image or apt layer.
		b.Shell = isolatedShell{
			Shell:   imagebuilder.ExecShell{},
			graph:   filepath.Join(out, "podman-graph"),
			runroot: filepath.Join(out, "podman-run"),
		}
		if err := b.Build(context.Background()); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
		return out
	}
	first := buildOnce("first")
	second := buildOnce("second")

	for _, name := range []string{"vmlinuz", "rootfs.img", "manifest.json"} {
		want := fileSHA(t, filepath.Join(first, name))
		got := fileSHA(t, filepath.Join(second, name))
		if want != got {
			t.Errorf("%s differs across clean rebuilds:\n  first:  %s\n  second: %s", name, want, got)
		}
	}
}

// isolatedShell points podman at a per-build graphroot/runroot by prepending
// its global flags, and passes every other command through unchanged.
type isolatedShell struct {
	imagebuilder.Shell
	graph   string
	runroot string
}

func (s isolatedShell) rewrite(cmd imagebuilder.Command) imagebuilder.Command {
	if cmd.Name == "podman" {
		cmd.Args = append([]string{"--root", s.graph, "--runroot", s.runroot}, cmd.Args...)
	}
	return cmd
}

func (s isolatedShell) Run(ctx context.Context, cmd imagebuilder.Command) error {
	return s.Shell.Run(ctx, s.rewrite(cmd))
}

func (s isolatedShell) Output(ctx context.Context, cmd imagebuilder.Command) ([]byte, error) {
	return s.Shell.Output(ctx, s.rewrite(cmd))
}

// repoRoot resolves the repository root from the package directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

// fileSHA streams a file's SHA-256 so a multi-hundred-megabyte rootfs never
// lands in memory.
func fileSHA(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
