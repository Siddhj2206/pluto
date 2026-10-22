package devices_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/testssh"
)

// The one-password path delegates to the standard ssh-copy-id tool and passes
// the same route overrides as every other ssh call.
func TestCopyIDUsesSSHCopyID(t *testing.T) {
	log := testssh.StartCopyID(t)
	p := devices.Profile{
		Nickname: "neptuno", Destination: "siddhant@neptuno",
		Port: 2222, IdentityFile: "~/.ssh/id_ed25519", ProxyJump: "bastion",
	}
	var out, errOut bytes.Buffer
	if err := devices.CopyID(context.Background(), "", p, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("CopyID: %v", err)
	}
	args := testssh.LoggedArgs(t, log)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p 2222", "-i ~/.ssh/id_ed25519", "-J bastion", "siddhant@neptuno"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ssh-copy-id args = %q, want %q", args, want)
		}
	}
}

func TestCopyIDReportsAMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := devices.CopyID(context.Background(), "", devices.Profile{Nickname: "n", Destination: "n"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, devices.ErrCopyIDUnavailable) {
		t.Fatalf("CopyID error = %v, want ErrCopyIDUnavailable", err)
	}
}

func TestKeyInstallHints(t *testing.T) {
	p := devices.Profile{Nickname: "neptuno", Destination: "siddhant@neptuno", Port: 2222, IdentityFile: "~/.ssh/id_ed25519"}
	if got := devices.CopyIDCommand(p); !strings.Contains(got, "ssh-copy-id -p 2222") {
		t.Fatalf("CopyIDCommand = %q", got)
	}
	ps := devices.PowerShellKeyInstall(p)
	for _, want := range []string{"$env:USERPROFILE", "ssh -p 2222", "cat >> ~/.ssh/authorized_keys"} {
		if !strings.Contains(ps, want) {
			t.Fatalf("PowerShellKeyInstall = %q, want %q", ps, want)
		}
	}
}

// The cache is disposable and secret-free; a corrupt file is an error, not a
// silent fresh device.
func TestCacheRoundTripsAndRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	c := devices.Cache{
		Nickname: "neptuno",
		Status:   devices.StatusConnected,
		Resolved: devices.Resolved{Destination: "neptuno", Host: "home.example.net", User: "sid", Port: 2222},
		Remote:   devices.Remote{OS: "linux", Arch: "amd64", Path: "/home/sid/.local/bin/pluto", Version: "v0.1.5", Protocol: 1},
		Daemon:   devices.Daemon{Reachable: true},
	}
	if err := c.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := devices.LoadCache(dir, "neptuno")
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if got.Status != devices.StatusConnected || got.Remote.Path != "/home/sid/.local/bin/pluto" || got.Resolved.Port != 2222 {
		t.Fatalf("cache = %+v, want the saved facts", got)
	}
	data, err := os.ReadFile(devices.CachePath(dir, "neptuno"))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	for _, forbidden := range []string{"password", "PRIVATE KEY", "token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("cache contains %q: %s", forbidden, data)
		}
	}

	if missing, err := devices.LoadCache(dir, "missing"); err != nil || missing.Status != devices.StatusUnverified {
		t.Fatalf("missing cache = %+v, %v; want unverified", missing, err)
	}

	if err := os.WriteFile(devices.CachePath(dir, "broken"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write broken cache: %v", err)
	}
	if _, err := devices.LoadCache(dir, "broken"); err == nil {
		t.Fatal("corrupt cache should fail")
	}

	if err := devices.RemoveCache(dir, "neptuno"); err != nil {
		t.Fatalf("RemoveCache: %v", err)
	}
	if _, err := os.Stat(devices.CachePath(dir, "neptuno")); !os.IsNotExist(err) {
		t.Fatalf("cache still exists: %v", err)
	}
	if err := devices.RemoveCache(dir, "missing"); err != nil {
		t.Fatalf("RemoveCache missing: %v", err)
	}
}
