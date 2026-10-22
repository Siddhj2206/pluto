package remotecli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/testssh"
)

// The one-password path: on a terminal, a refused key offers ssh-copy-id once,
// then re-probes and records the connected device. A test process has no pty,
// so the terminal seam is replaced for this test.
func TestDeviceAddInstallsAKeyOnATerminal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("PLUTO_DEVICE_CACHE_DIR", filepath.Join(dir, "cache"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("USERPROFILE", filepath.Join(dir, "home"))

	marker := filepath.Join(dir, "key-installed")
	t.Setenv("FAKE_SSH_MARKER", marker)
	t.Setenv("FAKE_SSH_COPYID_MARKER", marker)
	testssh.StartCopyID(t)
	calls := testssh.CallLog(t)
	testssh.StartScript(t,
		testssh.Rule{Match: "-G", Stdout: "host home.example.net\nuser siddhant\nport 22\n"},
		testssh.Rule{Match: "uname -s", Stdout: "os=Linux\narch=x86_64\nshell=/bin/sh\n"},
		testssh.Rule{Match: "command -v pluto", Stdout: "/home/sid/.local/bin/pluto\n"},
		testssh.Rule{Match: "version --json", Stdout: `{"schema_version":1,"version":"v0.1.5","protocol":1,"capabilities":["json"]}` + "\n"},
		testssh.Rule{Match: "image ls", Stdout: "VERSION\tSNAPSHOT\n0.1.5\t2026-01-01\n"},
		testssh.Rule{Match: "pluto ls", Stdout: "ID\tPROJECT/BRANCH\n"},
		// The batch probe is refused until a key is installed; the interactive
		// probe models a password-only account.
		testssh.Rule{Match: "BatchMode=yes", Stderr: "siddhant@neptuno: Permission denied (publickey,password).\n", Exit: 255},
		testssh.Rule{Match: "printf ok", Stdout: "ok"},
	)

	old := deviceIsTerminal
	deviceIsTerminal = func() bool { return true }
	t.Cleanup(func() { deviceIsTerminal = old })

	var out, errOut bytes.Buffer
	code := RunDevice([]string{"add", "neptuno", "siddhant@neptuno"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("add exit = %d, stderr: %s", code, errOut.String())
	}
	for _, want := range []string{"installing an ssh key", "ssh-copy-id asks for the account password"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr = %q, want %q", errOut.String(), want)
		}
	}
	if !strings.Contains(out.String(), "neptuno  connected") {
		t.Fatalf("stdout = %q, want a connected card", out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("ssh-copy-id did not install the key: %v", err)
	}
	// The successful probe after the copy is batch mode, not another password
	// prompt: a password-only host must end with the key installed.
	batchProbes := 0
	for _, call := range testssh.LoggedCalls(t, calls) {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "printf ok") && strings.Contains(joined, "BatchMode=yes") {
			batchProbes++
		}
	}
	if batchProbes < 2 {
		t.Fatalf("batch probes = %d, want one before and one after the key install", batchProbes)
	}
}
