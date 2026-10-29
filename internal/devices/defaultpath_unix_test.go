//go:build !windows

package devices_test

import (
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// On unix, os.UserConfigDir honors XDG_CONFIG_HOME.
func TestDefaultPathFollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := devices.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "pluto", "devices.toml"); path != want {
		t.Fatalf("DefaultPath = %q, want %q", path, want)
	}
}
