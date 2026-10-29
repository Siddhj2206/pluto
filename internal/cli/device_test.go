package cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// deviceConfig points the registry and cache at fresh directories. The CLI
// does not need a daemon, so these tests never start one. It also moves the
// test outside any project checkout, so a remote command sends no
// default-target context unless the test opts in.
func deviceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PLUTO_DEVICE_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Chdir(t.TempDir())
	return dir
}

func resolvedRule(host string, port int) testssh.Rule {
	return testssh.Rule{
		Match:  "-G",
		Stdout: fmt.Sprintf("host %s\nuser siddhant\nport %d\nproxyjump none\n", host, port),
	}
}

func healthyRules() []testssh.Rule {
	return []testssh.Rule{
		resolvedRule("home.example.net", 22),
		{Match: "printf ok", Stdout: "ok"},
		{Match: "uname -s", Stdout: "os=Linux\narch=x86_64\nshell=/bin/sh\n"},
		{Match: "command -v pluto", Stdout: "/usr/local/bin/pluto\n"},
		{Match: "version --json", Stdout: `{"schema_version":1,"version":"v0.1.5","protocol":1,"capabilities":["json"]}` + "\n"},
		{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\n0.1.5\t2026-01-01\n"},
		{Match: "pluto ls", Stdout: "ID\tPROJECT/BRANCH\n"},
	}
}

func TestDeviceAddLsRm(t *testing.T) {
	cfg := deviceConfig(t)
	testssh.StartScript(t, healthyRules()...)

	code, out, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"neptuno  connected", "siddhant@home.example.net", "/usr/local/bin/pluto", "v0.1.5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device add output = %q, want %q", out, want)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); err != nil {
		t.Fatalf("registry file not written under XDG config: %v", err)
	}

	code, out, errOut = runCLI(t, "device", "ls")
	if code != 0 {
		t.Fatalf("device ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"neptuno", "siddhant@neptuno", "connected", "v0.1.5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device ls output = %q, want %q", out, want)
		}
	}

	code, out, errOut = runCLI(t, "device", "rm", "neptuno")
	if code != 0 {
		t.Fatalf("device rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "removed") || !strings.Contains(out, "neptuno") {
		t.Fatalf("device rm output = %q, want a removal note", out)
	}

	code, out, _ = runCLI(t, "device", "ls")
	if code != 0 || strings.Contains(out, "neptuno") {
		t.Fatalf("device ls after rm = %q, want the device gone", out)
	}
}

func TestDeviceAddWarnsButSavesWhenTheTargetDoesNotAnswer(t *testing.T) {
	deviceConfig(t)
	rules := healthyRules()
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "Permission denied (publickey).\n", Exit: 255}
	testssh.StartScript(t, rules...)

	code, out, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add must not fail on a failed probe; exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved device") || !strings.Contains(out, "offline") {
		t.Fatalf("device add output = %q, want a save note and offline status", out)
	}
	if !strings.Contains(errOut, "next:") {
		t.Fatalf("device add stderr = %q, want a next step", errOut)
	}

	code, out, _ = runCLI(t, "device", "ls")
	if code != 0 || !strings.Contains(out, "neptuno") || !strings.Contains(out, "offline") {
		t.Fatalf("device ls after a warned add = %q, want the saved device offline", out)
	}
}

func TestDeviceDoctorJSON(t *testing.T) {
	deviceConfig(t)
	testssh.StartScript(t, healthyRules()...)
	if code, _, errOut := runCLI(t, "device", "add", "neptuno", "siddhant@neptuno"); code != 0 {
		t.Fatalf("add exit = %d, stderr: %s", code, errOut)
	}
	// doctor --offline avoids real DNS/TCP while still proving the stable
	// report shape and hop order.
	code, out, errOut := runCLI(t, "device", "doctor", "neptuno", "--offline", "--json")
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr: %s", code, errOut)
	}
	var report devices.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode doctor json: %v\n%s", err, out)
	}
	if len(report.Hops) != 14 {
		t.Fatalf("hops = %d, want 14: %+v", len(report.Hops), report.Hops)
	}
	if report.Hops[0].Name != "local.ssh" || report.Hops[13].Name != "image" {
		t.Fatalf("hop order = %+v, want local.ssh first and image last", report.Hops)
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
		{"device", "add", "neptuno", "siddhant@neptuno", "--port", "70000"},
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
	for _, args := range [][]string{{"device"}, {"device", "add"}, {"device", "rm"}, {"device", "bogus"}} {
		code, _, errOut := runCLI(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2 (stderr %q)", args, code, errOut)
		}
		if errOut == "" {
			t.Fatalf("%v printed no usage or error", args)
		}
	}
}
