package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
)

func TestAttachArgsProxyVsock(t *testing.T) {
	info := api.AttachInfo{User: "dev", UDS: "/state/boxes/abc/v.sock", Key: "/state/boxes/abc/id", Port: 22}
	args := attachArgs(info, "/usr/local/bin/pluto", nil, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-i /state/boxes/abc/id",
		"ProxyCommand=/usr/local/bin/pluto vsock connect /state/boxes/abc/v.sock 22",
		"dev@box",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestAttachArgsQuotesPathsWithSpaces(t *testing.T) {
	info := api.AttachInfo{User: "dev", UDS: "/home/dev/my state/v.sock", Key: "/home/dev/my state/id", Port: 22}
	args := attachArgs(info, "/opt/my tools/pluto", nil, false)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ProxyCommand='/opt/my tools/pluto' vsock connect '/home/dev/my state/v.sock' 22") {
		t.Fatalf("proxy command not quoted: %v", args)
	}
}

func TestAttachArgsPassesCommand(t *testing.T) {
	args := attachArgs(api.AttachInfo{User: "dev", Port: 22}, "/bin/pluto", []string{"uname", "-a"}, false)
	if len(args) < 2 || args[len(args)-2] != "uname" || args[len(args)-1] != "-a" {
		t.Fatalf("command not passed through: %v", args)
	}
}

// A named session becomes tmux's attach: it enters the declared session
// without starting one, so a gone session is an error, never a bare shell.
func TestAttachCommandNamesTheTmuxSession(t *testing.T) {
	got, err := attachCommand("agent", nil)
	if err != nil {
		t.Fatalf("attachCommand: %v", err)
	}
	want := []string{"tmux", "attach-session", "-t", "agent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attachCommand = %q, want %q", got, want)
	}
}

// Bare attach stays a plain shell: no session means no injected command.
func TestAttachCommandForAPlainShell(t *testing.T) {
	got, err := attachCommand("", nil)
	if err != nil || got != nil {
		t.Fatalf("bare attachCommand = %q, %v; want nil, nil", got, err)
	}
	got, err = attachCommand("", []string{"uname", "-a"})
	if err != nil || !reflect.DeepEqual(got, []string{"uname", "-a"}) {
		t.Fatalf("explicit command attachCommand = %q, %v; want the command", got, err)
	}
}

// --session and an explicit command cannot both choose the remote command.
func TestAttachCommandRejectsSessionWithAnExplicitCommand(t *testing.T) {
	if _, err := attachCommand("agent", []string{"uname"}); err == nil {
		t.Fatal("attachCommand accepted both --session and an explicit command")
	}
}

// The session argv flows through the ssh invocation exactly as tmux expects.
func TestAttachArgsCarriesTheSessionAttach(t *testing.T) {
	cmd, err := attachCommand("agent", nil)
	if err != nil {
		t.Fatalf("attachCommand: %v", err)
	}
	args := attachArgs(api.AttachInfo{User: "dev", Port: 22}, "/bin/pluto", cmd, false)
	joined := strings.Join(args, " ")
	if !strings.HasSuffix(joined, "dev@box tmux attach-session -t agent") {
		t.Fatalf("args = %v, want the tmux attach after the target", args)
	}
}

// A remote command needs an explicit pty request, or tmux fails with
// "open terminal failed: not a terminal"; an interactive attach asks ssh for
// one, ahead of the destination.
func TestAttachArgsRequestsAPtyForARemoteCommand(t *testing.T) {
	cmd, err := attachCommand("agent", nil)
	if err != nil {
		t.Fatalf("attachCommand: %v", err)
	}
	args := attachArgs(api.AttachInfo{User: "dev", Port: 22}, "/bin/pluto", cmd, true)
	if len(args) == 0 || args[0] != "-t" {
		t.Fatalf("args = %v, want a leading -t pty request", args)
	}
}

// A bare attach (no remote command) stays a plain ssh shell and must not
// request a pty on its own, so a piped stdin keeps working.
func TestAttachArgsLeavesABareAttachAlone(t *testing.T) {
	args := attachArgs(api.AttachInfo{User: "dev", Port: 22}, "/bin/pluto", nil, false)
	for _, a := range args {
		if a == "-t" {
			t.Fatalf("bare attach requested a pty: %v", args)
		}
	}
}

// attachTTY decides the pty request: only a remote command on an interactive
// stdin needs one. A bare attach, or a piped stdin, does not.
func TestAttachTTYOnlyForARemoteCommandOnATerminal(t *testing.T) {
	cases := []struct {
		name     string
		remote   []string
		terminal bool
		want     bool
	}{
		{"remote command on a terminal", []string{"tmux", "attach-session", "-t", "agent"}, true, true},
		{"remote command with piped stdin", []string{"uname", "-a"}, false, false},
		{"bare attach on a terminal", nil, true, false},
	}
	for _, tc := range cases {
		if got := attachTTY(tc.remote, tc.terminal); got != tc.want {
			t.Errorf("%s: attachTTY = %v, want %v", tc.name, got, tc.want)
		}
	}
}
