package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCommandNeedsNoDaemonOrGitRepo(t *testing.T) {
	dir := t.TempDir()
	stateHome := filepath.Join(t.TempDir(), "state-home")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Chdir(dir)
	var out, stderr bytes.Buffer
	if code := Run([]string{"init"}, &out, &stderr); code != 0 {
		t.Fatalf("init exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".pluto.toml")); err != nil {
		t.Fatalf("starter contract missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "pluto")); err == nil {
		t.Fatal("init unexpectedly created state directory")
	}
}

func TestInitCreatesContractAndPreservesExistingContract(t *testing.T) {
	dir := t.TempDir()
	if err := initRepo(dir, false, false); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(dir, ".pluto.toml")
	first, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "[jobs.test]") {
		t.Fatalf("starter contract = %q", first)
	}
	if err := os.WriteFile(contract, []byte("my contract\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initRepo(dir, false, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(contract)
	if string(got) != "my contract\n" {
		t.Fatalf("existing contract changed: %q", got)
	}
}

func TestInitHookSetupPreservesExistingHookAndCanBeRemoved(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	hooks := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(hooks, "post-commit")
	if err := os.WriteFile(original, []byte("#!/bin/sh\necho existing\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := initRepo(dir, true, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(original)
	if err != nil || string(got) != "#!/bin/sh\necho existing\n" {
		t.Fatalf("existing hook changed: %q, %v", got, err)
	}
	managed := gitOutput(t, dir, "config", "--local", "core.hooksPath")
	if managed == "" || managed == hooks {
		t.Fatalf("core.hooksPath = %q", managed)
	}
	installed, err := os.ReadFile(filepath.Join(managed, "post-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), original) || !strings.Contains(string(installed), "event post-commit") {
		t.Fatalf("hook does not chain and notify: %s", installed)
	}
	if err := initRepo(dir, false, true); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(original)
	if err != nil || string(got) != "#!/bin/sh\necho existing\n" {
		t.Fatalf("existing hook lost after removal: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(managed, "post-commit")); !os.IsNotExist(err) {
		t.Fatalf("managed hook remains: %v", err)
	}
	if got := gitOutputOptional(t, dir, "config", "--local", "--get", "core.hooksPath"); got != "" {
		t.Fatalf("core.hooksPath remains: %q", got)
	}
}

func TestInitHookSetupRestoresConfiguredHooksPath(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	originalDir := filepath.Join(dir, "team-hooks")
	if err := os.MkdirAll(originalDir, 0700); err != nil {
		t.Fatal(err)
	}
	originalHook := filepath.Join(originalDir, "post-commit")
	if err := os.WriteFile(originalHook, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "core.hooksPath", "team-hooks")
	if err := initRepo(dir, true, false); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, dir, "config", "--local", "core.hooksPath"); got == "team-hooks" {
		t.Fatalf("hooksPath was not routed through Pluto: %q", got)
	}
	installed, err := os.ReadFile(filepath.Join(gitOutput(t, dir, "config", "--local", "core.hooksPath"), "post-commit"))
	if err != nil || !strings.Contains(string(installed), originalHook) {
		t.Fatalf("installed hook does not chain to configured hook: %s, %v", installed, err)
	}
	if err := initRepo(dir, false, true); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, dir, "config", "--local", "core.hooksPath"); got != "team-hooks" {
		t.Fatalf("core.hooksPath = %q, want original team-hooks", got)
	}
	if _, err := os.Stat(originalHook); err != nil {
		t.Fatalf("configured hook was lost: %v", err)
	}
}

func TestInitHookRemovalPreservesModifiedManagedHook(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	if err := initRepo(dir, true, false); err != nil {
		t.Fatal(err)
	}
	managed := gitOutput(t, dir, "config", "--local", "core.hooksPath")
	path := filepath.Join(managed, "post-commit")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# user edit\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := initRepo(dir, false, true); err == nil {
		t.Fatal("removal unexpectedly deleted/disabled a changed user hook")
	}
	if got := gitOutput(t, dir, "config", "--local", "core.hooksPath"); got != managed {
		t.Fatalf("active hook path changed: %q", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("user hook was removed: %v", err)
	}
}

func gitOutputOptional(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
