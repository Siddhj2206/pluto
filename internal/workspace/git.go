package workspace

import (
	"fmt"
	"os/exec"
	"strings"
)

// git runs git and returns combined output. dir empty means the caller does
// not need a repository (clone).
func git(dir string, args ...string) (string, error) {
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	return string(out), err
}

func gitOutput(dir string, args ...string) (string, error) {
	out, err := git(dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}
