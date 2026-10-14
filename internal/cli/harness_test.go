package cli_test

import (
	"bytes"
	"os/exec"
	"testing"

	"github.com/Siddhj2206/pluto/internal/cli"
)

// gitRepo makes a temporary git checkout on branch main, skipping when git is
// unavailable.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "HEAD", "refs/heads/main").CombinedOutput(); err != nil {
		t.Fatalf("set branch: %v (%s)", err, out)
	}
	return dir
}

// runCLI invokes the real CLI entry point in-process and captures its streams.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = cli.Run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}
