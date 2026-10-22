package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/workspace"
)

// upTarget is a resolved `up` target. managed means the target came from the
// project registry; worktree is the host checkout either way.
type upTarget struct {
	managed  bool
	project  string
	branch   string
	source   string
	worktree string
}

// resolveUpTarget applies the up resolution rules: --worktree wins, then
// --repo, then a positional project/key/path, then PLUTO_CLIENT_CONTEXT, then
// the current host worktree. Explicit targets never fall through to context.
func resolveUpTarget(socket, positional, worktreeFlag, repoFlag, branchFlag string, stderr io.Writer) (*upTarget, error) {
	w := workspace.Default()
	warn := func(msg string) { fmt.Fprintf(stderr, "warning: %s\n", msg) }

	switch {
	case worktreeFlag != "":
		if filepath.IsAbs(worktreeFlag) {
			dir := filepath.Clean(worktreeFlag)
			if !statDir(dir) {
				return nil, fmt.Errorf("host worktree %s does not exist", dir)
			}
			return &upTarget{worktree: dir}, nil
		}
		return resolveManagedKeyFlag(w, socket, positional, worktreeFlag, branchFlag, warn)
	case repoFlag != "":
		p, _, err := w.EnsureProject(repoFlag, "", "")
		if err != nil {
			return nil, err
		}
		branch := branchFlag
		if branch == "" {
			branch = p.DefaultBranch
		}
		return ensureManagedUp(w, socket, p.Name, branch, "", warn)
	case positional != "":
		if p, err := w.Project(positional); err == nil {
			branch := branchFlag
			if branch == "" {
				branch = p.DefaultBranch
			}
			return ensureManagedUp(w, socket, p.Name, branch, "", warn)
		} else if !errors.Is(err, workspace.ErrProjectNotFound) {
			return nil, err
		}
		if wt, err := w.FindWorktree(positional); err == nil {
			return ensureManagedUp(w, socket, wt.Project, wt.Branch, "", warn)
		}
		if i := strings.Index(positional, "@"); i > 0 {
			project, branch := positional[:i], positional[i+1:]
			if project != "" && branch != "" {
				if _, err := w.Project(project); err == nil {
					return ensureManagedUp(w, socket, project, branch, "", warn)
				}
			}
		}
		if statDir(positional) {
			warnManagedTarget(stderr, positional)
			return &upTarget{worktree: positional}, nil
		}
		return nil, fmt.Errorf("no project, worktree key, or host path named %q", positional)
	default:
		ctx, err := workspace.ClientContextFromEnv()
		if err != nil {
			return nil, err
		}
		if ctx != nil {
			return ensureContextUp(w, socket, ctx, branchFlag, "", warn)
		}
		dir, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		root, _, err := gitInfo(dir)
		if err != nil {
			return nil, fmt.Errorf("%s is not a git worktree; name a project or use 'pluto up --repo <url>'", dir)
		}
		// Inside a managed checkout, resolve its registry identity rather than
		// treating the directory as an ad-hoc host path.
		if wt, err := w.FindWorktree(root); err == nil {
			return ensureManagedUp(w, socket, wt.Project, wt.Branch, "", warn)
		}
		return &upTarget{worktree: root}, nil
	}
}

// resolveManagedKeyFlag resolves a relative --worktree value. The exact
// project@branch form names a new or existing managed target; a known key
// reuses its checkout; with a positional project, the value is a custom key.
func resolveManagedKeyFlag(w *workspace.Workspace, socket, positional, keyFlag, branchFlag string, warn func(string)) (*upTarget, error) {
	if i := strings.Index(keyFlag, "@"); i > 0 {
		project, branch := keyFlag[:i], keyFlag[i+1:]
		if project == "" || branch == "" {
			return nil, fmt.Errorf("--worktree %q is not a valid project@branch key", keyFlag)
		}
		return ensureManagedUp(w, socket, project, branch, "", warn)
	}
	if wt, err := w.FindWorktree(keyFlag); err == nil {
		return ensureManagedUp(w, socket, wt.Project, wt.Branch, "", warn)
	}
	if positional != "" {
		if p, err := w.Project(positional); err == nil {
			branch := branchFlag
			if branch == "" {
				branch = p.DefaultBranch
			}
			return ensureManagedUp(w, socket, p.Name, branch, keyFlag, warn)
		}
	}
	// On a client, a relative --worktree with no positional is a custom
	// host-side key for the checkout's project and branch. An explicit
	// positional target must resolve on its own and never fall back here.
	if positional == "" {
		if ctx, err := workspace.ClientContextFromEnv(); err == nil && ctx != nil {
			return ensureContextUp(w, socket, ctx, branchFlag, keyFlag, warn)
		}
	}
	return nil, fmt.Errorf("no managed worktree %q; use <project>@<branch> or name a project", keyFlag)
}

// ensureContextUp resolves the client's project and branch to a managed
// checkout, with an optional custom --worktree key. It refuses a checkout
// whose committed HEAD is not on origin, so the box never runs older code.
func ensureContextUp(w *workspace.Workspace, socket string, ctx *workspace.ClientContext, branchFlag, key string, warn func(string)) (*upTarget, error) {
	if ctx.NoRemote || ctx.Source == "" {
		return nil, noRemoteError()
	}
	if ctx.Branch == "" && ctx.Head != "" {
		return nil, detachedError()
	}
	p, _, err := w.EnsureProject(ctx.Source, ctx.Project, "")
	if err != nil {
		return nil, err
	}
	branch := branchFlag
	if branch == "" {
		branch = ctx.Branch
	}
	if branch == "" {
		branch = p.DefaultBranch
	}
	target, err := ensureManagedUp(w, socket, p.Name, branch, key, warn)
	if err != nil {
		return nil, err
	}
	// The box checks out a committed ref from origin. If the laptop has commits
	// that ref does not contain, the box would run older code: refuse and ask
	// for a push instead.
	if branch == ctx.Branch {
		if err := w.VerifyClientHead(p.Name, branch, ctx.Head); err != nil {
			return nil, err
		}
	}
	if ctx.Dirty {
		warn("the client worktree has uncommitted changes; the box runs the committed branch")
	}
	return target, nil
}

// missingWorktreeBoxError reports a box whose recorded managed worktree is
// gone. It is typed so the CLI can attach the stale-record next step without
// matching on the message.
type missingWorktreeBoxError struct {
	boxID   string
	project string
	branch  string
	path    string
}

func (e *missingWorktreeBoxError) Error() string {
	return fmt.Sprintf("box %s for %s/%s records worktree %s, which is missing", state.ShortID(e.boxID), e.project, e.branch, e.path)
}

// ensureManagedUp resolves one project/branch to a managed checkout, guarding
// a box whose recorded worktree is gone before a replacement is created.
func ensureManagedUp(w *workspace.Workspace, socket, project, branch, key string, warn func(string)) (*upTarget, error) {
	p, err := w.Project(project)
	if err != nil {
		return nil, err
	}
	if key == "" {
		expected, err := w.ExpectedWorktreePath(project, branch)
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(expected); errors.Is(statErr, os.ErrNotExist) {
			if b := missingWorktreeBox(socket, project, branch, expected); b != nil {
				return nil, &missingWorktreeBoxError{boxID: b.ID, project: project, branch: branch, path: expected}
			}
		}
	} else {
		expected := w.WorktreePath(workspace.SanitizeKey(key))
		if _, statErr := os.Stat(expected); errors.Is(statErr, os.ErrNotExist) {
			if b := missingWorktreeBox(socket, project, branch, expected); b != nil {
				return nil, &missingWorktreeBoxError{boxID: b.ID, project: project, branch: branch, path: expected}
			}
		}
	}
	wt, err := w.EnsureWorktreeAt(project, branch, key, warn)
	if err != nil {
		return nil, err
	}
	return &upTarget{managed: true, project: project, branch: branch, source: p.Source, worktree: wt.Path}, nil
}

// missingWorktreeBox returns a box whose identity matches project/branch and
// whose recorded worktree is path. The daemon may be unreachable; up then
// fails later at the box request with the daemon's own error.
func missingWorktreeBox(socket, project, branch, path string) *state.Box {
	list, err := client.New(socket).ListBoxes()
	if err != nil {
		return nil
	}
	clean := filepath.Clean(path)
	for _, b := range list.Boxes {
		if b.Project == project && b.Branch == branch && filepath.Clean(b.Worktree) == clean {
			return b
		}
	}
	return nil
}

// upTargetHint names the next step for the common resolution failures.
func upTargetHint(err error) []string {
	var missing *workspace.MissingBranchError
	if errors.As(err, &missing) {
		return []string{fmt.Sprintf("push the branch with 'git push -u origin %s', or use --worktree <host path>", missing.Branch)}
	}
	var ahead *workspace.LocalAheadError
	if errors.As(err, &ahead) {
		return []string{fmt.Sprintf("push the branch with 'git push -u origin %s', or use --worktree <host path>", ahead.Branch)}
	}
	if errors.Is(err, workspace.ErrProjectNotFound) {
		return []string{"list projects with 'pluto project ls', or register one with 'pluto project add <url|path>'"}
	}
	if errors.Is(err, workspace.ErrWorktreeNotFound) {
		return []string{"list worktrees with 'pluto worktree ls'"}
	}
	var missingWorktree *missingWorktreeBoxError
	if errors.As(err, &missingWorktree) {
		return []string{"inspect managed state with 'pluto worktree ls', or remove the stale box with 'pluto destroy <id> --yes'"}
	}
	return nil
}
