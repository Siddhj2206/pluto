package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "pluto")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "pluto")
}

func socketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "pluto", "host.sock")
	}
	// Some sessions don't set it, but the service does. Find the same socket.
	if d := fmt.Sprintf("/run/user/%d", os.Getuid()); dirExists(d) {
		return filepath.Join(d, "pluto", "host.sock")
	}
	return filepath.Join(stateDir(), "host.sock")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func unitPath() string {
	d := os.Getenv("XDG_CONFIG_HOME")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, ".config")
	}
	return filepath.Join(d, "systemd", "user", "pluto-host.service")
}
