package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/contract"
)

// runCICommand is the manual CI trigger (#148): it runs a declared task once in
// a fresh ephemeral box seeded from the workspace's provisioned package layer.
// The box mounts no durable volumes, so CI never reads or mutates a durable
// box's volumes; a successful run discards the box, a failed run retains it and
// the message names the attach path.
func runCICommand(args []string, socket string, stdout, stderr io.Writer) int {
	if maybeHelp(args, "ci", stdout) {
		return 0
	}
	fs := flag.NewFlagSet("ci", flag.ContinueOnError)
	workspace := fs.String("workspace", "", "workspace path (default: current directory)")
	if code := parseCommand(fs, args, stderr, "usage: pluto ci <task> [--workspace PATH]"); code != 0 {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: pluto ci <task> [--workspace PATH]")
		return 2
	}
	task := fs.Arg(0)
	dir := *workspace
	if dir == "" {
		d, _, _, err := yamlContractDir()
		if err != nil {
			return fail(stderr, err, contractRunHint(err, "pluto ci")...)
		}
		dir = d
	} else if abs, err := filepath.Abs(dir); err == nil {
		// The daemon resolves the workspace against its own cwd, so send an
		// absolute path.
		dir = abs
	}
	project, err := contract.LoadProject(dir)
	if err != nil {
		return fail(stderr, err, contractRunHint(err, "pluto ci")...)
	}
	if _, err := project.TaskExec(task); err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	run, err := client.New(socket).RunCI(api.RunRequest{Workspace: dir, Task: task}, stdout)
	if err != nil {
		return fail(stderr, err, "list the workspace's tasks in pluto.yml")
	}
	return finishRun(run, stderr, true)
}
