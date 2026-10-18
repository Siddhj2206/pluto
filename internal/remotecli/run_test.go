package remotecli_test

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

// The ssh options fence the connection: no prompt, a bounded connect, and a
// first-use host key accepted while a changed key still fails. They precede
// the `--` fence and the target.
var sshOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "StrictHostKeyChecking=accept-new",
}

// Run streams the remote streams back and returns the remote exit code.
func TestRunStreamsRemoteOutputAndReturnsExitCode(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "remote says hi\n", "remote says hmm\n", 7)

	var out, errOut bytes.Buffer
	code := remotecli.Run("sid@host", []string{"status", "mybox"}, &out, &errOut)
	if code != 7 {
		t.Fatalf("exit = %d, want the remote exit code 7 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "remote says hi") {
		t.Fatalf("stdout = %q, want the remote output streamed", out.String())
	}
	if !strings.Contains(errOut.String(), "remote says hmm") {
		t.Fatalf("stderr = %q, want the remote error streamed", errOut.String())
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "pluto", "status", "mybox")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// A raw user@host target works without a registry entry.
func TestRunRawTargetNeedsNoRegistry(t *testing.T) {
	cfg := deviceConfig(t)
	corruptDevices(t, cfg)
	log := startFakeSSH(t, "", "", 0)

	var out, errOut bytes.Buffer
	if code := remotecli.Run("sid@neptuno", []string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@neptuno", "pluto", "version")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// A saved nickname resolves to its target without touching ssh options.
func TestRunResolvesASavedNickname(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := startFakeSSH(t, "", "", 0)

	var out, errOut bytes.Buffer
	if code := remotecli.Run("neptuno", []string{"ls"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
	}
	want := append(append([]string{}, sshOptions...),
		"--", "siddhant@neptuno", "pluto", "ls")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the saved target", got)
	}
}

// An unknown nickname is a CLI failure with a next step, not an ssh attempt.
func TestRunUnknownNicknameNamesTheNextStep(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "", "", 0)

	var out, errOut bytes.Buffer
	code := remotecli.Run("missing", []string{"ls"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, errOut.String())
	}
	for _, want := range []string{"unknown device", "missing", "next:", "'pluto device ls'"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr = %q, want %q", errOut.String(), want)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("ssh ran for an unknown device (stat err = %v)", err)
	}
}

// A corrupt registry names the file and never reaches ssh.
func TestRunUnreadableRegistryShowsTheNextStep(t *testing.T) {
	cfg := deviceConfig(t)
	path := corruptDevices(t, cfg)
	log := startFakeSSH(t, "", "", 0)

	var out, errOut bytes.Buffer
	code := remotecli.Run("neptuno", []string{"ls"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, errOut.String())
	}
	for _, want := range []string{path, "next:", "fix '" + path + "'"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr = %q, want %q", errOut.String(), want)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("ssh ran with a corrupt registry (stat err = %v)", err)
	}
}

// A failed ssh connection returns as the client exit code and its message is
// streamed through, so the student sees ssh's complaint.
func TestRunStreamsAFailedSSHConnection(t *testing.T) {
	deviceConfig(t)
	startFakeSSH(t, "", "ssh: connect to host x: refused\n", 255)

	var out, errOut bytes.Buffer
	code := remotecli.Run("sid@x", []string{"ls"}, &out, &errOut)
	if code != 255 {
		t.Fatalf("exit = %d, want the remote exit code 255 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "ssh: connect to host x: refused") {
		t.Fatalf("stderr = %q, want the remote ssh error", errOut.String())
	}
}

// An empty device or command is a usage error and never runs ssh.
func TestRunNeedsADeviceAndACommand(t *testing.T) {
	deviceConfig(t)
	startFakeSSH(t, "", "", 0)

	for _, tc := range []struct {
		device string
		argv   []string
	}{
		{"", []string{"ls"}},
		{"sid@host", nil},
	} {
		var out, errOut bytes.Buffer
		if code := remotecli.Run(tc.device, tc.argv, &out, &errOut); code != 2 {
			t.Fatalf("Run(%q, %q) exit = %d, want 2 (stderr %q)", tc.device, tc.argv, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), "--device") {
			t.Fatalf("usage = %q, want it to document --device", errOut.String())
		}
	}
}

// Arguments reach the remote shell quoted, so words with spaces and quotes
// survive the trip.
func TestRunPreservesQuotedArguments(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "", "", 0)

	var out, errOut bytes.Buffer
	code := remotecli.Run("sid@host", []string{"run", "mybox", "--", "sh", "-c", "echo hello world", "it's"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut.String())
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "pluto", "run", "mybox", "--", "sh", "-c", "'echo hello world'", `'it'\''s'`)
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want arguments preserved for the remote shell", got)
	}
}
