package devices

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ErrCopyIDUnavailable means ssh-copy-id is not installed, so the client must
// print the manual (or PowerShell) equivalent instead of writing
// authorized_keys itself.
var ErrCopyIDUnavailable = errors.New("ssh-copy-id is not installed")

// CopyID runs the standard ssh-copy-id tool. It is the only path that may ask
// for the account password, and the password is never read or stored by
// Pluto: ssh-copy-id talks to the terminal itself.
func CopyID(ctx context.Context, ssh string, p Profile, stdin io.Reader, stdout, stderr io.Writer) error {
	bin, err := exec.LookPath("ssh-copy-id")
	if err != nil {
		return ErrCopyIDUnavailable
	}
	p.applyDefaults()
	args := []string{}
	if p.Port > 0 {
		args = append(args, "-p", fmt.Sprint(p.Port))
	}
	if p.IdentityFile != "" {
		args = append(args, "-i", p.IdentityFile)
	}
	if p.ProxyJump != "" {
		args = append(args, "-J", p.ProxyJump)
	}
	args = append(args, p.Destination)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if ssh != "" {
		// ssh-copy-id shells out to `ssh`; make the discovered binary win.
		cmd.Env = append(os.Environ(), "PATH="+dirOf(ssh)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh-copy-id %s: %w", p.Destination, err)
	}
	return nil
}

func dirOf(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[:i]
	}
	return "."
}

// CopyIDCommand is the exact ssh-copy-id invocation for a profile.
func CopyIDCommand(p Profile) string {
	p.applyDefaults()
	args := []string{"ssh-copy-id"}
	if p.Port > 0 {
		args = append(args, "-p", fmt.Sprint(p.Port))
	}
	if p.IdentityFile != "" {
		args = append(args, "-i", p.IdentityFile)
	}
	if p.ProxyJump != "" {
		args = append(args, "-J", p.ProxyJump)
	}
	args = append(args, p.Destination)
	return strings.Join(args, " ")
}

// PowerShellKeyInstall is the Windows equivalent when ssh-copy-id is absent.
// It streams the public key over ssh; Pluto never writes authorized_keys
// itself.
func PowerShellKeyInstall(p Profile) string {
	p.applyDefaults()
	key := `$env:USERPROFILE\.ssh\id_ed25519.pub`
	sshArgs := []string{"ssh"}
	if p.Port > 0 {
		sshArgs = append(sshArgs, "-p", fmt.Sprint(p.Port))
	}
	if p.IdentityFile != "" {
		sshArgs = append(sshArgs, "-i", p.IdentityFile)
	}
	if p.ProxyJump != "" {
		sshArgs = append(sshArgs, "-J", p.ProxyJump)
	}
	sshArgs = append(sshArgs, p.Destination)
	remote := `mkdir -p ~/.ssh && chmod 700 ~/.ssh && cat >> ~/.ssh/authorized_keys`
	return fmt.Sprintf(`type %s | %s "%s"`, key, strings.Join(sshArgs, " "), remote)
}
