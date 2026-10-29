//go:build windows

package main

// Attach from Windows drives the shipped pluto.exe, not the package: the
// client replays `pluto attach <box>` on the host over ssh. The shared fake
// ssh echoes stdin here, so these tests cover the process-level argv, both
// directions of the stream, and the shell's exit status on Windows CI. A real
// console cannot be allocated from the test, so the `ssh -t` branch is covered
// by the unit test in internal/remotecli and by Windows-to-Linux acceptance.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/testssh"
)

// Bare attach opens the box's default shell: the command reaches the host
// unchanged and the host's output returns through pluto.exe.
func TestWindowsClientAttachesToTheBoxShell(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_STDOUT", "box shell ready\n")

	code, out, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "attach", "box-1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "box shell ready") {
		t.Fatalf("stdout = %q, want the host's output streamed", out)
	}
	want := clientSSHArgs("sid@neptuno", "attach", "box-1")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// --session reaches the host with the box and the session name in the order
// the user typed them, so the host enters the declared session.
func TestWindowsClientAttachesIntoADeclaredSession(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)

	code, _, errOut := runPluto(t, exe, "", "--device", "sid@neptuno",
		"attach", "box-1", "--session", "agent")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := clientSSHArgs("sid@neptuno", "attach", "box-1", "--session", "agent")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the session preserved for the host", got)
	}
}

// Input and output stream in both directions through the real executable: the
// text pluto.exe reads on stdin reaches the remote ssh, and what ssh writes
// comes back on pluto.exe's stdout.
func TestWindowsClientAttachStreamsStdinBothWays(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	testssh.EchoStdin(t)

	input := "echo hello from the box\nexit\n"
	code, out, errOut := runPluto(t, exe, input, "--device", "sid@neptuno", "attach", "box-1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, input) {
		t.Fatalf("stdout = %q, want stdin echoed back through the client", out)
	}
}

// When the box shell exits nonzero, pluto.exe returns that status so the
// Windows prompt sees the failure. Exiting 0 returns control silently and is
// covered by the attach tests above.
func TestWindowsClientAttachReturnsTheShellExitStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_EXIT", "3")

	code, _, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "attach", "box-1")
	if code != 3 {
		t.Fatalf("exit = %d, want the shell's exit status 3 (stderr %q)", code, errOut)
	}
	if strings.Contains(errOut, "pluto: ssh:") {
		t.Fatalf("stderr = %q, want the exit status, not an ssh failure", errOut)
	}
}

// A saved nickname resolves for attach too, so the demo uses the same
// --device form as every other host command.
func TestWindowsClientAttachUsesASavedDevice(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	cfg := plutoEnv(t)
	exe := buildPluto(t)
	if err := os.MkdirAll(filepath.Join(cfg, "pluto"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	devices := filepath.Join(cfg, "pluto", "devices.toml")
	if err := os.WriteFile(devices, []byte("[device]\nneptuno = \"siddhant@neptuno\"\n"), 0o644); err != nil {
		t.Fatalf("write devices.toml: %v", err)
	}
	code, _, errOut := runPluto(t, exe, "", "--device", "neptuno", "attach", "box-1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := clientSSHArgs("siddhant@neptuno", "attach", "box-1")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the saved target", got)
	}
}
