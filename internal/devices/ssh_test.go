package devices_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// fakeSSH compiles the shared Go fake ssh first on PATH: it logs each
// argument on its own line, writes the prepared stderr, and exits with code.
// A Go program rather than a shell script so `go test ./internal/devices`
// also runs on Windows, where the client ships the same SSH path.
func fakeSSH(t *testing.T, stderr string, code int) string {
	t.Helper()
	dir := t.TempDir()
	name := "ssh"
	if runtime.GOOS == "windows" {
		name = "ssh.exe"
	}
	bin := filepath.Join(dir, name)
	// A prebuilt fake can be supplied with PLUTO_FAKE_SSH_BINARY, which lets
	// the tests run under an emulator (the Wine rehearsal in
	// scripts/windows-client-wine-smoke.sh) where no Go toolchain exists.
	if override := os.Getenv("PLUTO_FAKE_SSH_BINARY"); override != "" {
		bin = override
	} else {
		build := exec.Command("go", "build", "-o", bin, "../remotecli/testdata/fakessh")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fake ssh: %v\n%s", err, output)
		}
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "ssh.args")
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_SSH_STDERR", stderr)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(code))
	return log
}

func loggedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// The real SSH runner bounds the wait and fences the target, observed through
// the argv the fake ssh receives.
func TestSSHRunBoundsTheWaitAndFencesTheTarget(t *testing.T) {
	log := fakeSSH(t, "", 0)
	if err := (devices.SSH{}).Run(context.Background(), "siddhant@neptuno", "pluto", "version"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(loggedArgs(t, log), " ")
	for _, want := range []string{
		"BatchMode=yes",
		"ConnectTimeout=5",
		"-- siddhant@neptuno",
		"pluto version",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh args missing %q: %v", want, joined)
		}
	}
}

func TestSSHRunQuotesRemoteWords(t *testing.T) {
	log := fakeSSH(t, "", 0)
	if err := (devices.SSH{}).Run(context.Background(), "host", "echo", "hello world", "it's"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(loggedArgs(t, log), " ")
	if !strings.Contains(joined, "'hello world'") {
		t.Fatalf("spaced word not quoted: %v", joined)
	}
	if !strings.Contains(joined, `'it'\''s'`) {
		t.Fatalf("quote in word not escaped: %v", joined)
	}
}

// A failed run carries ssh's last non-empty stderr line, not the banner.
func TestSSHRunReportsTheLastStderrLine(t *testing.T) {
	fakeSSH(t, "banner\n\nssh: connect to host x: refused\n", 255)
	err := (devices.SSH{}).Run(context.Background(), "x", "pluto", "version")
	if err == nil {
		t.Fatal("Run should fail")
	}
	if !strings.Contains(err.Error(), "ssh: connect to host x: refused") {
		t.Fatalf("error = %q, want ssh's last complaint", err)
	}
}

// Verify probes the target with `pluto version` through the runner seam.
func TestVerifyProbesPlutoVersion(t *testing.T) {
	runner := &fakeRunner{}
	if err := devices.Verify(context.Background(), runner, "siddhant@neptuno"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if runner.target != "siddhant@neptuno" || strings.Join(runner.argv, " ") != "pluto version" {
		t.Fatalf("probe = %s %v, want 'pluto version' on the target", runner.target, runner.argv)
	}
}

func TestVerifyWrapsAFailedProbe(t *testing.T) {
	runner := &fakeRunner{err: errors.New("connection refused")}
	err := devices.Verify(context.Background(), runner, "siddhant@neptuno")
	if err == nil || !strings.Contains(err.Error(), "did not answer") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("Verify error = %v, want the target and the probe failure", err)
	}
}
