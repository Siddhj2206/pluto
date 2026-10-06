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

// TestDoubleBuildDeterminism builds the base image twice into clean output
// directories and requires byte-identical vmlinuz, rootfs.img, and
// manifest.json. It is the Tier 1 determinism bar for #71: same pins, same
// source, identical bytes.
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
	t.Cleanup(func() { os.RemoveAll(base) })

	buildOnce := func(name string) string {
		out := filepath.Join(base, name)
		b := imagebuilder.New(pins, out, root, filepath.Join(root, "images"))
		b.DiskMB = 512
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
