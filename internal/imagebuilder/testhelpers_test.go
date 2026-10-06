package imagebuilder_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/Siddhj2206/pluto/internal/imagebuilder"
)

// recorded is one command the fake shell saw.
type recorded struct {
	name string
	args []string
	dir  string
	env  []string
}

// fakeShell is the Shell seam for unit tests: it records commands and lets a
// test hook simulate their filesystem effect, so no podman, tar, or mkfs runs.
type fakeShell struct {
	mu      sync.Mutex
	cmds    []recorded
	outputs map[string]string
	handler func(imagebuilder.Command) error
}

func newFakeShell() *fakeShell {
	return &fakeShell{outputs: map[string]string{
		// A host that can honor SOURCE_DATE_EPOCH, so Build's probe passes by
		// default; a test that wants the rejection overrides this.
		"mkfs.ext4": "mke2fs 1.47.4 (6-Mar-2025)\n\tUsing EXT2FS Library version 1.47.4\n",
	}}
}

func (f *fakeShell) Run(ctx context.Context, cmd imagebuilder.Command) error {
	f.mu.Lock()
	f.cmds = append(f.cmds, recorded{name: cmd.Name, args: append([]string(nil), cmd.Args...), dir: cmd.Dir, env: append([]string(nil), cmd.Env...)})
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		return h(cmd)
	}
	return nil
}

func (f *fakeShell) Output(ctx context.Context, cmd imagebuilder.Command) ([]byte, error) {
	if err := f.Run(ctx, cmd); err != nil {
		return nil, err
	}
	data := []byte(f.outputs[cmd.Name])
	if cmd.Stderr != nil {
		// Mirror ExecShell's passthrough: a command that writes version text
		// to stderr (mkfs.ext4 -V) lands the fake output there too.
		_, _ = cmd.Stderr.Write(data)
	}
	return data, nil
}

func (f *fakeShell) all() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.cmds...)
}

// artifactHandler is the fake shell behavior that lets Build run end to end
// without podman, tar, or mkfs: it materializes each command's filesystem
// effect so the manifest can hash real files. Tests that need the probe to
// fail wrap it and intercept the command they care about.
func artifactHandler() func(imagebuilder.Command) error {
	return func(cmd imagebuilder.Command) error {
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
}

// write creates a file, failing the test on error.
func write(t testingT, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type testingT interface {
	Helper()
	Fatalf(format string, args ...any)
}
