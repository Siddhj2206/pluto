package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

// noRemoteError explains a client checkout with no usable remote. It is a
// hintError so the command's own next steps are kept and this one is added.
func noRemoteError() error {
	return &hintError{
		Err: errors.New("the client worktree has no usable git remote"),
		Next: []string{
			"add one with 'git remote add origin <url>', or use 'pluto up --worktree <host path>'",
		},
	}
}

// detachedError explains a client checkout on a detached HEAD, where there is
// no branch for the host to check out.
func detachedError() error {
	return &hintError{
		Err:  errors.New("the client worktree has a detached HEAD"),
		Next: []string{"check out a branch with 'git switch <branch>'"},
	}
}

// contextBox resolves PLUTO_CLIENT_CONTEXT to the box for its project and
// branch. present is false when no context was sent; an error means a context
// was sent but cannot name a box.
func contextBox(socket string) (box *state.Box, present bool, err error) {
	ctx, err := workspace.ClientContextFromEnv()
	if err != nil {
		return nil, true, err
	}
	if ctx == nil {
		return nil, false, nil
	}
	if ctx.NoRemote {
		return nil, true, noRemoteError()
	}
	project := workspace.SanitizeName(ctx.Project)
	if ctx.Branch == "" {
		if ctx.Head != "" {
			return nil, true, detachedError()
		}
		return nil, true, &hintError{
			Err:  errors.New("the client context names no branch"),
			Next: []string{"pass a box id or <project>@<branch>"},
		}
	}
	list, err := client.New(socket).ListBoxes()
	if err != nil {
		return nil, true, err
	}
	for _, b := range list.Boxes {
		if b.Project == project && b.Branch == ctx.Branch {
			return b, true, nil
		}
	}
	return nil, true, &hintError{
		Err:  fmt.Errorf("no box for %s/%s", project, ctx.Branch),
		Next: []string{"create it with 'pluto up'", "list boxes with 'pluto ls'"},
	}
}

// resolveDefaultBox resolves a command's target when none was given: the
// client context first, then the host's current git worktree. present is
// false when neither exists, and the caller names the registered projects.
func resolveDefaultBox(socket string) (*state.Box, bool, error) {
	if box, present, err := contextBox(socket); present {
		return box, true, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, false, err
	}
	root, _, err := gitInfo(dir)
	if err != nil {
		return nil, false, nil
	}
	box, err := resolveBox(client.New(socket), root)
	return box, true, err
}

// boxForTarget resolves an explicit target, or the default target when none
// was given. A non-zero code means the failure is already printed.
func boxForTarget(socket, target string, stderr io.Writer) (*state.Box, int) {
	if target != "" {
		box, err := resolveBox(client.New(socket), target)
		if err != nil {
			return nil, fail(stderr, err)
		}
		return box, 0
	}
	box, present, err := resolveDefaultBox(socket)
	if err != nil {
		return nil, fail(stderr, err)
	}
	if !present {
		return nil, noTargetFailure(stderr)
	}
	return box, 0
}

// noTargetFailure says a command needs a target and lists the registered
// projects, so a host outside a checkout never guesses a last-used box.
func noTargetFailure(stderr io.Writer) int {
	fmt.Fprintln(stderr, "pluto: no target: not in a project checkout and no box was given")
	var names []string
	if projects, err := workspace.Default().Projects(); err == nil {
		for _, p := range projects {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stderr, "projects: none (register one with 'pluto project add <url|path>')")
	} else {
		fmt.Fprintf(stderr, "projects: %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintln(stderr, "next: cd into the project checkout, or pass <project>@<branch>")
	fmt.Fprintln(stderr, "next: list boxes with 'pluto ls'")
	return 1
}
