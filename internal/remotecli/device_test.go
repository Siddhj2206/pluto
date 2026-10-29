package remotecli_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
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

// resolvedRule is the `ssh -G` answer for a route.
func resolvedRule(host string, port int, proxy string) testssh.Rule {
	if proxy == "" {
		proxy = "none"
	}
	return testssh.Rule{
		Match: "-G",
		Stdout: fmt.Sprintf("host %s\nuser siddhant\nport %d\nproxyjump %s\nidentityfile ~/.ssh/id_ed25519\n",
			host, port, proxy),
	}
}

// healthyRules answers every step of the add preflight for a Linux host with
// a reachable daemon and one imported image.
func healthyRules(host string, port int) []testssh.Rule {
	return []testssh.Rule{
		resolvedRule(host, port, ""),
		{Match: "printf ok", Stdout: "ok"},
		{Match: "uname -s", Stdout: "os=Linux\narch=x86_64\nshell=/bin/sh\nhome=/home/sid\n"},
		{Match: "command -v pluto", Stdout: "/home/sid/.local/bin/pluto\n"},
		{Match: "version --json", Stdout: `{"schema_version":1,"version":"v0.1.5","protocol":1,"capabilities":["json","device","doctor"]}` + "\n"},
		{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\tKERNEL\tROOTFS\n0.1.5\t2026-01-01\tabc\tdef\n"},
		{Match: "pluto ls", Stdout: "ID\tPROJECT/BRANCH\n"},
	}
}

func cachePath(t *testing.T, nickname string) string {
	t.Helper()
	dir, err := devices.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	return devices.CachePath(dir, nickname)
}

// add/ls/rm round-trips a device through the registry, the preflight, and the
// disposable cache.
func TestRunDeviceAddLsRm(t *testing.T) {
	cfg := deviceConfig(t)
	log := testssh.StartScript(t, healthyRules("home.example.net", 2222)...)
	calls := testssh.CallLog(t)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"neptuno  connected",
		"route     siddhant@home.example.net:2222",
		"/home/sid/.local/bin/pluto",
		"v0.1.5",
		"daemon",
		"image",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("device add output = %q, want %q", out, want)
		}
	}

	// The first call is ssh -G; the route is resolved before any connection.
	first := testssh.LoggedCalls(t, calls)[0]
	if !reflect.DeepEqual(first[:2], []string{"-G", "--"}) {
		t.Fatalf("first ssh call = %q, want ssh -G", first)
	}
	args := testssh.LoggedArgs(t, log)
	for _, want := range []string{"-T", "BatchMode=yes", "ConnectTimeout=10", "ServerAliveInterval=15", "StrictHostKeyChecking=yes", "sh", "-c"} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatalf("last ssh args = %q, want %q", args, want)
		}
	}

	registry := filepath.Join(cfg, "pluto", "devices.toml")
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry file not written under the user config dir: %v", err)
	}
	for _, want := range []string{"version = 1", "[devices.neptuno]", `destination = "siddhant@neptuno"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("registry = %q, want %q", data, want)
		}
	}
	var cached devices.Cache
	cacheData, err := os.ReadFile(cachePath(t, "neptuno"))
	if err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	if err := json.Unmarshal(cacheData, &cached); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if cached.Status != devices.StatusConnected || cached.Remote.Path != "/home/sid/.local/bin/pluto" || cached.LastOK == nil {
		t.Fatalf("cache = %+v, want connected with path and last_ok", cached)
	}
	for _, secret := range []string{"password", "private", "PRIVATE"} {
		if strings.Contains(string(cacheData), secret) {
			t.Fatalf("cache contains %q: %s", secret, cacheData)
		}
	}

	code, out, errOut = runDevice(t, "ls")
	if code != 0 {
		t.Fatalf("device ls exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"NICKNAME", "DESTINATION", "ROUTE", "STATUS", "neptuno", "siddhant@neptuno", "connected", "v0.1.5"} {
		if !strings.Contains(out, want) {
			t.Fatalf("device ls output = %q, want %q", out, want)
		}
	}

	code, out, errOut = runDevice(t, "rm", "neptuno")
	if code != 0 {
		t.Fatalf("device rm exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "removed") || !strings.Contains(out, "neptuno") {
		t.Fatalf("device rm output = %q, want a removal note", out)
	}
	if _, err := os.Stat(cachePath(t, "neptuno")); !os.IsNotExist(err) {
		t.Fatalf("device rm left the cache: %v", err)
	}

	code, out, _ = runDevice(t, "ls")
	if code != 0 || strings.Contains(out, "neptuno") {
		t.Fatalf("device ls after rm = %q, want the device gone", out)
	}
}

// A reachable host with a refused key is saved as offline, never connected,
// and the no-TTY path prints the exact key-install command instead of
// hanging.
func TestRunDeviceAddSavesAnUnreachableDeviceAsOffline(t *testing.T) {
	deviceConfig(t)
	rules := healthyRules("home.example.net", 2222)
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "siddhant@neptuno: Permission denied (publickey).\n", Exit: 255}
	testssh.StartScript(t, rules...)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add must not fail on an unreachable host; exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno  offline") {
		t.Fatalf("device add output = %q, want the offline status", out)
	}
	for _, want := range []string{"no terminal is available", "ssh-copy-id siddhant@neptuno", "PowerShell"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("device add stderr = %q, want %q", errOut, want)
		}
	}

	code, out, _ = runDevice(t, "ls")
	if code != 0 || !strings.Contains(out, "offline") {
		t.Fatalf("device ls = %q, want the saved device offline", out)
	}
}

// Invalid input is a usage error before anything is stored.
func TestRunDeviceAddRejectsInvalidInput(t *testing.T) {
	cfg := deviceConfig(t)
	for _, args := range [][]string{
		{"add", "two words", "siddhant@neptuno"},
		{"add", "neptuno", ""},
		{"add", "neptuno", "-oProxyCommand=boom"},
		{"add", "neptuno", "siddhant@neptuno", "--port", "70000"},
		{"add", "neptuno", "siddhant@neptuno", "--trust", "no"},
	} {
		code, _, errOut := runDevice(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want a usage error 2 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(errOut, "invalid") || !strings.Contains(errOut, "usage") {
			t.Fatalf("%v stderr = %q, want an invalid-argument error and usage", args, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); !os.IsNotExist(err) {
		t.Fatalf("invalid input wrote a registry: %v", err)
	}
}

// The device tree rejects bad shapes with exit 2.
func TestRunDeviceUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"add"}, {"rm"}, {"bogus"}, {"doctor", "a", "b"}} {
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
		{[]string{"doctor", "-h"}, "Check the full connection path"},
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

// --dry-run resolves and prints the exact argv but writes nothing.
func TestRunDeviceAddDryRunWritesNothing(t *testing.T) {
	cfg := deviceConfig(t)
	log := testssh.StartScript(t, resolvedRule("home.example.net", 2222, "bastion"))

	code, out, errOut := runDevice(t, "add", "neptuno", "neptuno", "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"neptuno -> siddhant@home.example.net:2222 via bastion", "ssh:", "checks:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output = %q, want %q", out, want)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg, "pluto", "devices.toml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote a registry: %v", err)
	}
	args := testssh.LoggedArgs(t, log)
	if !reflect.DeepEqual(args[:2], []string{"-G", "--"}) {
		t.Fatalf("dry-run ssh args = %q, want only ssh -G", args)
	}
}

// --no-probe saves a profile without contacting the host.
func TestRunDeviceAddNoProbeSavesUnverified(t *testing.T) {
	deviceConfig(t)
	log := testssh.Start(t, "", "", 0)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno", "--no-probe")
	if code != 0 {
		t.Fatalf("--no-probe exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno  unverified") {
		t.Fatalf("output = %q, want unverified", out)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("--no-probe ran ssh: %v", err)
	}
}

// Missing OpenSSH fails cleanly with the install commands, and still leaves
// the profile saved so ls can explain the state.
func TestRunDeviceAddMissingSSH(t *testing.T) {
	deviceConfig(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PLUTO_SSH", "")

	code, _, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"E_SSH_MISSING", "install OpenSSH", "PLUTO_SSH"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// Explicit overrides are persisted and used for both ssh -G and the route.
func TestRunDeviceAddPersistsRouteOverrides(t *testing.T) {
	cfg := deviceConfig(t)
	testssh.StartScript(t, healthyRules("home.example.net", 2222)...)

	code, _, errOut := runDevice(t, "add", "neptuno", "neptuno",
		"--port", "2222", "--identity", "~/.ssh/id_ed25519", "--proxy-jump", "bastion",
		"--trust", "strict", "--control-master")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	data, err := os.ReadFile(filepath.Join(cfg, "pluto", "devices.toml"))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, want := range []string{"port = 2222", `identity_file = "~/.ssh/id_ed25519"`, `proxy_jump = "bastion"`, `trust = "strict"`, "control_master = true"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("registry = %q, want %q", data, want)
		}
	}
}

// A legacy destination-only entry still resolves and lists, and keeps its
// learned path.
func TestRunDeviceLegacyRegistryStillWorks(t *testing.T) {
	cfg := deviceConfig(t)
	writeDevices(t, cfg, "[device]\nneptuno = \"siddhant@neptuno\"\n\n[remote]\nneptuno = \"/usr/local/bin/pluto\"\n")
	log := testssh.Start(t, "", "", 0)

	code, out, _ := runDevice(t, "ls")
	if code != 0 || !strings.Contains(out, "neptuno") || !strings.Contains(out, "siddhant@neptuno") {
		t.Fatalf("device ls = %q, want the legacy entry", out)
	}

	var buf, errOut bytes.Buffer
	if code := remotecli.Run("neptuno", []string{"ls"}, &buf, &errOut); code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut.String())
	}
	want := wantRemote("siddhant@neptuno", "/usr/local/bin/pluto ls")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the legacy learned path %q", got, want)
	}
}

// A strict profile (the default) never accepts an unknown host key silently.
func TestRunDeviceAddStrictTrustRejectsAnUnknownHostKey(t *testing.T) {
	deviceConfig(t)
	rules := healthyRules("home.example.net", 2222)
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "Host key verification failed.\n", Exit: 255}
	testssh.StartScript(t, rules...)

	code, out, errOut := runDevice(t, "add", "neptuno", "neptuno")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "neptuno  offline") {
		t.Fatalf("output = %q, want offline", out)
	}
	for _, want := range []string{"E_HOSTKEY_UNKNOWN", "--trust accept-new"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A first-use host key is accepted only when the profile asks for it.
func TestRunDeviceAddAcceptNewIsAnExplicitFirstUseOptIn(t *testing.T) {
	deviceConfig(t)
	log := testssh.StartScript(t, healthyRules("home.example.net", 2222)...)

	code, _, errOut := runDevice(t, "add", "neptuno", "neptuno", "--trust", "accept-new")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if got := strings.Join(testssh.LoggedArgs(t, log), " "); !strings.Contains(got, "StrictHostKeyChecking=accept-new") {
		t.Fatalf("ssh argv = %q, want the explicit accept-new option", got)
	}
}

// doctor checks the ordered hops and exits 0 for a healthy route. A local TCP
// listener stands in for sshd so the network hops are real but hermetic.
func TestRunDeviceDoctorReportsOrderedHops(t *testing.T) {
	cfg := deviceConfig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	calls := testssh.CallLog(t)
	testssh.StartScript(t, healthyRules("127.0.0.1", port)...)

	code, out, errOut := runDevice(t, "doctor", "neptuno", "--json")
	if code != 0 {
		t.Fatalf("doctor exit = %d, stderr: %s", code, errOut)
	}
	var report devices.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	if !report.OK || report.Status != devices.StatusConnected {
		t.Fatalf("report = %+v, want connected", report)
	}
	wantNames := []string{"local.ssh", "config.resolve", "dns", "tcp", "proxy", "hostkey", "auth", "shell", "pluto.lookup", "pluto.version", "protocol", "daemon", "state", "image"}
	var got []string
	for _, hop := range report.Hops {
		got = append(got, hop.Name)
	}
	if !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("hops = %q, want %q", got, wantNames)
	}

	// The host-key probe is strict, whatever the profile says.
	for _, call := range testssh.LoggedCalls(t, calls) {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "printf ok") {
			if !strings.Contains(joined, "StrictHostKeyChecking=yes") {
				t.Fatalf("doctor probe = %q, want strict host-key checking", call)
			}
			if strings.Contains(joined, "accept-new") || strings.Contains(joined, "StrictHostKeyChecking=no") {
				t.Fatalf("doctor probe = %q, must not weaken host-key checking", call)
			}
		}
	}
}

// A failed hop carries a stable code and an exact next step, and doctor exits
// non-zero.
func TestRunDeviceDoctorFailureNamesTheCodeAndFix(t *testing.T) {
	cfg := deviceConfig(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	rules := healthyRules("127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
	rules[1] = testssh.Rule{Match: "printf ok", Stderr: "siddhant@neptuno: Permission denied (publickey).\n", Exit: 255}
	testssh.StartScript(t, rules...)

	code, out, errOut := runDevice(t, "doctor", "neptuno")
	if code != 1 {
		t.Fatalf("doctor exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"E_AUTH", "next:", "ssh-add -l", "ssh-copy-id"} {
		if !strings.Contains(out+errOut, want) {
			t.Fatalf("output = %q / %q, want %q", out, errOut, want)
		}
	}
}

// --offline reads the cache and marks network hops skipped without ssh.
func TestRunDeviceDoctorOfflineUsesTheCache(t *testing.T) {
	deviceConfig(t)
	testssh.StartScript(t, healthyRules("127.0.0.1", 22)...)
	if code, _, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno"); code != 0 {
		t.Fatalf("add exit = %d, stderr: %s", code, errOut)
	}
	code, out, errOut := runDevice(t, "doctor", "neptuno", "--offline", "--json")
	if code != 0 {
		t.Fatalf("offline doctor exit = %d, stderr: %s", code, errOut)
	}
	var report devices.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, hop := range report.Hops {
		if hop.Name == "dns" || hop.Name == "tcp" {
			if hop.Status != devices.HopSkipped {
				t.Fatalf("offline hop %s = %q, want skipped", hop.Name, hop.Status)
			}
		}
	}
}

// --print-ssh prints the transport argv with the identity path redacted.
func TestRunDevicePrintSSHRedactsIdentity(t *testing.T) {
	cfg := deviceConfig(t)
	reg, err := devices.Open(filepath.Join(cfg, "pluto", "devices.toml"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := reg.AddProfile(devices.Profile{Nickname: "neptuno", Destination: "neptuno", IdentityFile: "/home/sid/.ssh/id_ed25519", Trust: devices.TrustStrict}); err != nil {
		t.Fatalf("add profile: %v", err)
	}
	testssh.Start(t, "", "", 0)

	code, out, errOut := runDevice(t, "doctor", "neptuno", "--print-ssh")
	if code != 0 {
		t.Fatalf("print-ssh exit = %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{"-T", "StrictHostKeyChecking=yes", "<identity>", "-- neptuno"} {
		if !strings.Contains(out, want) {
			t.Fatalf("print-ssh = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "/home/sid/.ssh/id_ed25519") {
		t.Fatalf("print-ssh leaked the identity path: %q", out)
	}
}

// Two saved devices make a bare `doctor` ambiguous.
func TestRunDeviceDoctorNeedsANicknameWhenAmbiguous(t *testing.T) {
	cfg := deviceConfig(t)
	writeDevices(t, cfg, "[device]\nneptuno = \"siddhant@neptuno\"\nceres = \"siddhant@ceres\"\n")
	code, _, errOut := runDevice(t, "doctor")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "nickname") {
		t.Fatalf("stderr = %q, want a nickname hint", errOut)
	}
}

// The cache is keyed by nickname and holds no secrets.
func TestDeviceCacheHasNoSecrets(t *testing.T) {
	deviceConfig(t)
	testssh.StartScript(t, healthyRules("127.0.0.1", 22)...)
	if code, _, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno"); code != 0 {
		t.Fatalf("add exit = %d, stderr: %s", code, errOut)
	}
	data, err := os.ReadFile(cachePath(t, "neptuno"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	for _, forbidden := range []string{"password", "passphrase", "token", "secret", "PRIVATE KEY"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Fatalf("cache contains %q: %s", forbidden, data)
		}
	}
	if !strings.Contains(string(data), strconv.Itoa(devices.CacheSchemaVersion)) {
		t.Fatalf("cache has no schema version: %s", data)
	}
}

// --json is machine-readable: no human save line, and the report decodes.
func TestRunDeviceAddJSONIsMachineReadable(t *testing.T) {
	deviceConfig(t)
	testssh.StartScript(t, healthyRules("home.example.net", 22)...)

	code, out, errOut := runDevice(t, "add", "neptuno", "siddhant@neptuno", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if strings.Contains(out, "saved device") {
		t.Fatalf("--json output = %q, want no human save line", out)
	}
	var report devices.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, out)
	}
	if report.Status != devices.StatusConnected || report.Nickname != "neptuno" {
		t.Fatalf("report = %+v, want connected neptuno", report)
	}
}
