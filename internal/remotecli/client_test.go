package remotecli_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/remotecli"
	"github.com/Siddhj2206/pluto/internal/testssh"
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
	testssh.Start(t, "", "", 0)

	for _, args := range [][]string{{"ls"}, {"up", "--repo", "https://example.com/x.git"}, {}} {
		code, _, errOut := runClient(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want a usage error 2 (stderr %q)", args, code, errOut)
		}
		for _, want := range []string{"usage: pluto", "pluto device add", "--device", "Linux"} {
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
	log := testssh.Start(t, "boxes\n", "", 0)

	code, out, errOut := runClient(t, "--device", "neptuno", "status", "mybox")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "boxes") {
		t.Fatalf("stdout = %q, want the remote output streamed", out)
	}
	want := wantRemote("siddhant@neptuno", "pluto status mybox")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// Provisioning is a host command: the repository URL reaches the host's pluto
// unchanged, query and .git suffix included, and the host's result returns.
func TestRunClientProvisionsFromARepositoryURL(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := testssh.Start(t, "created box 1234 for app/trunk\n", "", 0)

	code, out, errOut := runClient(t, "--device", "neptuno",
		"up", "--repo", "https://github.com/you/app.git?ref=main&depth=1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "created box 1234") {
		t.Fatalf("stdout = %q, want the host's result", out)
	}
	want := wantRemote("siddhant@neptuno", "pluto up --repo 'https://github.com/you/app.git?ref=main&depth=1'")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the URL preserved for the host", got)
	}
}

// The box lifecycle commands are replayed verbatim, so an id or worktree
// resolves on the host that owns the box.
func TestRunClientReplaysTheBoxLifecycle(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := testssh.Start(t, "box mybox paused\n", "", 0)

	for _, args := range [][]string{
		{"ls"},
		{"status", "mybox"},
		{"pause", "mybox"},
		{"destroy", "mybox", "--yes"},
	} {
		code, out, errOut := runClient(t, append([]string{"--device", "neptuno"}, args...)...)
		if code != 0 {
			t.Fatalf("%v exit = %d, stderr: %s", args, code, errOut)
		}
		if !strings.Contains(out, "box mybox paused") {
			t.Fatalf("%v stdout = %q, want the remote output", args, out)
		}
		want := wantRemoteArgv("siddhant@neptuno", "pluto", args)
		if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
			t.Fatalf("%v ssh argv = %q, want %q", args, got, want)
		}
	}
}

// A host that rejects provisioning is reported: its exit status and stderr
// return instead of being swallowed.
func TestRunClientReportsAFailedProvisioning(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")
	testssh.Start(t, "", "fatal: repository 'https://github.com/you/app.git' not found\n", 1)

	code, _, errOut := runClient(t, "--device", "neptuno",
		"up", "--repo", "https://github.com/you/app.git")
	if code != 1 {
		t.Fatalf("exit = %d, want the host's exit status 1", code)
	}
	if !strings.Contains(errOut, "repository 'https://github.com/you/app.git' not found") {
		t.Fatalf("stderr = %q, want the host's failure", errOut)
	}
}

// Raw user@host targets work without a saved device.
func TestRunClientAcceptsARawTarget(t *testing.T) {
	deviceConfig(t)
	log := testssh.Start(t, "", "", 0)

	code, _, errOut := runClient(t, "--device", "sid@host", "ls")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := wantRemote("sid@host", "pluto ls")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// Explicit global flags are forwarded to the host, and --device is dropped
// because it selected the machine rather than a host-side flag.
func TestRunClientForwardsExplicitGlobalFlags(t *testing.T) {
	deviceConfig(t)
	log := testssh.Start(t, "", "", 0)

	code, _, errOut := runClient(t,
		"--socket", "/run/remote.sock",
		"--state-dir", "/var/lib/remote",
		"--device", "sid@host",
		"logs", "mybox", "--job", "last")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	want := wantRemote("sid@host", "pluto --socket /run/remote.sock --state-dir /var/lib/remote logs mybox --job last")
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want globals forwarded and --device dropped", got)
	}
}

// The command and its arguments are replayed verbatim, `--` included.
func TestRunClientPreservesTheCommandExactly(t *testing.T) {
	deviceConfig(t)
	log := testssh.Start(t, "", "", 9)

	code, _, errOut := runClient(t, "--device", "sid@host", "run", "mybox", "--", "sh", "-c", "echo hi")
	if code != 9 {
		t.Fatalf("exit = %d, want the remote exit code 9 (stderr %q)", code, errOut)
	}
	want := wantRemoteArgv("sid@host", "pluto", []string{"run", "mybox", "--", "sh", "-c", "echo hi"})
	if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want the command preserved", got)
	}
}

// The device tree is local: it never needs --device and never runs ssh beyond
// the add probe.
func TestRunClientDeviceDelegatesLocally(t *testing.T) {
	deviceConfig(t)
	testssh.StartScript(t, healthyRules("home.example.net", 22)...)

	code, out, errOut := runClient(t, "device", "add", "neptuno", "siddhant@neptuno")
	if code != 0 {
		t.Fatalf("device add exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "saved") || !strings.Contains(out, "neptuno  connected") || !strings.Contains(out, "/home/sid/.local/bin/pluto") {
		t.Fatalf("device add output = %q, want the save and connection card", out)
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

// --device takes precedence over the local commands, matching the Linux CLI:
// version, help, and the device tree are replayed on the host when a device is
// selected.
func TestRunClientDevicePrecedesLocalCommands(t *testing.T) {
	cfg := deviceConfig(t)
	saveDevice(t, cfg, "neptuno", "siddhant@neptuno")

	for _, args := range [][]string{{"version"}, {"help"}, {"device", "ls"}} {
		log := testssh.Start(t, "host says ok\n", "", 0)
		code, out, errOut := runClient(t, append([]string{"--device", "neptuno"}, args...)...)
		if code != 0 {
			t.Fatalf("%v exit = %d, stderr: %s", args, code, errOut)
		}
		if !strings.Contains(out, "host says ok") {
			t.Fatalf("%v stdout = %q, want the host's output", args, out)
		}
		want := wantRemoteArgv("siddhant@neptuno", "pluto", args)
		if got := testssh.LoggedArgs(t, log); !reflect.DeepEqual(got, want) {
			t.Fatalf("%v ssh argv = %q, want %q", args, got, want)
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

// A command the client does not document is probably a host command: keep
// help a success, but show how to reach its help on the host.
func TestRunClientHelpNamesTheHostForAHostCommand(t *testing.T) {
	code, out, errOut := runClient(t, "help", "run")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"pluto client", "next:", "pluto --device <nickname> help run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
}
