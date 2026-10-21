package devices_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
	"github.com/Siddhj2206/pluto/internal/testssh"
	"golang.org/x/crypto/ssh"
)

// setHome points the key lookup at a fresh home directory.
func setHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

// A fresh machine gets an ed25519 keypair; the next call reuses it so a retry
// does not install a different key.
func TestPublicKeyGeneratesThenReusesADefaultKey(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	first, err := devices.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if !strings.HasPrefix(first, "ssh-ed25519 ") {
		t.Fatalf("public key = %q, want an ssh-ed25519 key", first)
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("private key not written: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		t.Fatalf("generated key is not a valid OpenSSH private key: %v", err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); got != first {
		t.Fatalf("public key file = %q, want the key derived from the private half %q", first, got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatalf("stat private key: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("private key mode = %o, want 600", got)
		}
	}
	second, err := devices.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey again: %v", err)
	}
	if second != first {
		t.Fatalf("second call generated a different key:\n%s\n%s", first, second)
	}
}

// Locate returns the absolute path the host reported.
func TestLocateReturnsTheHostPath(t *testing.T) {
	log := testssh.Start(t, "/home/sid/.local/bin/pluto\n", "", 0)
	path, err := devices.Locate(context.Background(), "siddhant@neptuno")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if path != "/home/sid/.local/bin/pluto" {
		t.Fatalf("path = %q, want the host path", path)
	}
	args := testssh.LoggedArgs(t, log)
	script := args[len(args)-1]
	if !strings.HasPrefix(script, "if command -v pluto") || !strings.Contains(script, `"$HOME/.local/bin/pluto"`) {
		t.Fatalf("remote script = %q, want the PATH and ~/.local/bin probe", script)
	}
	if strings.HasPrefix(script, "'") {
		t.Fatalf("remote script was quoted for the remote shell: %q", script)
	}
	if !strings.Contains(strings.Join(args, " "), "BatchMode=yes") {
		t.Fatalf("Locate args = %q, want the non-interactive boundary", args)
	}
}

// A refused key is classified so device add knows it can offer a password.
func TestLocateClassifiesAPermissionDenial(t *testing.T) {
	testssh.Start(t, "", "siddhant@neptuno: Permission denied (publickey,password).\n", 255)
	_, err := devices.Locate(context.Background(), "siddhant@neptuno")
	if !errors.Is(err, devices.ErrAuth) {
		t.Fatalf("Locate error = %v, want ErrAuth", err)
	}
}

// A host with no pluto answers the login but exits 127 from the probe.
func TestLocateNamesAMissingHostBinary(t *testing.T) {
	testssh.Start(t, "", "", 127)
	_, err := devices.Locate(context.Background(), "siddhant@neptuno")
	if !errors.Is(err, devices.ErrPlutoMissing) {
		t.Fatalf("Locate error = %v, want ErrPlutoMissing", err)
	}
}

// The key travels on stdin, which ssh forwards to the remote script, and the
// install call must not be in BatchMode: this is the one command allowed to
// ask for a password.
func TestInstallKeySendsThePublicKeyOnStandardInput(t *testing.T) {
	log := testssh.Start(t, "", "", 0)
	stdinLog := testssh.StdinLog(t)
	const pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest pluto\n"
	if err := devices.InstallKey("siddhant@neptuno", strings.TrimSpace(pub), io.Discard, io.Discard); err != nil {
		t.Fatalf("InstallKey: %v", err)
	}
	got, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if string(got) != pub {
		t.Fatalf("stdin = %q, want the public key", got)
	}
	args := testssh.LoggedArgs(t, log)
	joined := strings.Join(args, " ")
	for _, want := range []string{"authorized_keys", "StrictHostKeyChecking=accept-new", "siddhant@neptuno"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("InstallKey args = %q, want %q", args, want)
		}
	}
	if strings.Contains(joined, "BatchMode") {
		t.Fatalf("InstallKey args = %q, must not be in BatchMode", args)
	}
}
