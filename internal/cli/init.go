package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/contract"
)

const starterContract = `# Pluto box contract. See https://github.com/Siddhj2206/pluto/blob/master/docs/pluto-yml.md
version: 1

provision:
  base: ubuntu:24.04

tasks:
  test:
    run: make test
    lock: exclusive
`

const initUsage = "usage: pluto init"

func runInit(args []string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "init", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if code := parseCommand(fs, args, stderr, initUsage); code != 0 {
		return code
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	if root, _, gitErr := gitInfo(dir); gitErr == nil {
		dir = root
	}
	if err := initRepo(dir); err != nil {
		return fail(stderr, err, "run 'pluto init' inside a Git worktree")
	}
	fmt.Fprintln(stdout, "initialized Pluto contract")
	return 0
}

// initRepo writes the starter pluto.yml only when one is absent; an existing
// contract is never overwritten.
func initRepo(dir string) error {
	path := filepath.Join(dir, contract.YAMLFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("create starter contract: %w", err)
	}
	if _, err := f.WriteString(starterContract); err != nil {
		f.Close()
		return fmt.Errorf("write starter contract: %w", err)
	}
	return f.Close()
}
