// Package vcs is the boundary between the core and a version-control system.
// The core knows workspaces, not branches: an adapter resolves a workspace
// path to the repo identity and revision a box is created from (ADR 0015).
package vcs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Workspace is a repo checkout on a host, identified by its path. Remote is
// the repo's identity (the origin URL) when it has one.
type Workspace struct {
	Path   string
	Remote string
}

// Revision is the content a box is seeded from: an opaque commit identifier.
type Revision struct {
	Commit string
}

// Adapter resolves a workspace path to the repo identity and revision a box
// boots from. It is the only place that understands a version-control system.
type Adapter interface {
	Resolve(ctx context.Context, path string) (Workspace, Revision, error)
}

// Git is the git-backed Adapter. Run is replaceable so tests can inject a git
// shim; nil runs the git binary.
type Git struct {
	Run func(ctx context.Context, path string, args ...string) ([]byte, error)
}

// NewGit returns a git-backed adapter.
func NewGit() *Git { return &Git{} }

func (g *Git) run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if g.Run != nil {
		return g.Run(ctx, path, args...)
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Resolve reports the workspace's canonical path, origin remote (empty when
// local-only), and current revision.
func (g *Git) Resolve(ctx context.Context, path string) (Workspace, Revision, error) {
	if strings.TrimSpace(path) == "" {
		return Workspace{}, Revision{}, errors.New("workspace path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Workspace{}, Revision{}, fmt.Errorf("resolve workspace path: %w", err)
	}
	if _, err := g.run(ctx, abs, "rev-parse", "--is-inside-work-tree"); err != nil {
		return Workspace{}, Revision{}, fmt.Errorf("%s is not a git workspace", abs)
	}
	commit, err := g.run(ctx, abs, "rev-parse", "HEAD")
	if err != nil {
		return Workspace{}, Revision{}, fmt.Errorf("resolve revision for %s: %w", abs, err)
	}
	ws := Workspace{Path: abs}
	if remote, err := g.run(ctx, abs, "remote", "get-url", "origin"); err == nil {
		ws.Remote = strings.TrimSpace(string(remote))
	}
	return ws, Revision{Commit: strings.TrimSpace(string(commit))}, nil
}

// Fake is a controllable Adapter for tests. Calls records every resolved path.
type Fake struct {
	Workspace Workspace
	Revision  Revision
	Err       error
	Calls     []string
}

// Resolve returns the fake's scripted facts. A zero Workspace.Path defaults to
// the requested path.
func (f *Fake) Resolve(_ context.Context, path string) (Workspace, Revision, error) {
	f.Calls = append(f.Calls, path)
	if f.Err != nil {
		return Workspace{}, Revision{}, f.Err
	}
	ws := f.Workspace
	if ws.Path == "" {
		ws.Path = path
	}
	return ws, f.Revision, nil
}
