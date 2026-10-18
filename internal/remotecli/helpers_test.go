package remotecli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// startFakeSSH compiles testdata/fakessh, puts it first on PATH, and points it
// at a log file. Tests never reach the network: the fake records argv, writes
// the requested streams, and exits with the requested code. The compiled name
// is ssh (ssh.exe on Windows) so the client's plain `ssh` lookup finds it.
func startFakeSSH(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	dir := t.TempDir()
	name := "ssh"
	if runtime.GOOS == "windows" {
		name = "ssh.exe"
	}
	bin := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", bin, "./testdata/fakessh")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	log := filepath.Join(t.TempDir(), "ssh.args")
	t.Setenv("FAKE_SSH_LOG", log)
	t.Setenv("FAKE_SSH_STDOUT", stdout)
	t.Setenv("FAKE_SSH_STDERR", stderr)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(code))
	return log
}

// loggedArgs returns the argv the fake ssh was invoked with.
func loggedArgs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("ssh was not invoked: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// deviceConfig points the registry at a fresh user config dir. Go reads
// XDG_CONFIG_HOME on unix and AppData on Windows, so the same helper works on
// both, and the Windows CI job exercises the AppData path.
func deviceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	return dir
}

// saveDevice writes a devices.toml in the shape `pluto device add` persists.
func saveDevice(t *testing.T, cfg, nickname, target string) {
	t.Helper()
	writeDevices(t, cfg, fmt.Sprintf("[device]\n%s = %q\n", nickname, target))
}

// corruptDevices writes an unparseable devices.toml and returns its path.
func corruptDevices(t *testing.T, cfg string) string {
	t.Helper()
	writeDevices(t, cfg, "not = = toml\n")
	return filepath.Join(cfg, "pluto", "devices.toml")
}

func writeDevices(t *testing.T, cfg, body string) {
	t.Helper()
	dir := filepath.Join(cfg, "pluto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devices.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write devices.toml: %v", err)
	}
}
