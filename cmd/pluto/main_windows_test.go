//go:build windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// buildPluto builds the real Windows client and returns its path. The test
// drives the shipped executable rather than the package, so it covers the
// entry point and the config path the student will hit. A binary built ahead
// of time can be supplied with PLUTO_WIN_TEST_BINARY, which lets the test run
// under an emulator (the Wine rehearsal in scripts/windows-client-wine-smoke.sh)
// where no Go toolchain is available.
func buildPluto(t *testing.T) string {
	t.Helper()
	if exe := os.Getenv("PLUTO_WIN_TEST_BINARY"); exe != "" {
		return exe
	}
	exe := filepath.Join(t.TempDir(), "pluto.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build pluto.exe: %v\n%s", err, output)
	}
	return exe
}

// fakeSSHOnPath compiles the shared Go fake ssh as ssh.exe and puts it first
// on PATH, so the real client's plain `ssh` lookup finds the fake. It is not
// a shell script because Windows runs the same test. A fake built ahead of
// time can be supplied with PLUTO_FAKE_SSH_BINARY (its directory goes on
// PATH), so the test can run without a Go toolchain.
func fakeSSHOnPath(t *testing.T) {
	t.Helper()
	if bin := os.Getenv("PLUTO_FAKE_SSH_BINARY"); bin != "" {
		t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
		return
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ssh.exe")
	build := exec.Command("go", "build", "-o", bin, "../../internal/remotecli/testdata/fakessh")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake ssh.exe: %v\n%s", err, output)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// loggedSSHArgs returns the argv the fake ssh recorded for the last command.
func loggedSSHArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
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

// The shipped executable dispatches provisioning and box lifecycle commands
// to the host unchanged. The shared fake ssh records argv, so this covers
// argument handling on Windows CI as well as the returned streams.
func TestWindowsClientDispatchesProvisioningAndLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	plutoEnv(t)
	exe := buildPluto(t)
	log := filepath.Join(t.TempDir(), "ssh.args")
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_SSH_STDOUT", "host says ok\n")

	prefix := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=accept-new", "--", "sid@neptuno", "pluto"}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"provision", []string{"up", "--repo", "https://github.com/you/app.git?ref=main&depth=1"},
			[]string{"up", "--repo", "'https://github.com/you/app.git?ref=main&depth=1'"}},
		{"list", []string{"ls"}, []string{"ls"}},
		{"inspect", []string{"status", "box-1"}, []string{"status", "box-1"}},
		{"pause", []string{"pause", "box-1"}, []string{"pause", "box-1"}},
		{"destroy", []string{"destroy", "box-1", "--yes"}, []string{"destroy", "box-1", "--yes"}},
	}
	for _, tc := range cases {
		if err := os.Remove(log); err != nil && !os.IsNotExist(err) {
			t.Fatalf("%s: reset ssh log: %v", tc.name, err)
		}
		code, out, errOut := runPluto(t, exe, "", append([]string{"--device", "sid@neptuno"}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%s exit = %d, stderr: %s", tc.name, code, errOut)
		}
		if !strings.Contains(out, "host says ok") {
			t.Fatalf("%s stdout = %q, want the host's output streamed", tc.name, out)
		}
		want := append(append([]string{}, prefix...), tc.want...)
		if got := loggedSSHArgs(t, log); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ssh argv = %q, want %q", tc.name, got, want)
		}
	}
}

// A host that rejects the repository URL fails the Windows process with the
// host's status and message.
func TestWindowsClientReportsAFailedProvisioning(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	fakeSSHOnPath(t)
	plutoEnv(t)
	exe := buildPluto(t)

	t.Setenv("FAKE_SSH_STDERR", "fatal: repository 'https://github.com/you/app.git' not found\n")
	t.Setenv("FAKE_SSH_EXIT", "1")

	code, _, errOut := runPluto(t, exe, "", "--device", "sid@neptuno",
		"up", "--repo", "https://github.com/you/app.git")
	if code != 1 {
		t.Fatalf("exit = %d, want the host's exit status 1", code)
	}
	if !strings.Contains(errOut, "repository 'https://github.com/you/app.git' not found") {
		t.Fatalf("stderr = %q, want the host's failure", errOut)
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
