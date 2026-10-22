//go:build !windows

package devices

import (
	"errors"
	"os/exec"
)

// ErrSSHMissing means no usable OpenSSH client was found on this machine.
var ErrSSHMissing = errors.New("OpenSSH client not found")

// FindSSH locates the ssh binary. PLUTO_SSH wins, then the normal PATH.
func FindSSH() (string, error) {
	if path := envSSH(); path != "" {
		return path, nil
	}
	path, err := exec.LookPath("ssh")
	if err != nil {
		return "", ErrSSHMissing
	}
	return path, nil
}
