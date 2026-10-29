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

	"github.com/Siddhj2206/pluto/internal/shquote"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// clientSSHArgs is the exact ssh argv the Windows client builds for one
// remote pluto command.
func clientSSHArgs(target string, argv ...string) []string {
	words := []string{"pluto"}
	for _, arg := range argv {
		words = append(words, shquote.Quote(arg))
	}
	return []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new", "--", target,
		"sh", "-c", shquote.QuoteAlways(strings.Join(words, " "))}
}

// buildPluto builds the real Windows client and returns its path. The test
// drives the shipped executable rather than the package, so it covers the
// entry point and the config path the user will hit. A binary built ahead
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

// plutoEnv points the client's registry at a temp config dir. Go reads the
// %AppData% variable for os.UserConfigDir on Windows.
func plutoEnv(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("APPDATA", cfg)
	t.Setenv("PLUTO_DEVICE_CACHE_DIR", filepath.Join(t.TempDir(), "device-cache"))
	return cfg
}

// runPluto runs the built client outside any project checkout with the test
// environment and returns its exit code plus both streams. Tests that need a
// checkout use runPlutoIn.
func runPluto(t *testing.T, exe, stdin string, args ...string) (int, string, string) {
	t.Helper()
	return runPlutoIn(t, exe, t.TempDir(), stdin, args...)
}

// runPlutoIn runs the built client with dir as its working directory.
func runPlutoIn(t *testing.T, exe, dir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
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
	testssh.StartScript(t,
		testssh.Rule{Match: "-G", Stdout: "host home.example.net\nuser siddhant\nport 22\n"},
		testssh.Rule{Match: "printf ok", Stdout: "ok"},
		testssh.Rule{Match: "uname -s", Stdout: "os=Linux\narch=x86_64\nshell=/bin/sh\n"},
		testssh.Rule{Match: "command -v pluto", Stdout: "/usr/local/bin/pluto\n"},
		testssh.Rule{Match: "version --json", Stdout: `{"schema_version":1,"version":"v0.1.5","protocol":1,"capabilities":["json"]}` + "\n"},
		testssh.Rule{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\n0.1.5\t2026-01-01\n"},
		testssh.Rule{Match: "pluto ls", Stdout: "ID\tPROJECT/BRANCH\n"},
	)
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
	testssh.Start(t, "", "", 0)
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
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	t.Setenv("FAKE_SSH_STDOUT", "host says ok\n")

	cases := []struct {
		name string
		args []string
	}{
		{"provision", []string{"up", "--repo", "https://github.com/you/app.git?ref=main&depth=1"}},
		{"list", []string{"ls"}},
		{"inspect", []string{"status", "box-1"}},
		{"pause", []string{"pause", "box-1"}},
		{"destroy", []string{"destroy", "box-1", "--yes"}},
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
		want := clientSSHArgs("sid@neptuno", tc.args...)
		if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
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
	testssh.Start(t, "", "", 0)
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
	testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)

	testssh.EchoStdin(t)
	code, out, errOut := runPluto(t, exe, "hello from stdin\n", "--device", "sid@neptuno", "run", "--", "cat")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "hello from stdin") {
		t.Fatalf("stdout = %q, want stdin streamed through", out)
	}
}

// --device precedes the client's local commands in the shipped executable too:
// version is answered by the host, as it is by the Linux CLI.
func TestWindowsClientDevicePrecedesLocalCommands(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "host version ok\n", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)

	code, out, errOut := runPluto(t, exe, "", "--device", "sid@neptuno", "version")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "host version ok") {
		t.Fatalf("stdout = %q, want the host's answer, not the client version", out)
	}
	want := clientSSHArgs("sid@neptuno", "version")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// From a project checkout the shipped client sends the local identity as
// metadata, so the host can resolve a default target without a box id.
func TestWindowsClientSendsTheCheckoutContext(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	log := testssh.Start(t, "", "", 0)
	plutoEnv(t)
	exe := buildPluto(t)
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"remote", "add", "origin", "https://example.com/cipher.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v (%s)", err, out)
		}
	}

	code, _, errOut := runPlutoIn(t, exe, repo, "", "--device", "sid@neptuno", "up")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	got := testssh.LoggedArgs(t, log)
	prefix := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new", "--", "sid@neptuno", "sh", "-c"}
	if len(got) != len(prefix)+1 || !reflect.DeepEqual(got[:len(prefix)], prefix) {
		t.Fatalf("ssh argv = %q, want one sh -c command", got)
	}
	script := got[len(prefix)]
	for _, want := range []string{"env ", "PLUTO_CLIENT_CONTEXT=", `"project":"cipher"`, `"branch":"main"`, "https://example.com/cipher.git", "pluto up"} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote command = %q, want %q", script, want)
		}
	}
}

// A host command without --device is a usage error naming device add.
func TestWindowsClientRequiresADevice(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the real pluto.exe")
	}
	testssh.Start(t, "", "", 0)
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
