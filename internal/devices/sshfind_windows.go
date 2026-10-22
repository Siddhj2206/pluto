//go:build windows

package devices

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// ErrSSHMissing means no usable OpenSSH client was found on this machine.
var ErrSSHMissing = errors.New("OpenSSH client not found")

// FindSSH locates ssh.exe. PLUTO_SSH wins, then where.exe, then the Windows
// OpenSSH install location, then Git for Windows, then PATH. The binary is
// spawned directly; remote work never goes through cmd.exe or PowerShell.
func FindSSH() (string, error) {
	if path := envSSH(); path != "" {
		return path, nil
	}
	if out, err := exec.Command("where.exe", "ssh.exe").Output(); err == nil {
		if path := firstLine(string(out)); path != "" {
			if fileExists(path) {
				return path, nil
			}
		}
	}
	var candidates []string
	if root := os.Getenv("SystemRoot"); root != "" {
		candidates = append(candidates, filepath.Join(root, "System32", "OpenSSH", "ssh.exe"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		candidates = append(candidates,
			filepath.Join(pf, "Git", "usr", "bin", "ssh.exe"),
			filepath.Join(pf, "Git", "bin", "ssh.exe"),
		)
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "Programs", "Git", "usr", "bin", "ssh.exe"))
	}
	for _, path := range candidates {
		if fileExists(path) {
			return path, nil
		}
	}
	for _, name := range []string{"ssh.exe", "ssh"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", ErrSSHMissing
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\r' || r == '\n' {
			return s[:i]
		}
	}
	return s
}
