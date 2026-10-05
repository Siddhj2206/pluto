package devices_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// fakeSSH puts a fake ssh first on PATH: it logs each argument on its own
// line, echoes the prepared stderr, and exits with code. Tests never ssh.
func fakeSSH(t *testing.T, stderr string, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.args")
	errFile := filepath.Join(dir, "ssh.stderr")
	if err := os.WriteFile(errFile, []byte(stderr), 0o644); err != nil {
		t.Fatalf("write fake ssh stderr: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat %q 1>&2\nexit %d\n", log, errFile, code)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
