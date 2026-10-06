package runner_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestImportBuiltArtifact imports an artifact produced by
// cmd/pluto-image-builder through the same path as `pluto image import`.
// It is the image-build CI job's acceptance check: the job builds images/out
// and points PLUTO_IMAGE_ARTIFACT at it. Normal `go test ./...` skips it, so
// the suite stays hermetic — no podman, network, or multi-GB artifact.
func TestImportBuiltArtifact(t *testing.T) {
	src := os.Getenv("PLUTO_IMAGE_ARTIFACT")
	if src == "" {
		t.Skip("set PLUTO_IMAGE_ARTIFACT=<artifact-dir> to import a built image")
	}
	// `go test` runs with the package directory as its working directory, so a
	// relative artifact path (images/out, as documented) is resolved against
	// the module root, where the builder wrote it.
	if !filepath.IsAbs(src) {
		src = filepath.Join(moduleRoot(t), src)
	}
	if _, err := os.Stat(filepath.Join(src, "manifest.json")); err != nil {
		t.Fatalf("artifact %s is not a pluto artifact: %v", src, err)
	}

	h := newHarness(t)
	version, err := h.r.Import(src)
	if err != nil {
		t.Fatalf("Import(%s): %v", src, err)
	}
	if len(version) != 16 {
		t.Fatalf("version = %q, want 16 hex chars", version)
	}
	for _, name := range []string{"vmlinuz", "rootfs.img", "firecracker", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(h.root, "images", version, name)); err != nil {
			t.Fatalf("imported image missing %s: %v", name, err)
		}
	}
	// Import is content-addressed and idempotent; a rebuild must not fail here.
	again, err := h.r.Import(src)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if again != version {
		t.Fatalf("second Import = %q, want %q", again, version)
	}
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod, so a relative artifact path is independent of which package
// `go test` was pointed at.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
