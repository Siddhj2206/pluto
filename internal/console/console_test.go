package console_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/console"
)

// IsTerminal decides whether to allocate a remote tty (`ssh -t`) and whether
// to prompt on stdin. A regular file is not a terminal on any platform, which
// is the observable client behavior this seam guards.
func TestIsTerminalIsFalseForARegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-tty")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if console.IsTerminal(f) {
		t.Fatalf("IsTerminal(%s) = true, want false", path)
	}
}

// A pipe is not a terminal either; this is the shape of piped stdin in CI and
// in a redirected shell.
func TestIsTerminalIsFalseForAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()
	if console.IsTerminal(r) {
		t.Fatal("IsTerminal(pipe) = true, want false")
	}
}
