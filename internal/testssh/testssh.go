// Package testssh builds and arranges the fake ssh used by tests. It is a
// normal package imported only from _test.go files, so it never links into a
// shipped binary.
//
// The fake is a Go program (testdata/fakessh) rather than a shell script so
// the same tests run on Linux and Windows. Building it honours
// PLUTO_FAKE_SSH_BINARY, which supplies a prebuilt binary when no Go toolchain
// is available (the Wine rehearsal).
package testssh

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// BinaryName is the name the client's plain `ssh` lookup uses on this
// platform.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "ssh.exe"
	}
	return "ssh"
}

// Build compiles the fake ssh under the name the client looks up and returns
// its path. A prebuilt fake can be supplied with PLUTO_FAKE_SSH_BINARY, which
// lets the tests run under an emulator where no Go toolchain exists.
func Build(t *testing.T) string {
	t.Helper()
	if override := os.Getenv("PLUTO_FAKE_SSH_BINARY"); override != "" {
		return override
	}
	return buildNamed(t, BinaryName())
}

// buildNamed compiles the fake under name in a fresh directory.
func buildNamed(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate testssh source")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", bin, "./testdata/fakessh")
	build.Dir = filepath.Dir(file)
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build fake %s: %v", name, err)
	}
	return bin
}

// Rule is one fake-ssh script entry: when Match appears in the joined argv,
// the fake writes Stdout/Stderr and exits with Exit.
type Rule struct {
	Match  string `json:"match"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

// StartScript starts the fake ssh with per-command responses. The first rule
// whose Match appears in the joined argv wins; unmatched calls fall back to
// the plain Start behaviour.
func StartScript(t *testing.T, rules ...Rule) string {
	t.Helper()
	log := Start(t, "", "", 0)
	data, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("encode fake ssh script: %v", err)
	}
	t.Setenv("FAKE_SSH_SCRIPT", string(data))
	return log
}

// CallLog saves every invocation in order and returns the path.
func CallLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh.calls")
	t.Setenv("FAKE_SSH_CALL_LOG", path)
	return path
}

// LoggedCalls returns every invocation recorded by CallLog, oldest first.
func LoggedCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	var calls [][]string
	for _, block := range strings.Split(string(data), "---\n") {
		block = strings.TrimSuffix(block, "\n")
		if block == "" {
			continue
		}
		calls = append(calls, strings.Split(block, "\n"))
	}
	return calls
}

// StartCopyID puts a fake ssh-copy-id first on PATH. It logs its argv like the
// ssh fake and exits 0.
func StartCopyID(t *testing.T) string {
	t.Helper()
	name := "ssh-copy-id"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := buildNamed(t, name)
	PutOnPath(t, bin)
	log := filepath.Join(t.TempDir(), "ssh-copy-id.args")
	t.Setenv("FAKE_SSH_LOG", log)
	return log
}

// PutOnPath puts the fake's directory first on PATH, so the client's plain
// `ssh` (or `ssh.exe`) lookup finds it.
func PutOnPath(t *testing.T, bin string) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Start builds the fake, puts it first on PATH, and points its environment at
// a fresh log with the requested streams and exit code. It returns the log
// path for LoggedArgs. Tests never reach the network.
func Start(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	bin := Build(t)
	PutOnPath(t, bin)

	log := filepath.Join(t.TempDir(), "ssh.args")
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_SSH_STDOUT", stdout)
	t.Setenv("FAKE_SSH_STDERR", stderr)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(code))
	t.Setenv("FAKE_SSH_ECHO_STDIN", "")
	return log
}

// EchoStdin makes the fake copy its stdin to stdout, so a test can prove a
// stream reaches the remote process and comes back.
func EchoStdin(t *testing.T) {
	t.Helper()
	t.Setenv("FAKE_SSH_ECHO_STDIN", "1")
}

// StartKeyBootstrap makes ssh fail with "Permission denied" until the client
// runs the key-install command (the one that mentions authorized_keys), then
// succeed on every later call. It models a host that only accepts a password
// until the client installs its key.
func StartKeyBootstrap(t *testing.T, stdout string) string {
	t.Helper()
	log := Start(t, stdout, "siddhant@neptuno: Permission denied (publickey,password).\n", 255)
	t.Setenv("FAKE_SSH_MARKER", filepath.Join(t.TempDir(), "key-installed"))
	return log
}

// StdinLog saves what the fake ssh received on stdin and returns the path.
func StdinLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh.stdin")
	t.Setenv("FAKE_SSH_STDIN_LOG", path)
	return path
}

// LoggedArgs returns the argv the fake ssh was invoked with.
func LoggedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}
