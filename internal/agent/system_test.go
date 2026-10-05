package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestHookUnitFile(t *testing.T) {
	body := hookUnitFile("provision", "/home/dev/work/x",
		"/home/dev/.local/state/pluto/hooks/provision.sh", "/usr/bin:/bin",
		"/home/dev/.local/state/pluto/logs/provision.log",
		contract.Exec{
			Command: contract.ShellCommand("make"),
			Dir:     "sub",
			Env:     map[string]string{"FOO": "bar baz"},
			Timeout: 10 * time.Minute,
		})
	for _, want := range []string{
		"Type=oneshot",
		"KillMode=control-group",
		"TimeoutStartSec=600",
		"WorkingDirectory=/home/dev/work/x/sub",
		"Environment=PATH=/usr/bin:/bin",
		`Environment="FOO=bar baz"`,
		"ExecStart=/home/dev/.local/state/pluto/hooks/provision.sh",
		"StandardOutput=append:/home/dev/.local/state/pluto/logs/provision.log",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hook unit missing %q:\n%s", want, body)
		}
	}
}

func TestServiceUnitFileQuotesSpacesAndRendersEnv(t *testing.T) {
	body := serviceUnitFile("web", "/home/dev/work/my repo",
		"/home/dev/.local/state/pluto/services/web.sh", "/usr/bin:/bin",
		map[string]string{"PORT": "3000"})
	if !strings.Contains(body, `WorkingDirectory="/home/dev/work/my repo"`) {
		t.Fatalf("service unit did not quote the worktree:\n%s", body)
	}
	if !strings.Contains(body, "Restart=on-failure") {
		t.Fatalf("service unit is not supervised:\n%s", body)
	}
	if !strings.Contains(body, "Environment=PORT=3000") {
		t.Fatalf("service unit did not render the service env:\n%s", body)
	}
}

func TestHookTimeoutMarksPhaseFailed(t *testing.T) {
	sys := newFakeSystem()
	sys.hookErr["wake"] = fmt.Errorf("%w after 5s", ErrTimeout)
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[wake]\ncommand = \"sleep 60\"\ntimeout = \"5s\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "wake failed", func() bool { return ag.Status().Wake.State == state.PhaseFailed })
	waitIdle(t, ag)
	if got := ag.Status().Wake.Error; !strings.Contains(got, "timed out") {
		t.Fatalf("wake error = %q, want a timeout", got)
	}
}

func TestHasCheckoutRecognizesARealRepo(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "pluto@test")
	git("config", "user.name", "pluto")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git("add", "f")
	git("commit", "-qm", "init")

	if !(Systemd{}).HasCheckout(dir) {
		t.Fatal("a committed repo should count as a usable checkout")
	}
}

func TestHasCheckoutRejectsEmptyAndUnbornRepos(t *testing.T) {
	if (Systemd{}).HasCheckout(t.TempDir()) {
		t.Fatal("an empty directory is not a checkout")
	}

	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	if (Systemd{}).HasCheckout(dir) {
		t.Fatal("a repo with no commit has no resolvable HEAD; it must not be adopted")
	}
}

func TestSetRemoteAddsUpdatesAndKeepsItWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	origin := func() string {
		t.Helper()
		out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
		if err != nil {
			t.Fatalf("get-url origin: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	// A repo with no origin gets one.
	if err := (Systemd{}).SetRemote(dir, "https://example.com/acme/app.git"); err != nil {
		t.Fatalf("SetRemote add: %v", err)
	}
	if got := origin(); got != "https://example.com/acme/app.git" {
		t.Fatalf("origin = %q, want the added url", got)
	}

	// An existing origin is updated in place.
	if err := (Systemd{}).SetRemote(dir, "git@example.com:acme/app.git"); err != nil {
		t.Fatalf("SetRemote update: %v", err)
	}
	if got := origin(); got != "git@example.com:acme/app.git" {
		t.Fatalf("origin = %q, want the updated url", got)
	}

	// Re-applying the same url is a no-op that still succeeds.
	if err := (Systemd{}).SetRemote(dir, "git@example.com:acme/app.git"); err != nil {
		t.Fatalf("SetRemote idempotent: %v", err)
	}
	if got := origin(); got != "git@example.com:acme/app.git" {
		t.Fatalf("origin = %q after re-apply, want it unchanged", got)
	}
}
