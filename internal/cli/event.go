package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
)

func runEvent(args []string, socket string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "post-commit" {
		return usageError(stderr, "event expects post-commit", "usage: pluto event post-commit")
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	root, branch, err := gitInfo(dir)
	if err != nil {
		return fail(stderr, err)
	}
	commitCmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	commit, err := commitCmd.Output()
	if err != nil {
		return fail(stderr, fmt.Errorf("resolve post-commit revision: %w", err))
	}
	if err := client.New(socket).PostCommit(api.PostCommitEvent{Worktree: root, Commit: strings.TrimSpace(string(commit)), Branch: branch}); err != nil {
		return fail(stderr, err)
	}
	return 0
}
