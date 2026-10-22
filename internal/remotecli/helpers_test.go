package remotecli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// deviceConfig points the registry at a fresh user config dir. Go reads
// XDG_CONFIG_HOME on unix and AppData on Windows, so the same helper works on
// both, and the Windows CI job exercises the AppData path. It also moves the
// test outside any project checkout, so the client sends no default-target
// context unless a test opts in by changing directory into a fixture.
func deviceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("AppData", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	t.Chdir(t.TempDir())
	t.Setenv("PLUTO_DEVICE_CACHE_DIR", filepath.Join(t.TempDir(), "device-cache"))
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
