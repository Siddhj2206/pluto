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
