package imagebuilder

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// Command is one external command. Env entries are appended to the inherited
// environment; Dir and the output writers are optional.
type Command struct {
	Name   string
	Args   []string
	Env    []string
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

// Shell runs external commands. The builder shells out to podman, go, tar, and
// mkfs.ext4 only through this seam, so tests can replace it with a fake and
// exercise assembly without podman, network, or KVM. It is the injectable
// boundary the ticket calls for.
type Shell interface {
	Run(ctx context.Context, cmd Command) error
	Output(ctx context.Context, cmd Command) ([]byte, error)
}

// ExecShell runs commands as real subprocesses.
type ExecShell struct{}

func (ExecShell) prepare(ctx context.Context, cmd Command, capture bool) *exec.Cmd {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	if !capture {
		c.Stdout = cmd.Stdout
		if c.Stdout == nil {
			c.Stdout = os.Stdout
		}
	}
	c.Stderr = cmd.Stderr
	if c.Stderr == nil {
		c.Stderr = os.Stderr
	}
	return c
}

// Run runs cmd, streaming output to the process by default.
func (s ExecShell) Run(ctx context.Context, cmd Command) error {
	return s.prepare(ctx, cmd, false).Run()
}

// Output runs cmd and returns its stdout.
func (s ExecShell) Output(ctx context.Context, cmd Command) ([]byte, error) {
	return s.prepare(ctx, cmd, true).Output()
}
