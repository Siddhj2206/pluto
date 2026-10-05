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

// gitDir initialises a repo with no commits and returns its path; the tests
// below only touch refs and config, so a checkout is not needed.
func gitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v (%s)", err, out)
	}
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func gitConfigGet(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func TestMirrorRemotesAddsFetchPushAndRefspec(t *testing.T) {
	dir := gitDir(t)
	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "fork", Fetch: "git@example.com:me/app.git", Push: []string{"ssh://git@example.com/me/app.git"}},
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
	}

	if err := (Systemd{}).MirrorRemotes(dir, remotes); err != nil {
		t.Fatalf("MirrorRemotes: %v", err)
	}
	if got := strings.Fields(gitRun(t, dir, "remote")); strings.Join(got, ",") != "fork,origin,upstream" {
		t.Fatalf("remotes = %v, want fork, origin, upstream", got)
	}
	if got := gitRun(t, dir, "remote", "get-url", "fork"); got != "git@example.com:me/app.git" {
		t.Fatalf("fork fetch = %q, want the host URL", got)
	}
	if got := gitRun(t, dir, "config", "--get", "remote.fork.pushurl"); got != "ssh://git@example.com/me/app.git" {
		t.Fatalf("fork pushurl = %q, want the distinct push URL", got)
	}
	if got := gitRun(t, dir, "config", "--get", "remote.origin.fetch"); got != "+refs/heads/*:refs/remotes/origin/*" {
		t.Fatalf("origin fetch = %q, want the default refspec", got)
	}

	// Re-applying the same list is idempotent: no duplicate push URLs.
	if err := (Systemd{}).MirrorRemotes(dir, remotes); err != nil {
		t.Fatalf("MirrorRemotes (again): %v", err)
	}
	if got := gitRun(t, dir, "config", "--get-all", "remote.fork.pushurl"); got != "ssh://git@example.com/me/app.git" {
		t.Fatalf("fork pushurl after re-apply = %q, want a single value", got)
	}
	if got := strings.Fields(gitRun(t, dir, "remote")); strings.Join(got, ",") != "fork,origin,upstream" {
		t.Fatalf("remotes after re-apply = %v, want no duplicate", got)
	}
}

// TestMirrorRemotesLeavesTheBoxsOwnRemotesAlone guards adoption: a remote the
// box carries that the host list does not mention is not clobbered.
func TestMirrorRemotesLeavesTheBoxsOwnRemotesAlone(t *testing.T) {
	dir := gitDir(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "boxonly", "https://example.com/boxonly/app.git").CombinedOutput(); err != nil {
		t.Fatalf("seed boxonly: %v (%s)", err, out)
	}

	remotes := []state.Remote{{Name: "origin", Fetch: "https://example.com/acme/app.git"}}
	if err := (Systemd{}).MirrorRemotes(dir, remotes); err != nil {
		t.Fatalf("MirrorRemotes: %v", err)
	}
	if got := gitRun(t, dir, "remote", "get-url", "boxonly"); got != "https://example.com/boxonly/app.git" {
		t.Fatalf("boxonly fetch = %q, want the box's own remote untouched", got)
	}
}

func TestTrackBranchSetsUpstreamForOrigin(t *testing.T) {
	dir := gitDir(t)
	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "fork", Fetch: "https://example.com/me/app.git"},
	}
	if err := (Systemd{}).TrackBranch(dir, "main", remotes); err != nil {
		t.Fatalf("TrackBranch: %v", err)
	}
	if got := gitRun(t, dir, "config", "--get", "branch.main.remote"); got != "origin" {
		t.Fatalf("branch.main.remote = %q, want origin", got)
	}
	if got := gitRun(t, dir, "config", "--get", "branch.main.merge"); got != "refs/heads/main" {
		t.Fatalf("branch.main.merge = %q, want refs/heads/main", got)
	}
	if got := gitRun(t, dir, "config", "--get", "push.default"); got != "current" {
		t.Fatalf("push.default = %q, want current", got)
	}
}

func TestTrackBranchUsesTheSoleRemote(t *testing.T) {
	dir := gitDir(t)
	if err := (Systemd{}).TrackBranch(dir, "main", []state.Remote{{Name: "fork", Fetch: "https://example.com/me/app.git"}}); err != nil {
		t.Fatalf("TrackBranch: %v", err)
	}
	if got := gitRun(t, dir, "config", "--get", "branch.main.remote"); got != "fork" {
		t.Fatalf("branch.main.remote = %q, want fork (the sole remote)", got)
	}
}

func TestTrackBranchLeavesAmbiguousUntracked(t *testing.T) {
	dir := gitDir(t)
	remotes := []state.Remote{
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
		{Name: "fork", Fetch: "https://example.com/me/app.git"},
	}
	if err := (Systemd{}).TrackBranch(dir, "main", remotes); err != nil {
		t.Fatalf("TrackBranch: %v", err)
	}
	if _, ok := gitConfigGet(t, dir, "branch.main.remote"); ok {
		t.Fatal("branch.main.remote is set; want the branch untracked when the choice is ambiguous")
	}
	if got := gitRun(t, dir, "config", "--get", "push.default"); got != "current" {
		t.Fatalf("push.default = %q, want current regardless", got)
	}
}

func TestTrackBranchLocalOnlyIsANoop(t *testing.T) {
	dir := gitDir(t)
	if err := (Systemd{}).TrackBranch(dir, "main", nil); err != nil {
		t.Fatalf("TrackBranch: %v", err)
	}
	for _, key := range []string{"branch.main.remote", "branch.main.merge", "push.default"} {
		if _, ok := gitConfigGet(t, dir, key); ok {
			t.Fatalf("%s is set; want a local-only box left alone", key)
		}
	}
}
