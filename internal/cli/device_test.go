package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSSH puts a fake ssh first on PATH so `device add` probes never reach
// the network. It logs its arguments and exits with code.
func fakeSSH(t *testing.T, code int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexit %d\n", log, code)
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// deviceConfig points the registry at a fresh XDG config dir. The CLI does
// not need a daemon, so these tests never start one.
func deviceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestDeviceAddLsRm(t *testing.T) {
	cfg := deviceConfig(t)
	log := fakeSSH(t, 0)

	code, out, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno")
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
	probeArgs := string(probe)
	for _, want := range []string{"siddhant@neptuno", "pluto", "version", "BatchMode=yes", "ConnectTimeout=5"} {
		if !strings.Contains(probeArgs, want) {
			t.Fatalf("probe args = %q, want %q", probeArgs, want)
		}
	}

	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); err != nil {
		t.Fatalf("registry file not written under XDG config: %v", err)
	}

	code, out, errOut = runCLI(t, "device", "ls")
	if code != 0 {
		t.Fatalf("device ls exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno") || !strings.Contains(out, "siddhant@neptuno") {
		t.Fatalf("device ls output = %q, want the saved device", out)
	}

	code, out, errOut = runCLI(t, "device", "rm", "neptuno")
	if code != 0 {
		t.Fatalf("device rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "removed") || !strings.Contains(out, "neptuno") {
		t.Fatalf("device rm output = %q, want a removal note", out)
	}

	code, out, errOut = runCLI(t, "device", "ls")
	if code != 0 {
		t.Fatalf("device ls after rm exit = %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out, "neptuno") {
		t.Fatalf("device ls still shows the removed device: %q", out)
	}
}

func TestDeviceAddWarnsButSavesWhenTheTargetDoesNotAnswer(t *testing.T) {
	deviceConfig(t)
	fakeSSH(t, 7)

	code, out, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add must not fail on a failed probe; exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved") {
		t.Fatalf("device add output = %q, want the save note", out)
	}
	if !strings.Contains(errOut, "warning") {
		t.Fatalf("device add stderr = %q, want a warning", errOut)
	}

	code, out, _ = runCLI(t, "device", "ls")
	if code != 0 || !strings.Contains(out, "neptuno") {
		t.Fatalf("device ls after a warned add = %q, want the saved device", out)
	}
}

func TestDeviceRmUnknownFails(t *testing.T) {
	deviceConfig(t)
	code, _, errOut := runCLI(t, "device", "rm", "missing")
	if code != 1 {
		t.Fatalf("removing an unknown device exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "missing") {
		t.Fatalf("stderr = %q, want the unknown nickname", errOut)
	}
	// ADR 0009: a failure names the next step.
	if !strings.Contains(errOut, "next:") || !strings.Contains(errOut, "'pluto device ls'") {
		t.Fatalf("stderr = %q, want a next step pointing at 'pluto device ls'", errOut)
	}
}

func TestDeviceUnreadableRegistryShowsTheNextStep(t *testing.T) {
	cfg := deviceConfig(t)
	path := filepath.Join(cfg, "pluto", "devices.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("not = = toml\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	code, _, errOut := runCLI(t, "device", "ls")
	if code != 1 {
		t.Fatalf("ls on a corrupt registry exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, path) {
		t.Fatalf("stderr = %q, want the registry path", errOut)
	}
	if !strings.Contains(errOut, "next:") {
		t.Fatalf("stderr = %q, want a next step", errOut)
	}
}

// ADR 0009: a non-usage `device add` failure names the next step too.
func TestDeviceAddUnreadableRegistryNamesTheNextStep(t *testing.T) {
	cfg := deviceConfig(t)
	path := filepath.Join(cfg, "pluto", "devices.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("not = = toml\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	code, _, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 1 {
		t.Fatalf("add on a corrupt registry exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{path, "next:", "fix '" + path + "'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

func TestDeviceAddRejectsInvalidInput(t *testing.T) {
	deviceConfig(t)
	for _, args := range [][]string{
		{"device", "add", "two words", "siddhant@neptuno"},
		{"device", "add", "neptuno", ""},
		{"device", "add", "neptuno", "-oProxyCommand=boom"},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want a usage error 2 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(errOut, "invalid") || !strings.Contains(errOut, "usage") {
			t.Fatalf("%v stderr = %q, want an invalid-argument error and usage", args, errOut)
		}
	}
}

func TestDeviceUsage(t *testing.T) {
	for _, args := range [][]string{{"device"}, {"device", "add", "one"}, {"device", "rm"}, {"device", "bogus"}} {
		code, _, errOut := runCLI(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2 (stderr %q)", args, code, errOut)
		}
		if errOut == "" {
			t.Fatalf("%v printed no usage or error", args)
		}
	}
}
