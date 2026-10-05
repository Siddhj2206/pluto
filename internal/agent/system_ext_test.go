package agent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/agent"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// fakeSystemctl puts a systemctl on PATH that reports every unit as loaded
// with exit 0 and copies each unit it is asked to start into capture, so
// Systemd's unit rendering is exercised without real systemd.
func fakeSystemctl(t *testing.T, capture string) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
case "$2" in
start)
	cp "$HOME/.config/systemd/user/$3" %q 2>/dev/null
	;;
show)
	printf 'loaded\n0\n'
	;;
esac
exit 0
`, capture)
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// RunHook renders the declared phase into a user unit and a script: dir and
// env resolution, the timebox, and the log destination.
func TestRunHookRendersUnitAndScript(t *testing.T) {
	home, stateDir, capture := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSystemctl(t, capture)
	t.Setenv("HOME", home)
	sys := agent.Systemd{Home: home, StateDir: stateDir}
	logPath := filepath.Join(stateDir, "logs", "provision.log")

	exit, err := sys.RunHook(context.Background(), "provision", "/home/dev/work/x", contract.Exec{
		Command: contract.ShellCommand("make"),
		Dir:     "sub",
		Env:     map[string]string{"FOO": "bar baz"},
		Timeout: 10 * time.Minute,
	}, logPath)
	if err != nil || exit != 0 {
		t.Fatalf("RunHook = %d, %v; want 0, nil", exit, err)
	}

	body := readFile(t, filepath.Join(home, ".config", "systemd", "user", "pluto-hook-provision.service"))
	for _, want := range []string{
		"Type=oneshot",
		"KillMode=control-group",
		"TimeoutStartSec=600",
		"WorkingDirectory=/home/dev/work/x/sub",
		"Environment=PATH=",
		`Environment="FOO=bar baz"`,
		"ExecStart=" + filepath.Join(stateDir, "hooks", "provision.sh"),
		"StandardOutput=append:" + logPath,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hook unit missing %q:\n%s", want, body)
		}
	}
	if got := readFile(t, filepath.Join(stateDir, "hooks", "provision.sh")); got != "#!/bin/sh\nexec /bin/sh -c make\n" {
		t.Fatalf("hook script = %q", got)
	}
}

// A string command runs through the shell; an array command is exec'd
// directly (ADR 0007).
func TestRunHookScriptsKeepTheirShape(t *testing.T) {
	home, stateDir, capture := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSystemctl(t, capture)
	t.Setenv("HOME", home)
	sys := agent.Systemd{Home: home, StateDir: stateDir}

	for _, tc := range []struct {
		name string
		cmd  contract.Command
		want string
	}{
		{"shell", contract.ShellCommand("pnpm test"), "#!/bin/sh\nexec /bin/sh -c 'pnpm test'\n"},
		{"argv", contract.ArgvCommand([]string{"pnpm", "dev"}), "#!/bin/sh\nexec pnpm dev\n"},
	} {
		if _, err := sys.RunHook(context.Background(), tc.name, "/home/dev/work/x", contract.Exec{Command: tc.cmd}, ""); err != nil {
			t.Fatalf("%s RunHook: %v", tc.name, err)
		}
		if got := readFile(t, filepath.Join(stateDir, "hooks", tc.name+".sh")); got != tc.want {
			t.Fatalf("%s script = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A job's timebox reaches the unit: unlimited by default, rounded up to
// whole seconds otherwise.
func TestRunJobRendersTheTimebox(t *testing.T) {
	home, stateDir, capture := t.TempDir(), t.TempDir(), t.TempDir()
	fakeSystemctl(t, capture)
	t.Setenv("HOME", home)
	sys := agent.Systemd{Home: home, StateDir: stateDir}

	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    string
	}{
		{"unlimited", 0, "TimeoutStartSec=infinity"},
		{"rounded", 1500 * time.Millisecond, "TimeoutStartSec=2"},
	} {
		jobID := state.NewID()
		logPath := filepath.Join(stateDir, "logs", "jobs", jobID+".log")
		exit, err := sys.RunJob(context.Background(), jobID, "/home/dev/work/x",
			contract.Exec{Command: contract.ArgvCommand([]string{"make"}), Timeout: tc.timeout}, logPath, func([]byte) {})
		if err != nil || exit != 0 {
			t.Fatalf("%s RunJob = %d, %v; want 0, nil", tc.name, exit, err)
		}
		body := readFile(t, filepath.Join(capture, "pluto-job-"+jobID+".service"))
		if !strings.Contains(body, tc.want) {
			t.Fatalf("%s unit missing %q:\n%s", tc.name, tc.want, body)
		}
	}
}
