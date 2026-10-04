// Package systemd installs the daemon as a systemd user service with linger.
package systemd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

const unitName = "pluto.service"

// Unit renders the user unit for a pluto binary.
func Unit(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=pluto host daemon
After=default.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, execPath)
}

// UnitPath is the user unit's location.
func UnitPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config dir: %w", err)
	}
	return filepath.Join(dir, "systemd", "user", unitName), nil
}

// Install writes the unit, reloads systemd, enables it now, and enables
// linger so the daemon starts at login.
func Install(execPath string, out io.Writer) error {
	path, err := UnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(Unit(execPath)), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	if err := run(out, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run(out, "systemctl", "--user", "enable", "--now", unitName); err != nil {
		return err
	}
	if username := currentUsername(); username != "" {
		if err := run(out, "loginctl", "enable-linger", username); err != nil {
			fmt.Fprintf(out, "warning: could not enable linger: %v\n", err)
		}
	}
	fmt.Fprintf(out, "installed %s\n", path)
	return nil
}

// Uninstall disables the unit and removes it. Linger is left enabled.
func Uninstall(out io.Writer) error {
	_ = run(out, "systemctl", "--user", "disable", "--now", unitName)
	path, err := UnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	if err := run(out, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s (linger left enabled)\n", path)
	return nil
}

func currentUsername() string {
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func run(out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
