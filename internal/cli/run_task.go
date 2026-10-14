package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// yamlContractDir returns the current worktree's root and its loaded pluto.yml
// when one exists. ok is false when there is no pluto.yml, so the legacy TOML
// path keeps its meaning until #150/#151 retire it.
func yamlContractDir() (dir string, project *contract.Project, ok bool, err error) {
	cwd, e := os.Getwd()
	if e != nil {
		return "", nil, false, nil
	}
	dir = cwd
	if root, _, e := gitInfo(cwd); e == nil {
		dir = root
	}
	if _, e := os.Stat(filepath.Join(dir, contract.YAMLFileName)); e != nil {
		return dir, nil, false, nil
	}
	project, err = contract.LoadProject(dir)
	return dir, project, true, err
}

// runDeclaredTask runs a task declared in pluto.yml in the workspace's durable
// box and records it. It is the `pluto run <task>` shape; the legacy job and
// ad-hoc spellings keep their meaning when no pluto.yml is present.
func runDeclaredTask(dir string, project *contract.Project, name, socket string, stdout, stderr io.Writer) int {
	if _, err := project.TaskExec(name); err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	run, err := client.New(socket).RunTask(api.RunTaskRequest{Workspace: dir, Task: name}, stdout)
	if err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	return finishDeclaredRun(run, stderr)
}

// finishDeclaredRun maps a recorded run to the process exit code: the work's
// code wins, so scripts and pipelines see what the command saw.
func finishDeclaredRun(run *state.Run, stderr io.Writer) int {
	if run.State == state.RunDone {
		return 0
	}
	if run.Error != "" {
		fmt.Fprintf(stderr, "pluto: task %s failed: %s\n", run.Task, run.Error)
	}
	if run.ExitCode != 0 {
		return run.ExitCode
	}
	fmt.Fprintf(stderr, "next: show the run with 'pluto show %s'\n", short(run.ID))
	return 1
}
