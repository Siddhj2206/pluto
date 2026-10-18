package remotecli_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/remotecli"
)

// runClient invokes RunClient with fresh buffers.
func runClient(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := remotecli.RunClient(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// A host command without --device is a usage error that names device add and
// explains that the Windows client drives a Linux host over SSH.
func TestRunClientRequiresADeviceForHostCommands(t *testing.T) {
	deviceConfig(t)
	startFakeSSH(t, "", "", 0)

	for _, args := range [][]string{{"ls"}, {"up", "--repo", "https://example.com/x.git"}, {}} {
		code, _, errOut := runClient(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want a usage error 2 (stderr %q)", args, code, errOut)
		}
		for _, want := range []string{"pluto device add", "--device", "Linux"} {
			if !strings.Contains(errOut, want) {
				t.Fatalf("%v stderr = %q, want %q", args, errOut, want)
			}
		}
	}
}

// A host command runs on the device with the client's argv replayed.
func TestRunClientRunsAHostCommandOnTheDevice(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := startFakeSSH(t, "boxes\n", "", 0)

	code, out, errOut := runClient(t, "--device", "neptuno", "status", "mybox")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "boxes") {
		t.Fatalf("stdout = %q, want the remote output streamed", out)
	}
	want := append(append([]string{}, sshOptions...),
		"--", "siddhant@neptuno", "pluto", "status", "mybox")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// Raw user@host targets work without a saved device.
func TestRunClientAcceptsARawTarget(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "", "", 0)

	code, _, errOut := runClient(t, "--device", "sid@host", "ls")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "pluto", "ls")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// Explicit global flags are forwarded to the host, and --device is dropped
// because it selected the machine rather than a host-side flag.
func TestRunClientForwardsExplicitGlobalFlags(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "", "", 0)

	code, _, errOut := runClient(t,
		"--socket", "/run/remote.sock",
		"--state-dir", "/var/lib/remote",
		"--device", "sid@host",
		"logs", "mybox", "--job", "last")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "pluto",
		"--socket", "/run/remote.sock",
		"--state-dir", "/var/lib/remote",
		"logs", "mybox", "--job", "last")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want globals forwarded and --device dropped", got)
	}
}

// The command and its arguments are replayed verbatim, `--` included.
func TestRunClientPreservesTheCommandExactly(t *testing.T) {
	deviceConfig(t)
	log := startFakeSSH(t, "", "", 9)

	code, _, errOut := runClient(t, "--device", "sid@host", "run", "mybox", "--", "sh", "-c", "echo hi")
	if code != 9 {
		t.Fatalf("exit = %d, want the remote exit code 9 (stderr %q)", code, errOut)
	}
	want := append(append([]string{}, sshOptions...),
		"--", "sid@host", "pluto", "run", "mybox", "--", "sh", "-c", "'echo hi'")
	if got := loggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the command preserved", got)
	}
}

// The device tree is local: it never needs --device and never runs ssh beyond
// the add probe.
func TestRunClientDeviceDelegatesLocally(t *testing.T) {
	deviceConfig(t)
	startFakeSSH(t, "", "", 0)

	code, out, errOut := runClient(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved") {
		t.Fatalf("device add output = %q, want the save note", out)
	}

	code, out, _ = runClient(t, "device", "ls")
	if code != 0 || !strings.Contains(out, "neptuno") {
		t.Fatalf("device ls output = %q, want the saved device", out)
	}
}

// version and --version print the resolved version.
func TestRunClientVersion(t *testing.T) {
	old := remotecli.Version
	remotecli.Version = "v0.1.5"
	t.Cleanup(func() { remotecli.Version = old })

	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, errOut := runClient(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if !strings.Contains(out, "pluto v0.1.5") {
			t.Fatalf("%v stdout = %q, want the link-time version", args, out)
		}
	}
}

// help and the top-level help flags print the client help on stdout.
func TestRunClientHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, out, errOut := runClient(t, args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0 (stderr %q)", args, code, errOut)
		}
		if errOut != "" {
			t.Fatalf("%v stderr = %q, want empty", args, errOut)
		}
		for _, want := range []string{"usage: pluto", "--device", "pluto device add"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v output = %q, want %q", args, out, want)
			}
		}
	}
}

// help names a device subcommand's own details.
func TestRunClientHelpsADeviceSubcommand(t *testing.T) {
	code, out, errOut := runClient(t, "help", "device", "add")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "usage: pluto device add") || !strings.Contains(out, "Save a nickname") {
		t.Fatalf("output = %q, want the device add help", out)
	}
}
