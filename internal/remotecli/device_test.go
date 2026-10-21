package remotecli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/remotecli"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// runDevice invokes RunDevice with fresh buffers.
func runDevice(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := remotecli.RunDevice(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// add/ls/rm round-trips a device through the registry and the probe.
func TestRunDeviceAddLsRm(t *testing.T) {
	cfg := deviceConfig(t)
	log := testssh.Start(t, "/home/sid/.local/bin/pluto\n", "", 0)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno") || !strings.Contains(out, "siddhant@neptuno") {
		t.Fatalf("device add output = %q, want the nickname and target", out)
	}
	if !strings.Contains(out, "connected: pluto at /home/sid/.local/bin/pluto") {
		t.Fatalf("device add output = %q, want the learned host path", out)
	}

	// The probe asks for the host's pluto path, bounded by ssh options.
	probe, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("probe did not run: %v", err)
	}
	for _, want := range []string{"siddhant@neptuno", "command -v pluto", ".local/bin/pluto", "BatchMode=yes", "ConnectTimeout=5"} {
		if !strings.Contains(string(probe), want) {
			t.Fatalf("probe args = %q, want %q", probe, want)
		}
	}

	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); err != nil {
		t.Fatalf("registry file not written under the user config dir: %v", err)
	}

	code, out, errOut = runDevice(t, "ls")
	if code != 0 {
		t.Fatalf("device ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno") || !strings.Contains(out, "siddhant@neptuno") {
		t.Fatalf("device ls output = %q, want the saved device", out)
	}

	code, out, errOut = runDevice(t, "rm", "neptuno")
	if code != 0 {
		t.Fatalf("device rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "removed") || !strings.Contains(out, "neptuno") {
		t.Fatalf("device rm output = %q, want a removal note", out)
	}

	code, out, _ = runDevice(t, "ls")
	if code != 0 || strings.Contains(out, "neptuno") {
		t.Fatalf("device ls after rm = %q, want the device gone", out)
	}
}

// A fresh host that only accepts a password is the normal Windows case:
// device add asks once, installs the key, and learns where pluto lives.
func TestRunDeviceAddInstallsAKeyAndLearnsThePath(t *testing.T) {
	log := testssh.StartKeyBootstrap(t, "/home/sid/.local/bin/pluto\n")
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	cfg := deviceConfig(t)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"saved device neptuno", "connected: pluto at /home/sid/.local/bin/pluto"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device add stdout = %q, want %q", out, want)
		}
	}
	for _, want := range []string{"installing an ssh key", "enter the account password once"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("device add stderr = %q, want %q", errOut, want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "id_ed25519")); err != nil {
		t.Fatalf("device add did not create a key in the isolated home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "id_ed25519.pub")); err != nil {
		t.Fatalf("device add did not write the public key: %v", err)
	}

	// The successful connected line proves the install ran: the fake only
	// stops refusing the login after it sees the install command.
	args := testssh.LoggedArgs(t, log)
	if !strings.Contains(strings.Join(args, " "), "command -v pluto") {
		t.Fatalf("last ssh call = %q, want the post-install probe", args)
	}

	data, err := os.ReadFile(filepath.Join(cfg, "pluto", "devices.toml"))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, want := range []string{"[remote]", "/home/sid/.local/bin/pluto"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("registry = %q, want %q", data, want)
		}
	}
}

// Later commands use the path learned at add time, not the bare name that a
// non-interactive ssh PATH may not contain.
func TestRunDeviceAddPathIsUsedByLaterCommands(t *testing.T) {
	cfg := deviceConfig(t)
	testssh.Start(t, "/usr/local/bin/pluto\n", "", 0)
	if code, _, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno"); code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); err != nil {
		t.Fatalf("registry not written: %v", err)
	}

	log := testssh.Start(t, "boxes\n", "", 0)
	var out, errOut bytes.Buffer
	if code := remotecli.Run("neptuno", []string{"ls"}, &out, &errOut); code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut.String())
	}
	want := append(append([]string{}, sshOptions...), "--", "siddhant@neptuno", "/usr/local/bin/pluto", "ls")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// A target that does not answer warns but still saves the device.
func TestRunDeviceAddWarnsButSavesWhenTheTargetDoesNotAnswer(t *testing.T) {
	deviceConfig(t)
	testssh.Start(t, "", "", 7)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add must not fail on a failed probe; exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved") {
		t.Fatalf("device add output = %q, want the save note", out)
	}
	if !strings.Contains(errOut, "warning") {
		t.Fatalf("device add stderr = %q, want a warning", errOut)
	}
}

// Removing an unknown device names the next step.
func TestRunDeviceRmUnknownFails(t *testing.T) {
	deviceConfig(t)
	code, _, errOut := runDevice(t, "rm", "missing")
	if code != 1 {
		t.Fatalf("removing an unknown device exit = %d, want 1", code)
	}
	for _, want := range []string{"missing", "next:", "'pluto device ls'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A corrupt registry names the file and retries nothing.
func TestRunDeviceUnreadableRegistryShowsTheNextStep(t *testing.T) {
	cfg := deviceConfig(t)
	path := corruptDevices(t, cfg)
	code, _, errOut := runDevice(t, "ls")
	if code != 1 {
		t.Fatalf("ls on a corrupt registry exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{path, "next:", "fix '" + path + "'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// Invalid input is a usage error before anything is stored.
func TestRunDeviceAddRejectsInvalidInput(t *testing.T) {
	deviceConfig(t)
	for _, args := range [][]string{
		{"add", "two words", "siddhant@neptuno"},
		{"add", "neptuno", ""},
		{"add", "neptuno", "-oProxyCommand=boom"},
	} {
		code, _, errOut := runDevice(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want a usage error 2 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(errOut, "invalid") || !strings.Contains(errOut, "usage") {
			t.Fatalf("%v stderr = %q, want an invalid-argument error and usage", args, errOut)
		}
	}
}

// The device tree rejects bad shapes with exit 2.
func TestRunDeviceUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"add", "one"}, {"rm"}, {"bogus"}} {
		code, _, errOut := runDevice(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2 (stderr %q)", args, code, errOut)
		}
		if errOut == "" {
			t.Fatalf("%v printed no usage or error", args)
		}
	}
}

// The device tree answers its own help without a registry or ssh.
func TestRunDeviceHelp(t *testing.T) {
	cases := []struct {
		args   []string
		detail string
	}{
		{[]string{"-h"}, "Manage the client-side registry"},
		{[]string{"--help"}, "Manage the client-side registry"},
		{[]string{"add", "-h"}, "Save a nickname for an ssh destination"},
		{[]string{"ls", "-h"}, "List saved devices."},
		{[]string{"rm", "-h"}, "Remove a saved device."},
	}
	for _, tc := range cases {
		code, out, errOut := runDevice(t, tc.args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", tc.args, code, errOut)
		}
		for _, want := range []string{"usage: pluto device", tc.detail, "examples:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v output = %q, want %q", tc.args, out, want)
			}
		}
	}
}
