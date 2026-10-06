package imagebuilder_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
	"github.com/Siddhj2206/pluto/internal/runner"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fetcher serves a body chosen by a substring of the URL, so the kernel and
// Firecracker downloads can differ.
type fetcher map[string][]byte

func (f fetcher) fetch(_ context.Context, url string) (io.ReadCloser, error) {
	for key, body := range f {
		if strings.Contains(url, key) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	return nil, fmt.Errorf("no body for %s", url)
}

func TestBuildProducesImportableArtifact(t *testing.T) {
	out := t.TempDir()
	root := t.TempDir()
	imagesDir := t.TempDir()

	kernel := []byte("kernel-bytes")
	fcTgz := []byte("firecracker-tgz-bytes")
	pins := imagebuilder.Pins{
		Base: imagebuilder.BasePins{
			Image:  "ubuntu:24.04",
			Digest: "sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55",
		},
		Apt: imagebuilder.AptPins{
			Snapshot: "20261001T000000Z",
			Packages: []string{"ca-certificates", "git"},
		},
		Toolchain: imagebuilder.ToolchainPins{Go: "1.27.1"},
		Firecracker: imagebuilder.FirecrackerPins{
			Version: "1.17.0",
			URL:     "https://example.com/firecracker-v1.17.0-x86_64.tgz",
			SHA256:  sha256Hex(fcTgz),
		},
		Kernel: imagebuilder.KernelPins{
			URL:    "https://example.com/vmlinux-6.18.51",
			SHA256: sha256Hex(kernel),
		},
	}

	// The Containerfile and agent unit are copied from the images dir.
	write(t, filepath.Join(imagesDir, "Containerfile"), "FROM ${BASE_IMAGE}\n", 0o644)
	if err := os.MkdirAll(filepath.Join(imagesDir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(imagesDir, "files", "pluto-agent.service"), "[Unit]\n", 0o644)

	sh := newFakeShell()
	sh.outputs["podman"] = "container-id"
	sh.handler = artifactHandler()

	b := imagebuilder.New(pins, out, root, imagesDir)
	b.Shell = sh
	b.Fetch = fetcher{"vmlinux": kernel, "firecracker": fcTgz}.fetch
	b.DiskMB = 8

	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, name := range []string{"vmlinuz", "rootfs.img", "manifest.json", "cache/firecracker"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("artifact missing %s: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m imagebuilder.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Schema != 2 {
		t.Errorf("schema = %d, want 2", m.Schema)
	}
	epoch, err := pins.Apt.Epoch()
	if err != nil {
		t.Fatalf("Apt.Epoch: %v", err)
	}
	if m.SourceDateEpoch != epoch {
		t.Errorf("source_date_epoch = %d, want %d", m.SourceDateEpoch, epoch)
	}
	if got, want := m.Kernel.SHA256, sha256Hex(kernel); got != want {
		t.Errorf("kernel.sha256 = %s, want %s", got, want)
	}
	if got, want := m.Kernel.Name, "vmlinux-6.18.51"; got != want {
		t.Errorf("kernel.name = %q, want %q", got, want)
	}
	if got, want := m.Rootfs.SHA256, sha256Hex([]byte("rootfs-bytes")); got != want {
		t.Errorf("rootfs.sha256 = %s, want %s", got, want)
	}
	if got, want := m.Rootfs.Base, pins.Base.String(); got != want {
		t.Errorf("rootfs.base = %q, want %q", got, want)
	}
	if got, want := m.Rootfs.AptSnapshot, pins.Apt.Snapshot; got != want {
		t.Errorf("rootfs.apt_snapshot = %q, want %q", got, want)
	}
	if m.Rootfs.DiskMB != 8 {
		t.Errorf("rootfs.disk_mb = %d, want 8", m.Rootfs.DiskMB)
	}
	if got, want := m.Agent.SHA256, sha256Hex([]byte("./cmd/pluto-agent-bytes")); got != want {
		t.Errorf("agent.sha256 = %s, want %s", got, want)
	}
	if got, want := m.Firecracker.Version, "1.17.0"; got != want {
		t.Errorf("firecracker.version = %q, want %q", got, want)
	}
	if got, want := m.Firecracker.SHA256, sha256Hex([]byte("fc-bytes")); got != want {
		t.Errorf("firecracker.sha256 = %s, want %s", got, want)
	}

	// The podman build carries the pinned base, snapshot, packages, and epoch.
	build := findCmd(t, sh, "podman", "build")
	for _, want := range []string{
		"BASE_IMAGE=" + pins.Base.String(),
		"APT_SNAPSHOT=" + pins.Apt.Snapshot,
		"APT_PACKAGES=ca-certificates git",
		"SOURCE_DATE_EPOCH=" + strconv.FormatInt(epoch, 10),
	} {
		if !contains(build.args, want) {
			t.Errorf("podman build args missing %q:\n%v", want, build.args)
		}
	}

	// The guest helpers are built with the pinned toolchain and no VCS stamp:
	// the same source must produce the same binaries on any host.
	for _, pkg := range []string{"./cmd/pluto-agent", "./cmd/pluto-vsock"} {
		build := findCmd(t, sh, "go", pkg)
		for _, want := range []string{"-trimpath", "-buildvcs=false"} {
			if !contains(build.args, want) {
				t.Errorf("go build %s args missing %q:\n%v", pkg, want, build.args)
			}
		}
		if !contains(build.env, "GOTOOLCHAIN=go1.27.1") {
			t.Errorf("go build %s env missing the pinned toolchain:\n%v", pkg, build.env)
		}
	}

	// Acceptance: `pluto image import` accepts the artifact.
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer st.Close()
	r := runner.New(st, "/bin/pluto")
	if _, err := r.Import(out); err != nil {
		t.Fatalf("pluto image import rejected the artifact: %v", err)
	}
}

func TestBuildRejectsKernelChecksumMismatch(t *testing.T) {
	out := t.TempDir()
	imagesDir := t.TempDir()
	write(t, filepath.Join(imagesDir, "Containerfile"), "x", 0o644)
	pins := imagebuilder.Pins{
		Base:        imagebuilder.BasePins{Image: "ubuntu:24.04", Digest: "sha256:" + strings.Repeat("a", 64)},
		Apt:         imagebuilder.AptPins{Snapshot: "20261001T000000Z", Packages: []string{"git"}},
		Firecracker: imagebuilder.FirecrackerPins{Version: "1.17.0", URL: "https://example.com/fc.tgz", SHA256: strings.Repeat("b", 64)},
		Kernel:      imagebuilder.KernelPins{URL: "https://example.com/vmlinux", SHA256: strings.Repeat("c", 64)},
	}
	b := imagebuilder.New(pins, out, t.TempDir(), imagesDir)
	b.Shell = newFakeShell()
	b.Fetch = fetcher{"vmlinux": []byte("wrong-bytes")}.fetch
	b.DiskMB = 1

	err := b.Build(context.Background())
	if err == nil {
		t.Fatal("Build accepted a kernel whose bytes do not match the pin")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %v, want a checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "vmlinuz")); !os.IsNotExist(statErr) {
		t.Error("a mismatched kernel must not be copied into the artifact")
	}
}

// TestBuildFallsBackToFaketime proves the reconciliation with #76: when the
// host's e2fsprogs predates 1.47.1, Build chooses the faketime fallback rather
// than failing, and the mkfs step runs under faketime at the pinned epoch.
func TestBuildFallsBackToFaketime(t *testing.T) {
	sh, out := runArtifactBuild(t, "1.47.0", nil)
	mkfs := findCmd(t, sh, "podman", "mkfs.ext4")
	epoch := strconv.FormatInt(testEpoch, 10)
	want := []string{"unshare", "faketime", "-f", epoch, "mkfs.ext4",
		"-q", "-F", "-L", "pluto-root", "-U", imagebuilder.DefaultUUID,
		"-E", "hash_seed=" + imagebuilder.DefaultHashSeed,
		"-d", filepath.Join(out, "rootfs"), filepath.Join(out, "rootfs.img")}
	if !reflect.DeepEqual(mkfs.args, want) {
		t.Errorf("fallback mkfs args =\n  %q\nwant\n  %q", mkfs.args, want)
	}
	for _, e := range []string{"SOURCE_DATE_EPOCH=" + epoch, "FAKETIME_FMT=%s"} {
		if !contains(mkfs.env, e) {
			t.Errorf("fallback mkfs env missing %q:\n%v", e, mkfs.env)
		}
	}
}

// TestBuildKeepsNativeMkfs pins the preference: e2fsprogs >= 1.47.1 runs mkfs
// directly with SOURCE_DATE_EPOCH and never consults faketime, even when
// faketime is unavailable.
func TestBuildKeepsNativeMkfs(t *testing.T) {
	sh, out := runArtifactBuild(t, "1.47.4", fmt.Errorf("faketime must not be probed on the native path"))
	mkfs := findCmd(t, sh, "podman", "mkfs.ext4")
	epoch := strconv.FormatInt(testEpoch, 10)
	want := []string{"unshare", "mkfs.ext4",
		"-q", "-F", "-L", "pluto-root", "-U", imagebuilder.DefaultUUID,
		"-E", "hash_seed=" + imagebuilder.DefaultHashSeed,
		"-d", filepath.Join(out, "rootfs"), filepath.Join(out, "rootfs.img")}
	if !reflect.DeepEqual(mkfs.args, want) {
		t.Errorf("native mkfs args =\n  %q\nwant\n  %q", mkfs.args, want)
	}
	if !reflect.DeepEqual(mkfs.env, []string{"SOURCE_DATE_EPOCH=" + epoch}) {
		t.Errorf("native mkfs env = %q, want only SOURCE_DATE_EPOCH", mkfs.env)
	}
}

// runArtifactBuild runs Build end to end against a fake shell: the probe sees
// mkfsVersion, and faketimeErr is what the faketime probe returns (nil means
// faketime is present). It returns the recorded shell and the output dir.
func runArtifactBuild(t *testing.T, mkfsVersion string, faketimeErr error) (*fakeShell, string) {
	t.Helper()
	out := t.TempDir()
	root := t.TempDir()
	imagesDir := t.TempDir()
	write(t, filepath.Join(imagesDir, "Containerfile"), "FROM ${BASE_IMAGE}\n", 0o644)
	if err := os.MkdirAll(filepath.Join(imagesDir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(imagesDir, "files", "pluto-agent.service"), "[Unit]\n", 0o644)
	kernel := []byte("kernel-bytes")
	fcTgz := []byte("firecracker-tgz-bytes")
	pins := imagebuilder.Pins{
		Base:        imagebuilder.BasePins{Image: "ubuntu:24.04", Digest: "sha256:" + strings.Repeat("a", 64)},
		Apt:         imagebuilder.AptPins{Snapshot: "20261001T000000Z", Packages: []string{"ca-certificates", "git"}},
		Toolchain:   imagebuilder.ToolchainPins{Go: "1.27.1"},
		Firecracker: imagebuilder.FirecrackerPins{Version: "1.17.0", URL: "https://example.com/firecracker-v1.17.0-x86_64.tgz", SHA256: sha256Hex(fcTgz)},
		Kernel:      imagebuilder.KernelPins{URL: "https://example.com/vmlinux-6.18.51", SHA256: sha256Hex(kernel)},
	}
	sh := newFakeShell()
	sh.outputs["mkfs.ext4"] = "mke2fs " + mkfsVersion + " (1-Jan-2020)\n"
	sh.outputs["podman"] = "container-id"
	handler := artifactHandler()
	sh.handler = func(cmd imagebuilder.Command) error {
		if cmd.Name == "faketime" {
			return faketimeErr
		}
		return handler(cmd)
	}
	b := imagebuilder.New(pins, out, root, imagesDir)
	b.Shell = sh
	b.Fetch = fetcher{"vmlinux": kernel, "firecracker": fcTgz}.fetch
	b.DiskMB = 8
	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build (mkfs %s): %v", mkfsVersion, err)
	}
	return sh, out
}

// TestBuildRejectsOldMkfsWithoutFaketime is the last resort: an e2fsprogs that
// ignores SOURCE_DATE_EPOCH and no faketime must fail clearly, before any
// download, so the rootfs is never silently non-reproducible.
func TestBuildRejectsOldMkfsWithoutFaketime(t *testing.T) {
	out := t.TempDir()
	imagesDir := t.TempDir()
	write(t, filepath.Join(imagesDir, "Containerfile"), "x", 0o644)
	pins := imagebuilder.Pins{
		Base:        imagebuilder.BasePins{Image: "ubuntu:24.04", Digest: "sha256:" + strings.Repeat("a", 64)},
		Apt:         imagebuilder.AptPins{Snapshot: "20261001T000000Z", Packages: []string{"git"}},
		Toolchain:   imagebuilder.ToolchainPins{Go: "1.27.1"},
		Firecracker: imagebuilder.FirecrackerPins{Version: "1.17.0", URL: "https://example.com/fc.tgz", SHA256: strings.Repeat("b", 64)},
		Kernel:      imagebuilder.KernelPins{URL: "https://example.com/vmlinux", SHA256: strings.Repeat("c", 64)},
	}
	sh := newFakeShell()
	// Ubuntu 24.04's e2fsprogs: SOURCE_DATE_EPOCH is silently ignored.
	sh.outputs["mkfs.ext4"] = "mke2fs 1.47.0 (5-Feb-2023)\n\tUsing EXT2FS Library version 1.47.0\n"
	handler := artifactHandler()
	sh.handler = func(cmd imagebuilder.Command) error {
		if cmd.Name == "faketime" {
			return fmt.Errorf("exec: faketime: executable file not found in $PATH")
		}
		return handler(cmd)
	}
	b := imagebuilder.New(pins, out, t.TempDir(), imagesDir)
	b.Shell = sh
	// A fetch that fails if called: the choice must be made before any download.
	b.Fetch = func(context.Context, string) (io.ReadCloser, error) {
		t.Error("the build downloaded before choosing a time-pinning mechanism")
		return nil, fmt.Errorf("unexpected fetch")
	}
	b.DiskMB = 8

	err := b.Build(context.Background())
	if err == nil {
		t.Fatal("Build accepted an old e2fsprogs with no faketime fallback")
	}
	if !strings.Contains(err.Error(), "1.47.1") || !strings.Contains(err.Error(), "faketime") {
		t.Errorf("error = %v, want a clear 'e2fsprogs >= 1.47.1 or faketime' message", err)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func findCmd(t *testing.T, sh *fakeShell, name string, arg string) recorded {
	t.Helper()
	for _, c := range sh.all() {
		if c.name == name && contains(c.args, arg) {
			return c
		}
	}
	t.Fatalf("no %s command containing %q", name, arg)
	return recorded{}
}
