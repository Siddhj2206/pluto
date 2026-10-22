package devices_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/shquote"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// A legacy destination-only registry keeps working: the [device] and [remote]
// tables load as a profile with the documented defaults.
func TestLegacyRegistryLoadsAsAProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pluto", "devices.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "[device]\nneptuno = \"siddhant@neptuno\"\n\n[remote]\nneptuno = \"/usr/local/bin/pluto\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, ok := reg.Profile("neptuno")
	if !ok {
		t.Fatal("legacy device did not load")
	}
	if p.Destination != "siddhant@neptuno" || p.RemotePath != "/usr/local/bin/pluto" {
		t.Fatalf("profile = %+v, want the legacy destination and path", p)
	}
	if p.Platform != devices.PlatformAuto || p.Auth != devices.AuthAuto || p.Trust != devices.TrustStrict || p.ControlMaster {
		t.Fatalf("defaults = %+v, want auto/auto/strict/off", p)
	}
}

// A richer profile round-trips through the new [devices.<name>] table, and a
// route change clears the learned path.
func TestProfileRoundTripsAndRouteChangeClearsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pluto", "devices.toml")
	reg, err := devices.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p := devices.Profile{
		Nickname:      "neptuno",
		Destination:   "neptuno",
		Port:          2222,
		IdentityFile:  "~/.ssh/id_ed25519",
		ProxyJump:     "bastion",
		RemotePath:    "/home/sid/.local/bin/pluto",
		Platform:      devices.PlatformLinux,
		Auth:          devices.AuthKey,
		Trust:         devices.TrustStrict,
		ControlMaster: true,
	}
	if err := reg.AddProfile(p); err != nil {
		t.Fatalf("AddProfile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	for _, want := range []string{"version = 1", "[devices.neptuno]", "port = 2222", `proxy_jump = "bastion"`, "control_master = true"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("registry = %q, want %q", data, want)
		}
	}

	reopened, err := devices.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, _ := reopened.Profile("neptuno")
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("profile = %+v, want %+v", got, p)
	}

	// The same nickname on a new route loses the old host's learned path.
	p.Destination = "neptuno.example.net"
	if err := reopened.AddProfile(p); err != nil {
		t.Fatalf("AddProfile new route: %v", err)
	}
	got, _ = reopened.Profile("neptuno")
	if got.RemotePath != devices.RemotePathAuto {
		t.Fatalf("remote path after route change = %q, want auto", got.RemotePath)
	}
}

func TestProfileRejectsBadValues(t *testing.T) {
	reg, err := devices.Open(filepath.Join(t.TempDir(), "devices.toml"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cases := []devices.Profile{
		{Nickname: "n", Destination: "host", Port: 70000},
		{Nickname: "n", Destination: "host", Trust: "no"},
		{Nickname: "n", Destination: "host", Auth: "password"},
		{Nickname: "n", Destination: "host", Platform: "darwin"},
		{Nickname: "n", Destination: "host", RemotePath: "relative/pluto"},
		{Nickname: "n", Destination: "host", IdentityFile: "two words"},
		{Nickname: "n", Destination: "-oProxyCommand=boom"},
	}
	for _, p := range cases {
		if err := reg.AddProfile(p); err == nil {
			t.Fatalf("AddProfile(%+v) succeeded, want an error", p)
		}
	}
}

// The transport never weakens host-key checking and never relies on the
// login shell: -T, bounded connect, keepalives, and one sh -c command.
func TestTransportPolicy(t *testing.T) {
	p := devices.Profile{Nickname: "n", Destination: "neptuno"}
	args := p.SSHArgs(devices.SSHOptions{BatchMode: true})
	want := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=yes",
		"--", "neptuno",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("SSHArgs = %q, want %q", args, want)
	}
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("SSHArgs = %q, must not contain %q", args, forbidden)
		}
	}

	strict := p.SSHArgs(devices.SSHOptions{BatchMode: true, Trust: devices.TrustStrict, ConnectTimeout: 7 * time.Second})
	if !strings.Contains(strings.Join(strict, " "), "StrictHostKeyChecking=yes") {
		t.Fatalf("strict SSHArgs = %q, want StrictHostKeyChecking=yes", strict)
	}
	if !strings.Contains(strings.Join(strict, " "), "ConnectTimeout=7") {
		t.Fatalf("strict SSHArgs = %q, want the bounded connect", strict)
	}

	interactive := p.SSHArgs(devices.SSHOptions{BatchMode: false, TTY: true})
	if !strings.Contains(strings.Join(interactive, " "), "-tt") {
		t.Fatalf("interactive SSHArgs = %q, want -tt", interactive)
	}
	if strings.Contains(strings.Join(interactive, " "), "BatchMode") {
		t.Fatalf("interactive SSHArgs = %q, must allow a prompt", interactive)
	}
}

// Explicit route overrides become -p/-i/-J; an empty override lets
// ~/.ssh/config win.
func TestTransportOverrides(t *testing.T) {
	p := devices.Profile{
		Nickname: "n", Destination: "neptuno",
		Port: 2222, IdentityFile: "~/.ssh/id_ed25519", ProxyJump: "bastion",
	}
	joined := strings.Join(p.SSHArgs(devices.SSHOptions{BatchMode: true}), " ")
	for _, want := range []string{"-p 2222", "-i ~/.ssh/id_ed25519", "-J bastion"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SSHArgs = %q, want %q", joined, want)
		}
	}
}

// ControlMaster is opt-in and POSIX-only.
func TestControlMasterIsOptIn(t *testing.T) {
	off := devices.Profile{Nickname: "n", Destination: "neptuno"}
	if strings.Contains(strings.Join(off.SSHArgs(devices.SSHOptions{BatchMode: true}), " "), "ControlMaster") {
		t.Fatal("ControlMaster is on by default")
	}
	on := devices.Profile{Nickname: "n", Destination: "neptuno", ControlMaster: true}
	args := strings.Join(on.SSHArgs(devices.SSHOptions{BatchMode: true}), " ")
	if runtimeIsWindows() {
		if strings.Contains(args, "ControlMaster") {
			t.Fatalf("Windows SSHArgs = %q, want no ControlMaster", args)
		}
	} else if !strings.Contains(args, "ControlMaster=auto") {
		t.Fatalf("SSHArgs = %q, want opt-in ControlMaster", args)
	}
}

func runtimeIsWindows() bool {
	return runtime.GOOS == "windows"
}

// One quoted remote command through sh -c; every word is quoted once.
func TestRemoteCommandIsOneShCString(t *testing.T) {
	got := devices.RemoteCommand("/opt/my tools/pluto", []string{"run", "box", "--", "sh", "-c", "echo hi"})
	want := []string{"sh", "-c", shquote.QuoteAlways(`'/opt/my tools/pluto' run box -- sh -c 'echo hi'`)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RemoteCommand = %q, want %q", got, want)
	}
}

// Resolve asks ssh -G and keeps the alias as the profile's destination.
func TestResolveReadsSSHConfig(t *testing.T) {
	testssh.StartScript(t, testssh.Rule{
		Match:  "-G",
		Stdout: "host home.example.net\nuser siddhant\nport 2222\nproxyjump bastion\nidentityfile ~/.ssh/id_ed25519\n",
	})
	ssh, err := devices.FindSSH()
	if err != nil {
		t.Fatalf("FindSSH: %v", err)
	}
	p := devices.Profile{Nickname: "neptuno", Destination: "neptuno"}
	r, err := devices.Resolve(context.Background(), ssh, p)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Host != "home.example.net" || r.User != "siddhant" || r.Port != 2222 || r.ProxyJump != "bastion" {
		t.Fatalf("resolved = %+v, want the ssh -G route", r)
	}
	if r.Display() != "siddhant@home.example.net:2222 via bastion" {
		t.Fatalf("Display = %q", r.Display())
	}
	if p.Destination != "neptuno" {
		t.Fatalf("Resolve changed the profile destination to %q", p.Destination)
	}
}
