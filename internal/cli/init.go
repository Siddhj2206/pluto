package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const starterContract = "# Pluto box contract. See https://github.com/Siddhj2206/pluto/blob/master/docs/contract.md\n\n[jobs.test]\ndescription = \"run the project test suite\"\ncommand = \"make test\"\n"
const managedHookMarker = "# pluto-managed-post-commit"

const initUsage = "usage: pluto init [--with-hooks | --remove-hooks]"

func runInit(args []string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "init", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	withHooks := fs.Bool("with-hooks", false, "install the opt-in post-commit hook")
	removeHooks := fs.Bool("remove-hooks", false, "remove Pluto's post-commit hook")
	if code := parseCommand(fs, args, stderr, initUsage); code != 0 {
		return code
	}
	if *withHooks && *removeHooks {
		return usageError(stderr, "--with-hooks and --remove-hooks cannot be used together", initUsage)
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	if root, _, gitErr := gitInfo(dir); gitErr == nil {
		dir = root
	}
	if err := initRepo(dir, *withHooks, *removeHooks); err != nil {
		hint := "run 'pluto init' inside a Git worktree"
		if *removeHooks {
			hint = "run 'pluto init --remove-hooks' inside a Git worktree"
		} else if *withHooks {
			hint = "run 'pluto init --with-hooks' inside a Git worktree"
		}
		return fail(stderr, err, hint)
	}
	if *removeHooks {
		fmt.Fprintln(stdout, "removed Pluto post-commit hook")
	} else if *withHooks {
		fmt.Fprintln(stdout, "initialized Pluto contract and installed post-commit hook")
	} else {
		fmt.Fprintln(stdout, "initialized Pluto contract")
	}
	return 0
}

func initRepo(dir string, withHooks, removeHooks bool) error {
	if !removeHooks {
		path := filepath.Join(dir, ".pluto.toml")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create starter contract: %w", err)
		}
		if err == nil {
			if _, err = f.WriteString(starterContract); err != nil {
				f.Close()
				return fmt.Errorf("write starter contract: %w", err)
			}
			if err = f.Close(); err != nil {
				return fmt.Errorf("close starter contract: %w", err)
			}
		}
	}
	if !withHooks && !removeHooks {
		return nil
	}
	root, _, err := gitInfo(dir)
	if err != nil {
		return errors.New("Git hooks require a Git worktree")
	}
	commonDir, err := gitOutputErr(root, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return err
	}
	managedDir := filepath.Join(commonDir, "pluto-hooks")
	if removeHooks {
		return removePostCommitHook(root, managedDir)
	}
	return installPostCommitHook(root, commonDir, managedDir)
}

func installPostCommitHook(root, commonDir, managedDir string) error {
	previous, previousSet, err := gitConfigEffective(root, "--path", "--get", "core.hooksPath")
	if err != nil {
		return err
	}
	_, hadLocalPath, err := gitConfig(root, "--get", "core.hooksPath")
	if err != nil {
		return err
	}
	previousConfig, hasSaved, err := gitConfig(root, "--get", "pluto.hooksPreviousPath")
	if err != nil {
		return err
	}
	if hasSaved {
		previous, previousSet = previousConfig, previousConfig != ""
	} else {
		if err := gitConfigSet(root, "pluto.hooksPreviousPath", previous); err != nil {
			return err
		}
		if hadLocalPath {
			if err := gitConfigSet(root, "pluto.hooksHadLocalPath", "true"); err != nil {
				return err
			}
		} else if err := gitConfigSet(root, "pluto.hooksHadLocalPath", "false"); err != nil {
			return err
		}
	}
	previousDir := previous
	if !previousSet {
		previousDir = filepath.Join(commonDir, "hooks")
	} else if !filepath.IsAbs(previousDir) {
		previousDir = filepath.Join(root, previousDir)
	}
	previousDir, err = filepath.Abs(previousDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(managedDir, 0700); err != nil {
		return err
	}
	if current, err := os.ReadFile(filepath.Join(managedDir, "post-commit")); err == nil && !strings.Contains(string(current), managedHookMarker) {
		return errors.New("post-commit already exists in Pluto's managed hook directory; preserving it")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	oldHook := filepath.Join(previousDir, "post-commit")
	content := strings.Join([]string{
		"#!/bin/sh", managedHookMarker,
		"if [ -x " + shellQuote(oldHook) + " ]; then",
		"  " + shellQuote(oldHook) + " \"$@\"", "  previous_status=$?", "else", "  previous_status=0", "fi",
		shellQuote(exe) + " event post-commit >/dev/null 2>&1 || true",
		"exit \"$previous_status\"", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(managedDir, "post-commit"), []byte(content), 0755); err != nil {
		return err
	}
	return gitConfigSet(root, "core.hooksPath", managedDir)
}

func removePostCommitHook(root, managedDir string) error {
	path := filepath.Join(managedDir, "post-commit")
	data, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(data), managedHookMarker) {
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if err == nil {
		return errors.New("post-commit in Pluto's managed directory was changed; preserving it and its active hooksPath")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if entries, err := os.ReadDir(managedDir); err == nil && len(entries) == 0 {
		_ = os.Remove(managedDir)
	}
	previous, saved, err := gitConfig(root, "--get", "pluto.hooksPreviousPath")
	if err != nil {
		return err
	}
	if saved {
		hadLocalPath, _, err := gitConfig(root, "--get", "pluto.hooksHadLocalPath")
		if err != nil {
			return err
		}
		if hadLocalPath == "true" {
			if previous != "" {
				if err := gitConfigSet(root, "core.hooksPath", previous); err != nil {
					return err
				}
			} else if err := gitConfigUnset(root, "core.hooksPath"); err != nil {
				return err
			}
		} else {
			if err := gitConfigUnset(root, "core.hooksPath"); err != nil {
				return err
			}
		}
		_ = gitConfigUnset(root, "pluto.hooksPreviousPath")
		_ = gitConfigUnset(root, "pluto.hooksHadLocalPath")
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func gitConfig(root string, args ...string) (string, bool, error) {
	out, err := exec.Command("git", append([]string{"-C", root, "config", "--local"}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read Git config: %w", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func gitConfigEffective(root string, args ...string) (string, bool, error) {
	out, err := exec.Command("git", append([]string{"-C", root, "config"}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read effective Git config: %w", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func gitConfigSet(root, key, value string) error {
	cmd := exec.Command("git", "-C", root, "config", "--local", key, value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write Git config %s: %w (%s)", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitConfigUnset(root, key string) error {
	cmd := exec.Command("git", "-C", root, "config", "--local", "--unset", key)
	if out, err := cmd.CombinedOutput(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("remove Git config %s: %w (%s)", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOutputErr(root string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
