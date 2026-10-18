package remotecli

// Attach from the Windows client is a replay of the host's `pluto attach <box>`
// over ssh. These tests cover the client half: the argv the host receives, the
// process-level stdin/stdout plumbing, the exit status, and the `ssh -t` request
// that gives the box shell a terminal. The guest-side ssh invocation is built
// on the host and covered by internal/cli's attach tests.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// attachSSHOptions is the fixed ssh option prefix shared with the other
// remote commands: no prompt, a bounded connect, and first-use host key
// acceptance while a changed key still fails.
var attachSSHOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "StrictHostKeyChecking=accept-new",
}

// runAttachClient invokes RunClient, the Windows client's entry point, with
// fresh buffers.
func runAttachClient(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := RunClient(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// attachConfig points the devices registry at a fresh user config dir. Go
// reads XDG_CONFIG_HOME on unix and %AppData% on Windows, so the same helper
// works on both.
func attachConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	return dir
}

// saveAttachDevice writes a devices.toml in the shape `pluto device add`
// persists.
func saveAttachDevice(t *testing.T, cfg, nickname, target string) {
	t.Helper()
	dir := filepath.Join(cfg, "pluto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "[device]\n" + nickname + " = " + strconv.Quote(target) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "devices.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write devices.toml: %v", err)
	}
}

// startAttachFakeSSH compiles the shared fake ssh, puts it first on PATH, and
// returns the log path. The fake records argv, writes stdout, and exits with
// code; tests never reach the network. A prebuilt fake can be supplied with
// PLUTO_FAKE_SSH_BINARY so the test also runs under an emulator without a Go
// toolchain (scripts/windows-client-wine-smoke.sh).
func startAttachFakeSSH(t *testing.T, stdout string, code int) string {
	t.Helper()
	name := "ssh"
	if runtime.GOOS == "windows" {
		name = "ssh.exe"
	}
	bin := ""
	if override := os.Getenv("PLUTO_FAKE_SSH_BINARY"); override != "" {
		bin = override
	} else {
		dir := t.TempDir()
		bin = filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", bin, "./testdata/fakessh")
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			t.Fatalf("build fake ssh: %v", err)
		}
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	log := filepath.Join(t.TempDir(), "ssh.args")
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_SSH_STDOUT", stdout)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(code))
	return log
}

// attachLoggedArgs returns the argv the fake ssh was invoked with.
func attachLoggedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// Bare attach replays `attach <box>` on the host unchanged and returns the
// host's output, so the host opens the box's default shell.
func TestRunClientAttachesToTheBoxShell(t *testing.T) {
	cfg := attachConfig(t)
	saveAttachDevice(t, cfg, "neptuno", "siddhant@neptuno")
	log := startAttachFakeSSH(t, "box shell ready\n", 0)

	code, out, errOut := runAttachClient(t, "--device", "neptuno", "attach", "box-1")
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "box shell ready") {
		t.Fatalf("stdout = %q, want the host's output streamed", out)
	}
	want := append(append([]string{}, attachSSHOptions...),
		"--", "siddhant@neptuno", "pluto", "attach", "box-1")
	if got := attachLoggedArgs(t, log); !reflect.DeepEqual(got, want) {
		t.Fatalf("ssh argv = %q, want %q", got, want)
	}
}

// --session is replayed verbatim in both spellings, so the host enters the
// declared session instead of starting a shell.
func TestRunClientAttachesIntoADeclaredSession(t *testing.T) {
	cfg := attachConfig(t)
	saveAttachDevice(t, cfg, "neptuno", "siddhant@neptuno")

	cases := []struct {
		name string
		args []string
	}{
		{"space form", []string{"attach", "box-1", "--session", "agent"}},
		{"equals form", []string{"attach", "box-1", "--session=agent"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := startAttachFakeSSH(t, "session restored\n", 0)
			code, out, errOut := runAttachClient(t, append([]string{"--device", "neptuno"}, tc.args...)...)
			if code != 0 {
				t.Fatalf("exit = %d, stderr: %s", code, errOut)
			}
			if !strings.Contains(out, "session restored") {
				t.Fatalf("stdout = %q, want the host's output streamed", out)
			}
			want := append(append([]string{}, attachSSHOptions...),
				"--", "siddhant@neptuno", "pluto")
			want = append(want, tc.args...)
			if got := attachLoggedArgs(t, log); !reflect.DeepEqual(got, want) {
				t.Fatalf("ssh argv = %q, want %q", got, want)
			}
		})
	}
}

// Input and output both stream: what the client writes to ssh's stdin comes
// back on its stdout while the same ssh invocation runs attach. The fake ssh
// echoes stdin, so this fails if stdin is dropped or output is withheld until
// exit.
func TestExecRemoteStreamsAttachStdinBothWays(t *testing.T) {
	startAttachFakeSSH(t, "", 0)
	t.Setenv("FAKE_SSH_ECHO_STDIN", "1")

	input := "echo hello from the box\nexit\n"
	var out, errOut bytes.Buffer
	code, err := execRemote("siddhant@neptuno", []string{"attach", "box-1"}, false,
		strings.NewReader(input), &out, &errOut)
	if err != nil {
		t.Fatalf("execRemote: %v (stderr %q)", err, errOut.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out.String() != input {
		t.Fatalf("stdout = %q, want the stdin echoed back (%q)", out.String(), input)
	}
}

// Exiting the box shell returns its status to the client instead of being
// swallowed, so a failed shell is visible on Windows.
func TestRunClientReturnsTheAttachShellExitStatus(t *testing.T) {
	cfg := attachConfig(t)
	saveAttachDevice(t, cfg, "neptuno", "siddhant@neptuno")
	startAttachFakeSSH(t, "", 3)

	code, out, errOut := runAttachClient(t, "--device", "neptuno", "attach", "box-1")
	if code != 3 {
		t.Fatalf("exit = %d, want the shell's exit status 3 (stderr %q)", code, errOut)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want no output from a silent shell", out)
	}
	if strings.Contains(errOut, "pluto: ssh:") {
		t.Fatalf("stderr = %q, want the exit status, not an ssh failure", errOut)
	}
}

// The tty branch: interactive stdin asks ssh for a remote pty so the host runs
// attach on a terminal, and a piped stdin does not. This is the private seam
// because Run chooses the branch from console.IsTerminal(os.Stdin), which a
// test process cannot turn into a real console.
func TestRemoteSSHArgsAllocateATtyForAttachOnATerminal(t *testing.T) {
	interactive := remoteSSHArgs("host", []string{"attach", "box-1"}, true)
	wantInteractive := append([]string{"-t"}, attachSSHOptions...)
	wantInteractive = append(wantInteractive, "--", "host", "pluto", "attach", "box-1")
	if !reflect.DeepEqual(interactive, wantInteractive) {
		t.Fatalf("interactive attach argv = %q, want a leading -t", interactive)
	}

	piped := remoteSSHArgs("host", []string{"attach", "box-1"}, false)
	wantPiped := append(append([]string{}, attachSSHOptions...),
		"--", "host", "pluto", "attach", "box-1")
	if !reflect.DeepEqual(piped, wantPiped) {
		t.Fatalf("piped attach argv = %q, want no -t", piped)
	}
}
