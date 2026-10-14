package terminal_test

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/terminal"
)

// TestOpenPTYRunsACommandInTheTerminal proves the production opener allocates a
// real host terminal pair that a process's stdio can be wired to.
func TestOpenPTYRunsACommandInTheTerminal(t *testing.T) {
	pty, err := terminal.Open()
	if err != nil {
		t.Skipf("no host pty available: %v", err)
	}
	hub := terminal.NewHub(func() (terminal.PTY, error) { return pty, nil })
	start := func(ctx context.Context, slave io.ReadWriteCloser) (int, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", "printf hello; exit 3")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode(), nil
			}
			return 1, err
		}
		return 0, nil
	}

	_, sub, err := hub.Attach("box", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer sub.Close()

	got := drain(t, sub)
	if !strings.Contains(got, "hello") {
		t.Fatalf("terminal output = %q, want the command's output", got)
	}
	if code := sub.ExitCode(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}
