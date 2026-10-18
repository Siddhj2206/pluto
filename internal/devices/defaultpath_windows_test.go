//go:build windows

package devices_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/devices"
)

// On Windows, os.UserConfigDir reads AppData, so the registry lands under
// %AppData%\pluto\devices.toml. The name is the one Go reads: "AppData".
func TestDefaultPathUsesAppData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	path, err := devices.DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, "pluto", "devices.toml"); path != want {
		t.Fatalf("DefaultPath = %q, want %q", path, want)
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Fatal("DefaultPath must not create the directory")
	}
}
