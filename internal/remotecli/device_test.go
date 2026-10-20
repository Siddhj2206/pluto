package remotecli_test

import (
	"bytes"
	"os"
	"path/filepath"
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
	log := testssh.Start(t, "", "", 0)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno") || !strings.Contains(out, "siddhant@neptuno") {
		t.Fatalf("device add output = %q, want the nickname and target", out)
	}

	// The probe ran `pluto version` on the target, bounded by ssh options.
	probe, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("probe did not run: %v", err)
	}
	for _, want := range []string{"siddhant@neptuno", "pluto", "version", "BatchMode=yes", "ConnectTimeout=5"} {
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
