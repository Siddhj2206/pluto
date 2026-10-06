package cli_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// Bare attach still opens a plain shell; --session opens the declared tmux
// session, appended to the ssh invocation after the box target.
func TestAttachOpensAShellOrASession(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantTail []string
	}{
		{"bare", nil, []string{"dev@box"}},
		{"session", []string{"--session", "agent"}, []string{"dev@box", "tmux", "attach-session", "-t", "agent"}},
		{"session equals form", []string{"--session=agent"}, []string{"dev@box", "tmux", "attach-session", "-t", "agent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			socket, st := startDaemonWith(t, fakeRunner{})
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, contract.FileName), []byte("[sessions.agent]\ncommand = \"sleep 1\"\n"), 0o644); err != nil {
				t.Fatalf("write contract: %v", err)
			}
			if _, _, err := st.CreateBox("app", "main", dir); err != nil {
				t.Fatalf("CreateBox: %v", err)
			}
			log := fakeDeviceSSH(t, "", "", 0)

			args := append([]string{"--socket", socket, "attach", dir}, tc.args...)
			code, _, errOut := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("attach exit = %d, stderr: %s", code, errOut)
			}
			got := loggedArgs(t, log)
			if len(got) < len(tc.wantTail) || !reflect.DeepEqual(got[len(got)-len(tc.wantTail):], tc.wantTail) {
				t.Fatalf("ssh argv = %q, want tail %q", got, tc.wantTail)
			}
		})
	}
}

// --session with an explicit command is a usage error: the two disagree about
// what to run, and silently ignoring either is worse than saying so.
func TestAttachSessionWithACommandIsAUsageError(t *testing.T) {
	socket, st := startDaemonWith(t, fakeRunner{})
	dir := t.TempDir()
	if _, _, err := st.CreateBox("app", "main", dir); err != nil {
		t.Fatalf("CreateBox: %v", err)
	}

	code, _, errOut := runCLI(t, "--socket", socket, "attach", dir, "--session", "agent", "--", "uname", "-a")
	if code != 2 {
		t.Fatalf("attach exit = %d, want 2 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "--session") {
		t.Fatalf("stderr = %q, want the conflict named", errOut)
	}
}
