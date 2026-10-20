package devices_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// The trust boundary has one definition: every ssh route asks for no prompt,
// a bounded connect, and first-use host-key acceptance. A changed key still
// fails, because the option is accept-new rather than no.
func TestHostOptionsFenceEveryRoute(t *testing.T) {
	got := devices.HostOptions(7 * time.Second)
	want := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=7",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if len(got) != len(want) {
		t.Fatalf("HostOptions = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HostOptions = %q, want %q", got, want)
		}
	}
}

// The real SSH runner bounds the wait and fences the target, observed through
// the argv the fake ssh receives.
func TestSSHRunBoundsTheWaitAndFencesTheTarget(t *testing.T) {
	log := testssh.Start(t, "", "", 0)
	if err := (devices.SSH{}).Run(context.Background(), "siddhant@neptuno", "pluto", "version"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(testssh.LoggedArgs(t, log), " ")
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
	log := testssh.Start(t, "", "", 0)
	if err := (devices.SSH{}).Run(context.Background(), "host", "echo", "hello world", "it's"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(testssh.LoggedArgs(t, log), " ")
	if !strings.Contains(joined, "'hello world'") {
		t.Fatalf("spaced word not quoted: %v", joined)
	}
	if !strings.Contains(joined, `'it'\''s'`) {
		t.Fatalf("quote in word not escaped: %v", joined)
	}
}

// A failed run carries ssh's last non-empty stderr line, not the banner.
func TestSSHRunReportsTheLastStderrLine(t *testing.T) {
	testssh.Start(t, "", "banner\n\nssh: connect to host x: refused\n", 255)
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
