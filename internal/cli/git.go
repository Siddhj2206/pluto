package cli

import (
	"fmt"
	"os/exec"
	"strings"
)

// gitInfo resolves a directory to its worktree root and current branch.
func gitInfo(dir string) (root, branch string, err error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", fmt.Errorf("%s is not a git worktree", dir)
	}
	root = strings.TrimSpace(string(out))
	out, err = exec.Command("git", "-C", root, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		if _, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Output(); err != nil {
			return "", "", fmt.Errorf("resolve branch in %s: %w", root, err)
		}
		return root, "(detached)", nil
	}
	branch = strings.TrimSpace(string(out))
	return root, branch, nil
}
