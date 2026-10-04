package runner

import (
	"fmt"
	"os/exec"
	"strings"
)

// ExecSystemctl drives the user's systemd through the systemctl binary.
type ExecSystemctl struct{}

func (ExecSystemctl) run(args ...string) (string, error) {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("systemctl --user %s: %w (%s)", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// IsActive reports the unit's active state. A never-started instance reads
// as inactive; that is not an error.
func (ExecSystemctl) IsActive(unit string) (string, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		if err != nil {
			return "", fmt.Errorf("systemctl --user is-active %s: %w", unit, err)
		}
		return "inactive", nil
	}
	return strings.SplitN(state, "\n", 2)[0], nil
}

// Start starts the unit.
func (e ExecSystemctl) Start(unit string) error {
	_, err := e.run("start", unit)
	return err
}

// Stop stops the unit, terminating its whole cgroup.
func (e ExecSystemctl) Stop(unit string) error {
	_, err := e.run("stop", unit)
	return err
}

// ResetFailed clears a failed unit's status.
func (e ExecSystemctl) ResetFailed(unit string) error {
	_, err := e.run("reset-failed", unit)
	return err
}

// DaemonReload reloads unit files.
func (e ExecSystemctl) DaemonReload() error {
	_, err := e.run("daemon-reload")
	return err
}
