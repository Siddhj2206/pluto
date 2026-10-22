// Package clientcontext is the identity a laptop client sends with a host
// command: the local git worktree's project, branch, selected remote, HEAD,
// and dirty state.
//
// It is metadata for default target resolution, never authorization, and it
// deliberately carries no local path: a laptop path is not a host worktree
// path. The same type is parsed on the host by internal/workspace, so the
// client and host agree on the wire shape without the client importing the
// host's managed workspace.
package clientcontext

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Env is the environment variable a client sets on the remote command.
const Env = "PLUTO_CLIENT_CONTEXT"

// Context is the compact client identity sent in Env.
type Context struct {
	Project  string `json:"project,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Source   string `json:"source,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Head     string `json:"head,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	NoRemote bool   `json:"no_remote,omitempty"`
}

// Parse decodes a PLUTO_CLIENT_CONTEXT value. An empty value is no context.
func Parse(value string) (*Context, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	var ctx Context
	if err := json.Unmarshal([]byte(value), &ctx); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", Env, err)
	}
	if ctx.NoRemote {
		// A checkout with no usable remote still reports its branch and HEAD so
		// the host can name the right next step instead of guessing a project.
		if ctx.Branch == "" && ctx.Head == "" {
			return nil, fmt.Errorf("invalid %s: branch or head is required", Env)
		}
		ctx.Project = ""
		ctx.Source = ""
		ctx.Remote = ""
		return &ctx, nil
	}
	if ctx.Project == "" && ctx.Source == "" {
		return nil, fmt.Errorf("invalid %s: project or source is required", Env)
	}
	if ctx.Project == "" {
		ctx.Project = ProjectName(ctx.Source)
	}
	return &ctx, nil
}

// FromEnv reads Env from the environment.
func FromEnv() (*Context, error) {
	return Parse(os.Getenv(Env))
}

// Detect resolves dir's git worktree identity for default target resolution.
// A nil context with a nil error means dir is not a git worktree. A checkout
// with no usable remote returns a context with NoRemote set rather than an
// error, so the host can name the next step.
func Detect(dir string) (*Context, error) {
	root, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return nil, nil
	}
	ctx := &Context{}
	if branch, err := gitOutput(root, "symbolic-ref", "--short", "HEAD"); err == nil {
		ctx.Branch = branch
	}
	if head, err := gitOutput(root, "rev-parse", "--verify", "HEAD"); err == nil {
		ctx.Head = head
	}
	if status, err := gitOutput(root, "status", "--porcelain"); err == nil && status != "" {
		ctx.Dirty = true
	}
	remote, ok := selectRemote(root, ctx.Branch)
	if !ok {
		ctx.NoRemote = true
		return ctx, nil
	}
	source, err := gitOutput(root, "remote", "get-url", remote)
	if err != nil || source == "" {
		ctx.NoRemote = true
		return ctx, nil
	}
	ctx.Remote = remote
	ctx.Source = stripCredentials(source)
	ctx.Project = ProjectName(source)
	return ctx, nil
}

// stripCredentials removes a userinfo section from an HTTP(S) remote URL. The
// host authenticates with its own credential helper or agent; the context
// carries no secret and no credential-bearing URL.
func stripCredentials(source string) string {
	parsed, err := url.Parse(source)
	if err != nil || parsed.User == nil {
		return source
	}
	parsed.User = nil
	return parsed.String()
}

// selectRemote picks the remote a box should be created from: origin first,
// then the branch's configured remote, then the only remote. Anything else is
// ambiguous, and guessing could clone the wrong repository.
func selectRemote(root, branch string) (string, bool) {
	out, err := gitOutput(root, "remote")
	if err != nil {
		return "", false
	}
	var remotes []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			remotes = append(remotes, name)
		}
	}
	for _, name := range remotes {
		if name == "origin" {
			return name, true
		}
	}
	if branch != "" {
		if tracked, err := gitOutput(root, "config", "--get", "branch."+branch+".remote"); err == nil {
			if tracked != "" && tracked != "." {
				for _, name := range remotes {
					if name == tracked {
						return name, true
					}
				}
			}
		}
	}
	if len(remotes) == 1 {
		return remotes[0], true
	}
	return "", false
}

// ProjectName derives a project name from a URL or local path.
func ProjectName(source string) string {
	s := strings.TrimRight(strings.TrimSpace(source), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.LastIndexAny(s, "/\\:"); i >= 0 {
		s = s[i+1:]
	}
	return SanitizeName(s)
}

// SanitizeName flattens a name into a registry-safe project or key segment.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "project"
	}
	return out
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(dir string, args ...string) (string, error) {
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
