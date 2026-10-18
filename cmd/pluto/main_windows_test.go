//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildPluto builds the real Windows client and returns its path. The test
// drives the shipped executable rather than the package, so it covers the
// entry point and the config path the student will hit.
func buildPluto(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "pluto.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pluto.exe: %v\n%s", err, output)
	}
	return exe
}

// fakeSSHOnPath compiles the shared Go fake ssh as ssh.exe and puts it first
// on PATH, so the real client's plain `ssh` lookup finds the fake. It is not
// a shell script because Windows runs the same test.
func fakeSSHOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ssh.exe")
	build := exec.Command("go", "build", "-o", bin, "../../internal/remotecli/testdata/fakessh")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake ssh.exe: %v\n%s", err, output)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// plutoEnv points the client's registry at a temp config dir. Go reads the
// %AppData% variable for os.UserConfigDir on Windows.
func plutoEnv(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("APPDATA", cfg)
	return cfg
}

// runPluto runs the built client with the test environment and returns its
// exit code plus both streams.
func runPluto(t *testing.T, exe, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run pluto %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// device add then device ls work through the real executable and write the
// registry under %AppData%\pluto\devices.toml.
func TestWindowsClientDeviceAddLs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	cfg := plutoEnv(t)
	exe := buildPluto(t)

	code, out, errOut := runPluto(t, exe, "", "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved device neptuno") {
		t.Fatalf("device add output = %q, want the save note", out)
	}
	registry := filepath.Join(cfg, "pluto", "devices.toml")
	if _, err := os.Stat(registry); err != nil {
		t.Fatalf("registry not written under %%AppData%%: %v", err)
	}

	code, out, errOut = runPluto(t, exe, "", "device", "ls")
	if code != 0 {
		t.Fatalf("device ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"neptuno", "siddhant@neptuno"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device ls output = %q, want %q", out, want)
		}
	}
}

// A remote command streams stdout and stderr and returns the remote exit
// status to the Windows process.
func TestWindowsClientReturnsRemoteStreamsAndExitStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	plutoEnv(t)
	exe := buildPluto(t)

	t.Setenv("FAKE_SSH_STDOUT", "remote says hi\n")
	t.Setenv("FAKE_SSH_STDERR", "remote says hmm\n")
	t.Setenv("FAKE_SSH_EXIT", "7")

	code, out, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "status", "mybox")
	if code != 7 {
		t.Fatalf("exit = %d, want the remote exit status 7 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "remote says hi") {
		t.Fatalf("stdout = %q, want the remote stdout streamed", out)
	}
	if !strings.Contains(errOut, "remote says hmm") {
		t.Fatalf("stderr = %q, want the remote stderr streamed", errOut)
	}
}

// stdin reaches the remote process: the fake echoes it back on stdout.
func TestWindowsClientStreamsStdin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	plutoEnv(t)
	exe := buildPluto(t)

	t.Setenv("FAKE_SSH_ECHO_STDIN", "1")
	code, out, errOut := runPluto(t, exe, "hello from stdin\n", "--device", "sid@neptuno", "run", "--", "cat")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "hello from stdin") {
		t.Fatalf("stdout = %q, want stdin streamed through", out)
	}
}

// A host command without --device is a usage error naming device add.
func TestWindowsClientRequiresADevice(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	plutoEnv(t)
	exe := buildPluto(t)

	code, _, errOut := runPluto(t, exe, "", "ls")
	if code != 2 {
		t.Fatalf("exit = %d, want a usage error 2 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"pluto device add", "--device"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}
