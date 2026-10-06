package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/envcache"
)

func requireDiskTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"mkfs.ext4", "debugfs", "resize2fs", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable: %v", tool, err)
		}
	}
}

// mkExt4 builds a small ext4 image from a directory tree.
func mkExt4(t *testing.T, tree, image string, sizeMiB int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatalf("mkdir image dir: %v", err)
	}
	f, err := os.Create(image)
	if err != nil {
		t.Fatalf("create image: %v", err)
	}
	if err := f.Truncate(int64(sizeMiB) << 20); err != nil {
		t.Fatalf("truncate image: %v", err)
	}
	f.Close()
	if out, err := exec.Command("mkfs.ext4", "-q", "-F", "-d", tree, image).CombinedOutput(); err != nil {
		t.Fatalf("mkfs.ext4: %v (%s)", err, out)
	}
}

func writeTree(t *testing.T, tree string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(tree, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func debugfsCat(t *testing.T, image, path string) (string, bool) {
	t.Helper()
	out, err := exec.Command("debugfs", "-R", "cat "+debugfsQuote(path), image).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func debugfsExists(t *testing.T, image, path string) bool {
	t.Helper()
	out, err := exec.Command("debugfs", "-R", "stat "+debugfsQuote(path), image).CombinedOutput()
	if err != nil {
		return false
	}
	return !strings.Contains(string(out), "File not found")
}

// scrubEnvironment removes another box's worktree, agent state, ssh identity,
// and any credential file, while leaving installed tools and unrelated files.
func TestScrubEnvironmentRemovesPerBoxState(t *testing.T) {
	requireDiskTools(t)
	dir := t.TempDir()
	image := filepath.Join(dir, "layer.img")
	writeTree(t, dir, map[string]string{
		"tree/usr/bin/keep":                                   "tool",
		"tree/home/dev/keep.txt":                              "user file",
		"tree/home/dev/work/proj/secret.txt":                  "branch work",
		"tree/home/dev/work/proj/sub/deep.txt":                "deep",
		"tree/home/dev/.local/state/pluto/status.json":        `{"provision":{"state":"done"}}`,
		"tree/home/dev/.local/state/pluto/logs/provision.log": "log",
		"tree/home/dev/.ssh/authorized_keys":                  "ssh-rsa AAAA box",
		"tree/home/dev/.git-credentials":                      "https://token@example.test",
	})
	mkExt4(t, filepath.Join(dir, "tree"), image, 8)

	if err := scrubEnvironment(image); err != nil {
		t.Fatalf("scrubEnvironment: %v", err)
	}
	for _, kept := range []string{"/usr/bin/keep", "/home/dev/keep.txt"} {
		if _, ok := debugfsCat(t, image, kept); !ok {
			t.Fatalf("%s was scrubbed, want kept", kept)
		}
	}
	for _, gone := range []string{
		"/home/dev/work/proj/secret.txt",
		"/home/dev/work/proj/sub/deep.txt",
		"/home/dev/work/proj/sub",
		"/home/dev/work/proj",
		"/home/dev/work",
		"/home/dev/.local/state/pluto/status.json",
		"/home/dev/.local/state/pluto/logs/provision.log",
		"/home/dev/.local/state/pluto",
		"/home/dev/.ssh/authorized_keys",
		"/home/dev/.git-credentials",
	} {
		if debugfsExists(t, image, gone) {
			t.Fatalf("%s survived the scrub", gone)
		}
	}
}

// prepareLayerDisk clones a layer, injects the box's own ssh key, and records
// that provision already ran, so a cache hit does not re-run setup.
func TestPrepareLayerDiskMarksProvisionDoneAndInjectsKey(t *testing.T) {
	requireDiskTools(t)
	dir := t.TempDir()
	layerDir := filepath.Join(dir, "layer")
	writeTree(t, dir, map[string]string{
		"tree/home/dev/.local/state/pluto/status.json": `{"worktree":"/home/dev/work/other"}`,
	})
	mkExt4(t, filepath.Join(dir, "tree"), filepath.Join(layerDir, "rootfs.img"), 8)

	boxDir := filepath.Join(dir, "box")
	if err := prepareLayerDisk(boxDir, layerDir, 16); err != nil {
		t.Fatalf("prepareLayerDisk: %v", err)
	}
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	status, ok := debugfsCat(t, disk, "/home/dev/.local/state/pluto/status.json")
	if !ok {
		t.Fatal("agent status was not written")
	}
	if !strings.Contains(status, `"state":"done"`) || strings.Contains(status, "worktree") {
		t.Fatalf("status = %q, want a minimal provision-done record", status)
	}
	pub, err := os.ReadFile(filepath.Join(boxDir, "id.pub"))
	if err != nil {
		t.Fatalf("read id.pub: %v", err)
	}
	auth, ok := debugfsCat(t, disk, "/home/dev/.ssh/authorized_keys")
	if !ok || !bytes.Equal(bytes.TrimSpace([]byte(auth)), bytes.TrimSpace(pub)) {
		t.Fatalf("authorized_keys = %q, want the box key %q", auth, pub)
	}
	info, err := os.Stat(disk)
	if err != nil {
		t.Fatalf("stat disk: %v", err)
	}
	if info.Size() != int64(16)<<20 {
		t.Fatalf("disk size = %d, want the grown 16 MiB", info.Size())
	}
	// The agent runs as uid 1000 and must own the state directory it writes.
	stat, err := exec.Command("debugfs", "-R", "stat /home/dev/.local/state/pluto", disk).Output()
	if err != nil {
		t.Fatalf("stat pluto dir: %v", err)
	}
	if !strings.Contains(string(stat), "Type: directory") || !strings.Contains(string(stat), "User:  1000") {
		t.Fatalf("pluto state dir is not a dev-owned directory:\n%s", stat)
	}
}

// Two boxes cloning the same layer get independent writable disks: one box's
// writes never reach the layer or another box.
func TestLayerCloneIsAnIndependentWritableDisk(t *testing.T) {
	requireDiskTools(t)
	dir := t.TempDir()
	layer := filepath.Join(dir, "layer.img")
	if err := os.WriteFile(layer, []byte("layer-original"), 0o644); err != nil {
		t.Fatalf("write layer: %v", err)
	}
	a := filepath.Join(dir, "a", "rootfs.img")
	b := filepath.Join(dir, "b", "rootfs.img")
	if err := createDisk(a, layer, 0, func(string, int) error { return nil }); err != nil {
		t.Fatalf("createDisk a: %v", err)
	}
	if err := createDisk(b, layer, 0, func(string, int) error { return nil }); err != nil {
		t.Fatalf("createDisk b: %v", err)
	}
	if err := os.WriteFile(a, []byte("box-a-write"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if data, _ := os.ReadFile(b); string(data) != "layer-original" {
		t.Fatalf("b observed a's write: %q", data)
	}
	if data, _ := os.ReadFile(layer); string(data) != "layer-original" {
		t.Fatalf("layer was mutated: %q", data)
	}
}

// A published layer is immutable (envcache.Publish chmods it 0444) and
// CloneFile inherits the source mode, so a naive clone is read-only and the
// host-side debugfs writes that inject the key and mark provision done silently
// no-op. This drives the real Publish -> CloneFile -> createDisk -> debugfs
// path with no stubs, proving the clone is writable and both writes land.
func TestPrepareLayerDiskClonesImmutableLayerWritable(t *testing.T) {
	requireDiskTools(t)
	dir := t.TempDir()
	cache := envcache.Cache{Root: filepath.Join(dir, "cache")}
	source := filepath.Join(dir, "source.img")
	writeTree(t, dir, map[string]string{
		"tree/home/dev/provision-ran":                  "once\n",
		"tree/home/dev/.local/state/pluto/status.json": `{"worktree":"/home/dev/work/other"}`,
	})
	mkExt4(t, filepath.Join(dir, "tree"), source, 8)
	key, err := envcache.Key(envcache.Inputs{
		Project: "https://example.test/acme/app.git",
		Setup:   "setup",
		Image:   "image",
		Trust:   envcache.Trusted,
	})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if err := cache.Publish(key, source, nil); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	layerDir, err := cache.LayerDir(key)
	if err != nil {
		t.Fatalf("LayerDir: %v", err)
	}
	layerImg := filepath.Join(layerDir, "rootfs.img")
	if info, err := os.Stat(layerImg); err != nil {
		t.Fatalf("stat published layer: %v", err)
	} else if info.Mode().Perm()&0o200 != 0 {
		t.Fatalf("test setup: published layer is writable (%v), want immutable", info.Mode())
	}

	boxDir := filepath.Join(dir, "box")
	if err := prepareLayerDisk(boxDir, layerDir, 16); err != nil {
		t.Fatalf("prepareLayerDisk: %v", err)
	}
	disk := filepath.Join(boxDir, "disk", "rootfs.img")
	info, err := os.Stat(disk)
	if err != nil {
		t.Fatalf("stat cloned disk: %v", err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Fatalf("cloned disk is read-only (%v); the debugfs key and marker writes would silently no-op", info.Mode())
	}
	pub, err := os.ReadFile(filepath.Join(boxDir, "id.pub"))
	if err != nil {
		t.Fatalf("read id.pub: %v", err)
	}
	auth, ok := debugfsCat(t, disk, "/home/dev/.ssh/authorized_keys")
	if !ok || !bytes.Equal(bytes.TrimSpace([]byte(auth)), bytes.TrimSpace(pub)) {
		t.Fatalf("authorized_keys = %q, want the box key %q", auth, pub)
	}
	status, ok := debugfsCat(t, disk, "/home/dev/.local/state/pluto/status.json")
	if !ok || !strings.Contains(status, `"state":"done"`) {
		t.Fatalf("provision-done status = %q, want the minimal done record written", status)
	}
}

// debugfs can print a write failure and still exit 0, so runDebugfs must fail
// on an unwritable image from the output rather than trust the exit code.
func TestRunDebugfsDetectsUnwritableImage(t *testing.T) {
	requireDiskTools(t)
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0444 image, so debugfs would not fail")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tree"), 0o755); err != nil {
		t.Fatalf("mkdir tree: %v", err)
	}
	image := filepath.Join(dir, "ro.img")
	mkExt4(t, filepath.Join(dir, "tree"), image, 8)
	if err := os.Chmod(image, 0o444); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	if err := runDebugfs(dir, image, "mkdir /home/dev/ro-test"); err == nil {
		t.Fatal("runDebugfs accepted a write debugfs could not perform")
	}
}
