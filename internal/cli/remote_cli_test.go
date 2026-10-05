package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeDeviceSSH puts a fake ssh first on PATH: it logs each argument on its
// own line, writes out to stdout and errOut to stderr, and exits with code.
// Tests never ssh.
func fakeDeviceSSH(t *testing.T, out, errOut string, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "ssh.args")
	outFile := filepath.Join(dir, "ssh.stdout")
	errFile := filepath.Join(dir, "ssh.stderr")
	if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
		t.Fatalf("write fake ssh output: %v", err)
	}
	if err := os.WriteFile(errFile, []byte(errOut), 0o644); err != nil {
		t.Fatalf("write fake ssh error: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat %q\ncat %q 1>&2\nexit %d\n", log, outFile, errFile, code)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// loggedArgs returns the argv the fake ssh was invoked with.
func loggedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestDeviceFlagRunsOnARawSSHTarget(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "remote says hi\n", "remote says hmm\n", 0)

	code, out, errOut := runCLI(t, "--device", "sid@neptuno", "status", "mybox")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "remote says hi") {
		t.Fatalf("stdout = %q, want the remote output streamed", out)
	}
	if !strings.Contains(errOut, "remote says hmm") {
		t.Fatalf("stderr = %q, want the remote error streamed", errOut)
	}
	args := loggedArgs(t, log)
	want := []string{"--", "sid@neptuno", "pluto", "status", "mybox"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want %q", args, want)
	}
}

func TestDeviceFlagPassesThroughTheRemoteExitCode(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "", "", 7)

	code, _, errOut := runCLI(t, "--device", "sid@neptuno", "run", "mybox", "--", "make", "test")
	if code != 7 {
		t.Fatalf("exit = %d, want the remote 7 (stderr: %s)", code, errOut)
	}
	args := loggedArgs(t, log)
	want := []string{"--", "sid@neptuno", "pluto", "run", "mybox", "--", "make", "test"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want the command preserved exactly", args)
	}
}

// saveDevice writes a devices.toml in the shape `pluto device add` persists.
func saveDevice(t *testing.T, cfg, nickname, target string) {
	t.Helper()
	dir := filepath.Join(cfg, "pluto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := fmt.Sprintf("[device]\n%s = %q\n", nickname, target)
	if err := os.WriteFile(filepath.Join(dir, "devices.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write devices.toml: %v", err)
	}
}

func TestDeviceFlagResolvesASavedNickname(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "neptuno", "ls")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	args := loggedArgs(t, log)
	want := []string{"--", "siddhant@neptuno", "pluto", "ls"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want the saved device's target", args)
	}
}

func TestDeviceFlagUnknownNicknameNamesTheNextStep(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "missing", "ls")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"unknown device", "missing", "next:", "'pluto device ls'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("ssh ran for an unknown device (stat err = %v)", err)
	}
}

// corruptDevices writes an unparseable devices.toml under the XDG config dir.
func corruptDevices(t *testing.T, cfg string) string {
	t.Helper()
	dir := filepath.Join(cfg, "pluto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "devices.toml")
	if err := os.WriteFile(path, []byte("not = = toml\n"), 0o644); err != nil {
		t.Fatalf("write corrupt devices.toml: %v", err)
	}
	return path
}

func TestDeviceFlagRawTargetNeedsNoRegistry(t *testing.T) {
	cfg := deviceConfig(t)
	corruptDevices(t, cfg)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "sid@neptuno", "version")
	if code != 0 {
		t.Fatalf("an ad-hoc target must not read the registry; exit = %d, stderr: %s", code, errOut)
	}
	args := loggedArgs(t, log)
	want := []string{"--", "sid@neptuno", "pluto", "version"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want %q", args, want)
	}
}

func TestDeviceFlagUnreadableRegistryShowsTheNextStep(t *testing.T) {
	cfg := deviceConfig(t)
	path := corruptDevices(t, cfg)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "neptuno", "ls")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{path, "next:", "fix '" + path + "'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("ssh ran with a corrupt registry (stat err = %v)", err)
	}
}

func TestDeviceFlagForwardsGlobalFlagsAndDropsItself(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t,
		"--socket", "/run/remote.sock",
		"--state-dir", "/var/lib/remote",
		"--device", "sid@host",
		"logs", "mybox", "--job", "last")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	args := loggedArgs(t, log)
	want := []string{
		"--", "sid@host", "pluto",
		"--socket", "/run/remote.sock",
		"--state-dir", "/var/lib/remote",
		"logs", "mybox", "--job", "last",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want globals forwarded and --device dropped", args)
	}
}

func TestDeviceFlagPreservesQuotedArguments(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "sid@host", "run", "mybox", "--", "sh", "-c", "echo hello world")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	args := loggedArgs(t, log)
	want := []string{"--", "sid@host", "pluto", "run", "mybox", "--", "sh", "-c", "'echo hello world'"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("ssh argv = %q, want arguments preserved for the remote shell", args)
	}
}

func TestDeviceFlagWithoutACommandIsAUsageError(t *testing.T) {
	deviceConfig(t)
	log := fakeDeviceSSH(t, "", "", 0)

	code, _, errOut := runCLI(t, "--device", "sid@host")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "--device") {
		t.Fatalf("usage = %q, want it to document --device", errOut)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("ssh ran without a command (stat err = %v)", err)
	}

	code, _, _ = runCLI(t, "--device", "", "ls")
	if code != 2 {
		t.Fatalf("empty --device exit = %d, want a usage error 2", code)
	}
}
