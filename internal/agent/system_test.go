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

func TestSessionUnitFileRunsUnderTmuxAndRendersEnv(t *testing.T) {
	body := sessionUnitFile("agent", "/home/dev/work/my repo",
		"/home/dev/.local/state/pluto/sessions/agent.sh", "/usr/bin:/bin",
		map[string]string{"SESS": "yes", "TOKEN": "a b"})
	for _, want := range []string{
		`WorkingDirectory="/home/dev/work/my repo"`,
		"Type=forking",
		"ExecStart=/usr/bin/tmux new-session -d -s agent",
		`-c "/home/dev/work/my repo"`,
		"/home/dev/.local/state/pluto/sessions/agent.sh",
		"Environment=PATH=/usr/bin:/bin",
		"Environment=SESS=yes",
		`Environment="TOKEN=a b"`,
		"Restart=on-failure",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("session unit missing %q:\n%s", want, body)
		}
	}
}

func TestSessionUnitName(t *testing.T) {
	if got := SessionUnit("agent"); got != "pluto-session-agent.service" {
		t.Fatalf("SessionUnit = %q, want pluto-session-agent.service", got)
	}
}

// cgroupUsage is the real reader's core: it must pull usage_usec out of
// cpu.stat and sum rbytes+wbytes across every device in io.stat.
func TestCgroupUsageSumsCPUAndIO(t *testing.T) {
	dir := t.TempDir()
	cpuStat := "usage_usec 4242\nuser_usec 4000\nsystem_usec 242\n"
	if err := os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte(cpuStat), 0o644); err != nil {
		t.Fatalf("write cpu.stat: %v", err)
	}
	ioStat := "8:0 rbytes=100 wbytes=200 rios=1 wios=2\n8:16 rbytes=300 wbytes=400 rios=3 wios=4\n"
	if err := os.WriteFile(filepath.Join(dir, "io.stat"), []byte(ioStat), 0o644); err != nil {
		t.Fatalf("write io.stat: %v", err)
	}

	got, err := cgroupUsage(dir)
	if err != nil {
		t.Fatalf("cgroupUsage: %v", err)
	}
	if got.CPUUsec != 4242 {
		t.Fatalf("CPUUsec = %d, want 4242", got.CPUUsec)
	}
	if got.IOBytes != 1000 {
		t.Fatalf("IOBytes = %d, want 1000 (the sum across devices)", got.IOBytes)
	}
}

// A missing counter file is an error, not a zero reading: the daemon must
// read that as unknown and keep the box awake.
func TestCgroupUsageFailsWithoutACounterFile(t *testing.T) {
	if _, err := cgroupUsage(t.TempDir()); err == nil {
		t.Fatal("cgroupUsage with no counters = nil error, want a failure")
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
