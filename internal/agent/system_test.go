package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestHookUnitFile(t *testing.T) {
	body := hookUnitFile("provision", "/home/dev/work/x",
		"/home/dev/.local/state/pluto/hooks/provision.sh", "/usr/bin:/bin",
		"/home/dev/.local/state/pluto/logs/provision.log", 10*time.Minute)
	for _, want := range []string{
		"Type=oneshot",
		"KillMode=control-group",
		"TimeoutStartSec=600",
		"WorkingDirectory=/home/dev/work/x",
		"ExecStart=/home/dev/.local/state/pluto/hooks/provision.sh",
		"StandardOutput=append:/home/dev/.local/state/pluto/logs/provision.log",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("hook unit missing %q:\n%s", want, body)
		}
	}
}

func TestServiceUnitFileQuotesSpaces(t *testing.T) {
	body := serviceUnitFile("web", "/home/dev/work/my repo",
		"/home/dev/.local/state/pluto/services/web.sh", "/usr/bin:/bin")
	if !strings.Contains(body, `WorkingDirectory="/home/dev/work/my repo"`) {
		t.Fatalf("service unit did not quote the worktree:\n%s", body)
	}
	if !strings.Contains(body, "Restart=on-failure") {
		t.Fatalf("service unit is not supervised:\n%s", body)
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
	if got := ag.Status().Wake.Error; !strings.Contains(got, "timed out") {
		t.Fatalf("wake error = %q, want a timeout", got)
	}
}
