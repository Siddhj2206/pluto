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
	sh.handler = func(cmd imagebuilder.Command) error {
		switch {
		case cmd.Name == "go":
			// go build [-trimpath] -o <bin> <pkg>
			var out string
			for i, a := range cmd.Args {
				if a == "-o" && i+1 < len(cmd.Args) {
					out = cmd.Args[i+1]
				}
			}
			return os.WriteFile(out, []byte(cmd.Args[len(cmd.Args)-1]+"-bytes"), 0o755)
		case cmd.Name == "tar":
			dir := cmd.Args[len(cmd.Args)-1]
			return os.WriteFile(filepath.Join(dir, "firecracker-v1.17.0-x86_64"), []byte("fc-bytes"), 0o755)
		case cmd.Name == "podman" && contains(cmd.Args, "mkfs.ext4"):
			return os.WriteFile(cmd.Args[len(cmd.Args)-1], []byte("rootfs-bytes"), 0o644)
		case cmd.Name == "podman" && len(cmd.Args) > 1 && cmd.Args[1] == "export":
			if cmd.Stdout != nil {
				_, err := cmd.Stdout.Write([]byte("exported-tar"))
				return err
			}
		}
		return nil
	}

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
