package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// ADR 0009: every user-facing failure names the next step.

// A target that resolves to no box points at creation.
func TestBoxTargetFailuresSuggestCreatingTheBox(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)

	for _, cmd := range []string{"attach", "pause", "status", "jobs", "logs"} {
		code, _, errOut := runCLI(t, "--socket", socket, cmd, repo)
		if code != 1 {
			t.Errorf("%s on a worktree with no box: exit = %d, want 1 (stderr %q)", cmd, code, errOut)
			continue
		}
		if !strings.Contains(errOut, "next: create it with 'pluto up'") {
			t.Errorf("%s stderr = %q, want the create hint", cmd, errOut)
		}
	}
}

// An unreachable daemon is the one failure every daemon command shares.
func TestUnreachableDaemonNamesTheNextStep(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sock")
	repo := gitRepo(t)
	artifact := t.TempDir()

	for _, args := range [][]string{
		{"ls"},
		{"status", "x"},
		{"pause", "x"},
		{"jobs", "x"},
		{"logs", "x"},
		{"attach", "x"},
		{"destroy", "0123456789abcdef", "--yes"},
		{"up", "--worktree", repo},
		{"run", repo, "--", "true"},
		{"image", "import", artifact},
	} {
		code, _, errOut := runCLI(t, append([]string{"--socket", missing}, args...)...)
		if code != 1 {
			t.Errorf("%v exit = %d, want 1 (stderr %q)", args, code, errOut)
			continue
		}
		if !strings.Contains(errOut, "next: start the daemon with 'pluto daemon'") {
			t.Errorf("%v stderr = %q, want the daemon hint", args, errOut)
		}
	}
}

func TestUpOutsideAGitWorktreeNamesTheNextStep(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", t.TempDir())
	if code != 1 {
		t.Fatalf("up outside a worktree exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"not a git worktree", "next:", "'pluto up --worktree <path>'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

func TestDestroyWithoutConfirmationNamesTheNextStep(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}

	code, _, errOut := runCLI(t, "--socket", socket, "destroy", repo)
	if code != 1 {
		t.Fatalf("destroy without --yes exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"confirmation", "next:", "pluto destroy", "--yes"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

func TestDaemonFailuresNameTheNextStep(t *testing.T) {
	// A state-dir that is a file cannot be opened.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	code, _, errOut := runCLI(t, "--state-dir", file, "daemon")
	if code != 1 {
		t.Fatalf("daemon with a bad state dir exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "next:") || !strings.Contains(errOut, "'pluto --state-dir <path> daemon'") {
		t.Fatalf("stderr = %q, want the state-dir hint", errOut)
	}
}

// fakeCommand puts a failing executable first on PATH, so systemd and ssh
// calls never reach the machine.
func fakeCommand(t *testing.T, name string, code int) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", code)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir)
}

func TestInstallAndUninstallFailuresNameTheNextStep(t *testing.T) {
	deviceConfig(t) // XDG_CONFIG_HOME for the unit path
	fakeCommand(t, "systemctl", 1)

	for _, cmd := range []string{"install", "uninstall"} {
		code, _, errOut := runCLI(t, cmd)
		if code != 1 {
			t.Errorf("%s exit = %d, want 1 (stderr %q)", cmd, code, errOut)
			continue
		}
		if !strings.Contains(errOut, "next:") {
			t.Errorf("%s stderr = %q, want a next step", cmd, errOut)
		}
	}
}

// A daemon or agent failure passes through as the first line; with no curated
// hint the CLI offers the generic daemon-log fallback.
func TestGenericDaemonErrorFallsBackToTheDaemonLogHint(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		return nil, fmt.Errorf("agent exploded")
	}})
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "true")
	if code != 1 {
		t.Fatalf("run exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"pluto: agent exploded", "next: check the daemon log with 'journalctl --user -u pluto -n 50'"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A job that fails without an exit code still names where to read the output.
func TestFailedJobWithoutExitCodeNamesTheLogsCommand(t *testing.T) {
	socket, _ := startDaemonWith(t, fakeRunner{run: func(box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Job, error) {
		emit([]byte("boom\n"))
		job := state.StartJobCommand(state.NewID(), spec.Command.String())
		now := time.Now().UTC()
		job.State = state.JobFailed
		job.Error = "unit timed out"
		job.FinishedAt = &now
		return &job, nil
	}})
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "--", "sleep", "forever")
	if code != 1 {
		t.Fatalf("run exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"pluto: job", "unit timed out", "next: read the output with 'pluto logs"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A contract parse error carries file:line and the promised edit hint; the
// fix is an edit at that spot (ADR 0009, docs/contract.md).
func TestContractParseErrorCarriesFileAndLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".pluto.toml"), []byte("[jobs.dev]\ncommand = [\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	t.Chdir(dir)

	code, _, errOut := runCLI(t, "run")
	if code != 1 {
		t.Fatalf("run with a broken contract exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, ".pluto.toml:") {
		t.Fatalf("stderr = %q, want file:line", errOut)
	}
	if !strings.Contains(errOut, "next: fix the contract and run 'pluto run' again") {
		t.Fatalf("stderr = %q, want the promised edit hint", errOut)
	}
}

// A semantic failure from a local load carries the blamed line too.
func TestContractSemanticErrorCarriesFileAndLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".pluto.toml"), []byte("[jobs.dev]\ncommand = \"make\"\ntimeout = \"soon\"\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	t.Chdir(dir)

	code, _, errOut := runCLI(t, "run")
	if code != 1 {
		t.Fatalf("run with a broken contract exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if want := filepath.Join(dir, ".pluto.toml") + ":3:"; !strings.Contains(errOut, want) {
		t.Fatalf("stderr = %q, want %q", errOut, want)
	}
	if !strings.Contains(errOut, "next: fix the contract and run 'pluto run' again") {
		t.Fatalf("stderr = %q, want the promised edit hint", errOut)
	}
}

// A contract failure the daemon reports keeps its line and gets the hint the
// CLI adds from the daemon's contract fact.
func TestDaemonContractFailureCarriesFileLineAndHint(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".pluto.toml"), []byte("[jobs.dev]\ncommand = \"make\"\ntimeout = \"soon\"\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}

	code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "dev")
	if code != 1 {
		t.Fatalf("run against a broken contract exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{
		filepath.Join(repo, ".pluto.toml") + ":3:",
		"next: fix the contract and run 'pluto run' again",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}

// A contract failure during up names the up retry.
func TestUpContractFailureNamesTheEdit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".pluto.toml"), []byte("[provision]\ncommand = [\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	_, loadErr := contract.Load(dir)
	if loadErr == nil {
		t.Fatal("the contract should fail to load")
	}
	socket, _ := startDaemonWith(t, fakeRunner{upErr: loadErr})
	repo := gitRepo(t)

	code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo)
	if code != 1 {
		t.Fatalf("up against a broken contract exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "next: fix the contract and run 'pluto up' again") {
		t.Fatalf("stderr = %q, want the up retry hint", errOut)
	}
}

// Unknown subcommands get a closest match and a next step, like top-level
// commands.
func TestUnknownSubcommandsSuggestTheClosestMatch(t *testing.T) {
	socket, _ := startDaemon(t)
	deviceConfig(t)

	code, _, errOut := runCLI(t, "--socket", socket, "image", "importt")
	if code != 2 {
		t.Fatalf("image importt exit = %d, want 2 (stderr %q)", code, errOut)
	}
	for _, want := range []string{`unknown image subcommand "importt"`, "next: did you mean 'pluto image import'?"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}

	code, _, errOut = runCLI(t, "device", "list")
	if code != 2 {
		t.Fatalf("device list exit = %d, want 2 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "next: did you mean 'pluto device ls'?") {
		t.Fatalf("stderr = %q, want the closest subcommand", errOut)
	}
}

// ADR 0009: `image ls` names the next step when the daemon cannot answer.
func TestImageLsNamesTheNextStep(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sock")
	code, _, errOut := runCLI(t, "--socket", missing, "image", "ls")
	if code != 1 {
		t.Fatalf("image ls exit = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "next: start the daemon with 'pluto daemon'") {
		t.Fatalf("stderr = %q, want the daemon hint", errOut)
	}
}

// A job-name failure in logs names the jobs command.
func TestLogsUnknownJobNamesTheJobsCommand(t *testing.T) {
	socket, st := startDaemon(t)
	repo := gitRepo(t)
	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit %d: %s", code, errOut)
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("boxes = %d, err = %v", len(boxes), err)
	}
	recordJob(t, st, boxes[0].ID, "make test", 0, 0)

	code, _, errOut := runCLI(t, "--socket", socket, "logs", repo, "--job", "deadbeef")
	if code != 1 {
		t.Fatalf("logs unknown job exit = %d, want 1 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"no job", "next: list the box's jobs with 'pluto jobs"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
	}
}
