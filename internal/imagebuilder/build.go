package imagebuilder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultDiskMB is the rootfs image size when no override is given.
const DefaultDiskMB = 2048

// Builder builds the image artifact from pins. Shell and Fetch are the seams
// that keep it unit-testable; the zero value is not usable, construct with New.
type Builder struct {
	Pins   Pins
	Out    string // artifact directory
	Root   string // repository root, for `go build`
	Images string // directory holding the Containerfile and files/
	DiskMB int

	Shell Shell
	Fetch func(ctx context.Context, url string) (io.ReadCloser, error)
	Logf  func(format string, args ...any)
}

// New returns a Builder with production defaults.
func New(pins Pins, out, root, images string) *Builder {
	return &Builder{
		Pins:   pins,
		Out:    out,
		Root:   root,
		Images: images,
		DiskMB: DefaultDiskMB,
		Shell:  ExecShell{},
		Fetch:  HTTPFetch,
		Logf:   log.Printf,
	}
}

// Build produces Out/{vmlinuz,rootfs.img,manifest.json} and the working
// directories import and boot expect. It is behavior-compatible with the
// original images/build.sh: same podman rootfs, same checksum gates, same
// artifact layout.
func (b *Builder) Build(ctx context.Context) error {
	if b.DiskMB <= 0 {
		return fmt.Errorf("image builder: disk size must be positive, got %d MiB", b.DiskMB)
	}
	timePinning, err := chooseMkfsTimePinning(ctx, b.Shell)
	if err != nil {
		return err
	}
	epoch, err := b.Pins.Apt.Epoch()
	if err != nil {
		return fmt.Errorf("image builder: %w", err)
	}
	for _, dir := range []string{
		b.Out,
		filepath.Join(b.Out, "cache"),
		filepath.Join(b.Out, "bin"),
		filepath.Join(b.Out, "context"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("image builder: create %s: %w", dir, err)
		}
	}

	b.logf("==> kernel")
	kernelCache := filepath.Join(b.Out, "cache", "vmlinuz")
	if _, err := b.fetchVerified(ctx, b.Pins.Kernel.URL, b.Pins.Kernel.SHA256, kernelCache, "kernel"); err != nil {
		return err
	}
	if err := copyFile(kernelCache, filepath.Join(b.Out, "vmlinuz"), 0o644); err != nil {
		return err
	}

	b.logf("==> firecracker v%s", b.Pins.Firecracker.Version)
	if err := b.ensureFirecracker(ctx, filepath.Join(b.Out, "cache", "firecracker")); err != nil {
		return err
	}

	b.logf("==> go helpers")
	for _, helper := range []struct{ name, pkg string }{
		{"pluto-agent", "./cmd/pluto-agent"},
		{"pluto-vsock", "./cmd/pluto-vsock"},
	} {
		bin := filepath.Join(b.Out, "bin", helper.name)
		cmd := Command{
			Name: "go",
			Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", bin, helper.pkg},
			Dir:  b.Root,
			Env: []string{
				"CGO_ENABLED=0",
				// Force the pinned toolchain rather than whatever `go` is on
				// PATH, so the guest binaries (and rootfs.img) are the same
				// bytes on every host. Go downloads the toolchain when absent.
				"GOTOOLCHAIN=go" + b.Pins.Toolchain.Go,
			},
		}
		if err := b.Shell.Run(ctx, cmd); err != nil {
			return fmt.Errorf("image builder: go build %s: %w", helper.name, err)
		}
	}
	if err := copyFile(filepath.Join(b.Out, "bin", "pluto-agent"), filepath.Join(b.Out, "context", "pluto-agent"), 0o755); err != nil {
		return err
	}
	if err := copyFile(filepath.Join(b.Images, "files", "pluto-agent.service"), filepath.Join(b.Out, "context", "pluto-agent.service"), 0o644); err != nil {
		return err
	}

	b.logf("==> rootfs (podman build)")
	if err := b.Shell.Run(ctx, Command{Name: "podman", Args: b.podmanBuildArgs(epoch)}); err != nil {
		return fmt.Errorf("image builder: podman build: %w", err)
	}

	if err := b.podmanExport(ctx); err != nil {
		return err
	}
	tarPath := filepath.Join(b.Out, "rootfs.tar")
	if err := Assemble(ctx, b.Shell, AssembleOptions{
		Tar:             tarPath,
		Dir:             b.Out,
		Agent:           filepath.Join(b.Out, "bin", "pluto-agent"),
		Image:           filepath.Join(b.Out, "rootfs.img"),
		DiskMB:          b.DiskMB,
		SourceDateEpoch: epoch,
		TimePinning:     timePinning,
	}); err != nil {
		return err
	}
	os.Remove(tarPath)

	b.logf("==> manifest")
	return b.writeManifest(epoch)
}

// podmanExport creates a container from the built image, exports its filesystem
// to Out/rootfs.tar, and removes the container.
func (b *Builder) podmanExport(ctx context.Context) error {
	out, err := b.Shell.Output(ctx, Command{Name: "podman", Args: []string{"create", imageTag}})
	if err != nil {
		return fmt.Errorf("image builder: podman create: %w", err)
	}
	cid := strings.TrimSpace(string(out))
	if cid == "" {
		return fmt.Errorf("image builder: podman create returned no container id")
	}
	tarPath := filepath.Join(b.Out, "rootfs.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		return fmt.Errorf("image builder: create %s: %w", tarPath, err)
	}
	exportErr := b.Shell.Run(ctx, Command{Name: "podman", Args: []string{"export", cid}, Stdout: f})
	closeErr := f.Close()
	rmErr := b.Shell.Run(ctx, Command{Name: "podman", Args: []string{"rm", "-f", cid}})
	if exportErr != nil {
		return fmt.Errorf("image builder: podman export: %w", exportErr)
	}
	if closeErr != nil {
		return fmt.Errorf("image builder: write %s: %w", tarPath, closeErr)
	}
	if rmErr != nil {
		return fmt.Errorf("image builder: podman rm: %w", rmErr)
	}
	return nil
}

// imageTag is the working tag podman builds and exports under.
const imageTag = "pluto-m3-base"

// podmanBuildArgs builds the image with the pins as build args, so the base
// image, apt snapshot, package set, and SOURCE_DATE_EPOCH have one source.
func (b *Builder) podmanBuildArgs(epoch int64) []string {
	return []string{
		"build", "-q",
		"-t", imageTag,
		"-f", filepath.Join(b.Images, "Containerfile"),
		"--build-arg", "BASE_IMAGE=" + b.Pins.Base.String(),
		"--build-arg", "APT_SNAPSHOT=" + b.Pins.Apt.Snapshot,
		"--build-arg", "APT_PACKAGES=" + strings.Join(b.Pins.Apt.Packages, " "),
		"--build-arg", "SOURCE_DATE_EPOCH=" + strconv.FormatInt(epoch, 10),
		filepath.Join(b.Out, "context"),
	}
}

// ensureFirecracker downloads and verifies the pinned tarball, then extracts
// the release binary to dest. A verified tarball and an existing binary skip
// the work; a version bump changes the tarball name and forces a re-extract.
func (b *Builder) ensureFirecracker(ctx context.Context, dest string) error {
	tgz := filepath.Join(b.Out, "cache", "firecracker-"+b.Pins.Firecracker.Version+".tgz")
	changed, err := b.fetchVerified(ctx, b.Pins.Firecracker.URL, b.Pins.Firecracker.SHA256, tgz, "firecracker")
	if err != nil {
		return err
	}
	if !changed {
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
	}
	tmp, err := os.MkdirTemp(filepath.Join(b.Out, "cache"), "extract-*")
	if err != nil {
		return fmt.Errorf("image builder: create extract dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := b.Shell.Run(ctx, Command{Name: "tar", Args: []string{"-xzf", tgz, "-C", tmp}}); err != nil {
		return fmt.Errorf("image builder: extract firecracker: %w", err)
	}
	found, err := findFirecrackerBinary(tmp)
	if err != nil {
		return err
	}
	return copyFile(found, dest, 0o755)
}

// fetchVerified downloads url to dest unless dest already matches wantSHA,
// returning whether it fetched. A checksum mismatch fails before the artifact
// can record a bad hash.
func (b *Builder) fetchVerified(ctx context.Context, url, wantSHA, dest, label string) (bool, error) {
	if got, err := hashFile(dest); err == nil && got == wantSHA {
		return false, nil
	}
	rc, err := b.Fetch(ctx, url)
	if err != nil {
		return false, fmt.Errorf("image builder: download %s: %w", label, err)
	}
	defer rc.Close()
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return false, fmt.Errorf("image builder: create %s: %w", tmp, err)
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), rc)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("image builder: download %s: %w", label, copyErr)
	}
	if closeErr != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("image builder: write %s: %w", tmp, closeErr)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		os.Remove(tmp)
		return false, fmt.Errorf(
			"image builder: %s checksum mismatch:\n  want: %s\n  got:  %s\n  delete %s and rebuild; update the pin only if the artifact moved intentionally",
			label, wantSHA, got, dest)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return false, fmt.Errorf("image builder: install %s: %w", dest, err)
	}
	return true, nil
}

// writeManifest hashes the finished artifact and writes manifest.json. The
// only temporal field is source_date_epoch, derived from the apt snapshot pin,
// so two builds on different days produce identical manifest bytes.
func (b *Builder) writeManifest(epoch int64) error {
	rootfs := filepath.Join(b.Out, "rootfs.img")
	kernelSHA, err := hashFile(filepath.Join(b.Out, "vmlinuz"))
	if err != nil {
		return err
	}
	fcSHA, err := hashFile(filepath.Join(b.Out, "cache", "firecracker"))
	if err != nil {
		return err
	}
	rootSHA, err := hashFile(rootfs)
	if err != nil {
		return err
	}
	agentSHA, err := hashFile(filepath.Join(b.Out, "bin", "pluto-agent"))
	if err != nil {
		return err
	}
	info, err := os.Stat(rootfs)
	if err != nil {
		return err
	}
	m := Manifest{
		Schema:          2,
		SourceDateEpoch: epoch,
		Kernel:          KernelManifest{Name: b.Pins.Kernel.Name(), URL: b.Pins.Kernel.URL, SHA256: kernelSHA},
		Firecracker:     FirecrackerManifest{Version: b.Pins.Firecracker.Version, URL: b.Pins.Firecracker.URL, SHA256: fcSHA},
		Rootfs: RootfsManifest{
			File:        "rootfs.img",
			Base:        b.Pins.Base.String(),
			AptSnapshot: b.Pins.Apt.Snapshot,
			SHA256:      rootSHA,
			SizeBytes:   info.Size(),
			DiskMB:      b.DiskMB,
		},
		Agent: AgentManifest{SHA256: agentSHA},
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("image builder: marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.Out, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("image builder: write manifest: %w", err)
	}
	return nil
}

func (b *Builder) logf(format string, args ...any) {
	if b.Logf != nil {
		b.Logf(format, args...)
	}
}

// HTTPFetch is the production downloader. It retries transient failures a few
// times, mirroring the original build script's `curl --retry 3`.
func HTTPFetch(ctx context.Context, url string) (io.ReadCloser, error) {
	const attempts = 3
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				return resp.Body, nil
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: %s", url, resp.Status)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return nil, lastErr
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("image builder: open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("image builder: create %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("image builder: create %s: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("image builder: copy %s: %w", dst, err)
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// findFirecrackerBinary returns the release binary in an extracted tarball:
// firecracker-v<version>-<arch>, never the .debug build.
func findFirecrackerBinary(dir string) (string, error) {
	var found string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "firecracker-v") && !strings.HasSuffix(name, ".debug") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("image builder: scan %s: %w", dir, err)
	}
	if found == "" {
		return "", fmt.Errorf("image builder: firecracker binary not found in tarball")
	}
	return found, nil
}
