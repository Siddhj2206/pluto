// Package workspace owns the host's managed code workspace: the project
// registry, one normal clone per project, and one flat worktree per branch.
//
// It is deliberately separate from internal/state. State is the daemon's
// single-writer box store; the workspace is the human-visible place where
// committed checkouts live. Removing ~/.pluto therefore never removes a box
// disk. Operations are serialized per project and registry writes are atomic.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Siddhj2206/pluto/internal/clientcontext"
)

// ClientContextEnv is the environment value a client sends to the host CLI
// with the local worktree's identity. It is metadata for default target
// resolution, never authorization.
const ClientContextEnv = clientcontext.Env

// ErrProjectNotFound reports a project that is not registered.
var ErrProjectNotFound = errors.New("project not found")

// ErrWorktreeNotFound reports a worktree key or path that is not managed.
var ErrWorktreeNotFound = errors.New("worktree not found")

// MissingBranchError reports a branch absent from the project's origin.
type MissingBranchError struct {
	Project string
	Branch  string
}

func (e *MissingBranchError) Error() string {
	return fmt.Sprintf("branch %q does not exist on origin in project %q", e.Branch, e.Project)
}

// LocalAheadError reports a client HEAD whose commits are not on the remote
// branch. Creating the box anyway would run an older commit, so up refuses.
type LocalAheadError struct {
	Project string
	Branch  string
}

func (e *LocalAheadError) Error() string {
	return fmt.Sprintf("local branch %q has commits that are not on origin in project %q", e.Branch, e.Project)
}

// Project is one registered repository. The registry map key is Name.
type Project struct {
	Name          string     `toml:"-"`
	Source        string     `toml:"source"`
	DefaultBranch string     `toml:"default_branch"`
	Clone         string     `toml:"clone"`
	CreatedAt     time.Time  `toml:"created_at"`
	LastFetch     *time.Time `toml:"last_fetch,omitempty"`
	// Repo is the pre-rename registry key for Clone. It is read for
	// compatibility and cleared on load, so a rewrite stores "clone".
	Repo string `toml:"repo,omitempty"`
}

// Worktree is one checkout of a project's branch. Default is the managed
// clone itself; every other branch has a flat worktree under the root.
type Worktree struct {
	Project string
	Branch  string
	Key     string
	Path    string
	Default bool
	Managed bool
	Created bool
}

// Workspace is a managed workspace rooted at one directory.
type Workspace struct {
	Root string
}

// DefaultRoot is the managed workspace root: $PLUTO_HOME, else ~/.pluto.
func DefaultRoot() string {
	if v := os.Getenv("PLUTO_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("pluto-home-%d", os.Getuid()))
	}
	return filepath.Join(home, ".pluto")
}

// Default opens the workspace selected by the environment.
func Default() *Workspace { return At(DefaultRoot()) }

// At opens a workspace at an explicit root.
func At(root string) *Workspace { return &Workspace{Root: filepath.Clean(root)} }

// ProjectsPath is the registry file.
func (w *Workspace) ProjectsPath() string { return filepath.Join(w.Root, "projects.toml") }

// ReposPath is the directory holding one normal clone per project.
func (w *Workspace) ReposPath() string { return filepath.Join(w.Root, "repos") }

// WorktreesPath is the directory holding one flat worktree per branch.
func (w *Workspace) WorktreesPath() string { return filepath.Join(w.Root, "worktrees") }

// RepoPath is a project's managed clone path.
func (w *Workspace) RepoPath(project string) string {
	return filepath.Join(w.ReposPath(), project)
}

// WorktreePath is the path for a managed worktree key.
func (w *Workspace) WorktreePath(key string) string {
	return filepath.Join(w.WorktreesPath(), key)
}

// Key is the short, stable worktree key: <project>-<sanitized branch>.
func Key(project, branch string) string {
	return project + "-" + SanitizeBranch(branch)
}

// ExactKey is the unambiguous target form: <project>@<branch>.
func ExactKey(project, branch string) string { return project + "@" + branch }

// SanitizeKey flattens an explicit managed key into one path-safe segment.
func SanitizeKey(key string) string { return sanitizeName(key) }

// SanitizeBranch flattens a branch name into a path-safe key segment. Path
// separators and any character outside [A-Za-z0-9._-] become '-'.
func SanitizeBranch(branch string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range branch {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}

// ProjectNameFromSource derives a project name from a URL or local path.
func ProjectNameFromSource(source string) string { return clientcontext.ProjectName(source) }

// SanitizeName flattens a name into a registry-safe project segment.
func SanitizeName(name string) string { return clientcontext.SanitizeName(name) }

func sanitizeName(name string) string { return clientcontext.SanitizeName(name) }

// Project returns one registered project.
func (w *Workspace) Project(name string) (*Project, error) {
	reg, err := w.loadRegistry()
	if err != nil {
		return nil, err
	}
	p, ok := reg.Project[name]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrProjectNotFound, name)
	}
	p.Name = name
	return &p, nil
}

// Projects returns every registered project sorted by name.
func (w *Workspace) Projects() ([]Project, error) {
	reg, err := w.loadRegistry()
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(reg.Project))
	for name, p := range reg.Project {
		p.Name = name
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// EnsureProject registers a project, cloning it once. name and branch are
// optional overrides; branch records the default branch. created reports a
// first registration. An existing project with a different source is refused,
// and a registered project whose clone disappeared fails loudly instead of
// being silently re-cloned.
func (w *Workspace) EnsureProject(source, name, branch string) (*Project, bool, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, false, errors.New("a repository URL or path is required")
	}
	if err := validateSource(source); err != nil {
		return nil, false, err
	}
	if name == "" {
		name = ProjectNameFromSource(source)
	} else {
		name = sanitizeName(name)
	}
	unlock, err := w.lock("project-" + name)
	if err != nil {
		return nil, false, err
	}
	defer unlock()

	if existing, err := w.Project(name); err == nil {
		if !sameSource(existing.Source, source) {
			return nil, false, fmt.Errorf("project %q is already registered from %s", name, existing.Source)
		}
		if !isGitDir(existing.Clone) {
			return nil, false, fmt.Errorf("managed clone for project %q is missing: %s", name, existing.Clone)
		}
		return existing, false, nil
	} else if !errors.Is(err, ErrProjectNotFound) {
		return nil, false, err
	}

	repo := w.RepoPath(name)
	if isGitDir(repo) {
		// A clone without a registry entry (for example after the registry was
		// removed by hand) is adopted rather than cloned twice.
	} else if _, err := os.Stat(repo); err == nil {
		return nil, false, fmt.Errorf("managed clone path %s exists and is not a git repository", repo)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	} else {
		if err := os.MkdirAll(w.ReposPath(), 0o755); err != nil {
			return nil, false, fmt.Errorf("create managed repos directory: %w", err)
		}
		cloneFrom := source
		if isLocalPath(source) {
			cloneFrom, err = filepath.Abs(source)
			if err != nil {
				return nil, false, err
			}
		}
		if out, err := git("", "clone", "--", cloneFrom, repo); err != nil {
			return nil, false, fmt.Errorf("clone project %q: %w (%s)", name, err, strings.TrimSpace(out))
		}
	}

	def := strings.TrimSpace(branch)
	if def == "" {
		out, err := git(repo, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return nil, false, fmt.Errorf("detect default branch for project %q: %w", name, err)
		}
		def = strings.TrimSpace(out)
	}
	if def == "" {
		return nil, false, fmt.Errorf("detect default branch for project %q: git reported no branch", name)
	}
	if branch != "" && branchAt(repo) != def {
		if w.localBranchExists(repo, def) {
			if out, err := git(repo, "checkout", def); err != nil {
				return nil, false, fmt.Errorf("check out default branch %q in project %q: %w (%s)", def, name, err, strings.TrimSpace(out))
			}
		} else if w.remoteBranchExists(repo, def) {
			if out, err := git(repo, "checkout", "-b", def, "--track", "origin/"+def); err != nil {
				return nil, false, fmt.Errorf("check out default branch %q in project %q: %w (%s)", def, name, err, strings.TrimSpace(out))
			}
		} else {
			return nil, false, &MissingBranchError{Project: name, Branch: def}
		}
	}
	now := time.Now().UTC()
	p := Project{Name: name, Source: source, DefaultBranch: def, Clone: repo, CreatedAt: now}
	err = w.withRegistry(func(reg *registry) error {
		if existing, ok := reg.Project[name]; ok {
			if !sameSource(existing.Source, source) {
				return fmt.Errorf("project %q is already registered from %s", name, existing.Source)
			}
			existing.Name = name
			p = existing
			return nil
		}
		if reg.Project == nil {
			reg.Project = map[string]Project{}
		}
		reg.Project[name] = Project{Source: source, DefaultBranch: def, Clone: repo, CreatedAt: now}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// EnsureWorktree creates or reuses the checkout for a project's branch. The
// default branch uses the managed clone; any other branch gets a flat worktree
// keyed <project>-<sanitized branch>. A branch that exists only on origin is
// created as a tracking branch; a branch missing from origin is an error.
func (w *Workspace) EnsureWorktree(project, branch string, warn func(string)) (*Worktree, error) {
	return w.EnsureWorktreeAt(project, branch, "", warn)
}

// EnsureWorktreeAt is EnsureWorktree with an explicit managed key. An empty
// key uses the default <project>-<branch> key. A custom key names a flat
// directory under the worktrees root; it is the --worktree override.
func (w *Workspace) EnsureWorktreeAt(project, branch, key string, warn func(string)) (*Worktree, error) {
	if key != "" {
		key = SanitizeKey(key)
	}
	p, err := w.Project(project)
	if err != nil {
		return nil, err
	}
	unlock, err := w.lock("project-" + project)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// Reload under the lock: another process may have changed the registry.
	if p, err = w.Project(project); err != nil {
		return nil, err
	}
	if branch == "" {
		branch = p.DefaultBranch
	}
	if branch == p.DefaultBranch {
		if !isGitDir(p.Clone) {
			return nil, fmt.Errorf("managed clone for project %q is missing: %s", project, p.Clone)
		}
		if branchAt(p.Clone) != branch {
			if !w.localBranchExists(p.Clone, branch) {
				return nil, fmt.Errorf("managed clone for project %q has no branch %q", project, branch)
			}
			if out, err := git(p.Clone, "checkout", branch); err != nil {
				return nil, fmt.Errorf("check out default branch %q in project %q: %w (%s)", branch, project, err, strings.TrimSpace(out))
			}
		}
		return &Worktree{Project: project, Branch: branch, Key: project, Path: p.Clone, Default: true, Managed: true}, nil
	}

	fetched := true
	if hasOrigin(p.Clone) {
		if out, err := git(p.Clone, "fetch", "--prune", "origin"); err != nil {
			fetched = false
			if warn != nil {
				warn(fmt.Sprintf("could not fetch origin for project %q: %v (%s); using local refs", project, err, strings.TrimSpace(out)))
			}
		}
	} else if warn != nil {
		warn(fmt.Sprintf("project %q has no origin remote; using local refs", project))
	}
	if fetched {
		w.touchFetch(project)
	}
	_, _ = git(p.Clone, "worktree", "prune")

	existing, err := w.worktreesFor(*p)
	if err != nil {
		return nil, err
	}
	for i := range existing {
		if existing[i].Branch == branch && !existing[i].Default {
			if key != "" && existing[i].Key != key {
				return nil, fmt.Errorf("branch %q already has worktree %s; remove it first or reuse that key", branch, existing[i].Key)
			}
			return &existing[i], nil
		}
	}

	if !w.remoteBranchExists(p.Clone, branch) {
		return nil, &MissingBranchError{Project: project, Branch: branch}
	}

	baseKey := Key(project, branch)
	if key == "" {
		key = baseKey
	}
	path := w.WorktreePath(key)
	if _, err := os.Stat(path); err == nil {
		if branchAt(path) == branch {
			return &Worktree{Project: project, Branch: branch, Key: key, Path: path, Managed: true}, nil
		}
		if key != baseKey {
			return nil, fmt.Errorf("worktree path %s is occupied", path)
		}
		key = baseKey + "-" + shortHash(branch)
		path = w.WorktreePath(key)
		if _, err := os.Stat(path); err == nil {
			if branchAt(path) == branch {
				return &Worktree{Project: project, Branch: branch, Key: key, Path: path, Managed: true}, nil
			}
			return nil, fmt.Errorf("worktree path %s is occupied", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(w.WorktreesPath(), 0o755); err != nil {
		return nil, fmt.Errorf("create managed worktrees directory: %w", err)
	}
	var out string
	if w.localBranchExists(p.Clone, branch) {
		out, err = git(p.Clone, "worktree", "add", path, branch)
	} else {
		out, err = git(p.Clone, "worktree", "add", "-b", branch, path, "origin/"+branch)
		if err == nil {
			_, _ = git(path, "branch", "--set-upstream-to=origin/"+branch)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("create worktree for %s/%s: %w (%s)", project, branch, err, strings.TrimSpace(out))
	}
	return &Worktree{Project: project, Branch: branch, Key: key, Path: path, Managed: true, Created: true}, nil
}

// VerifyClientHead checks that the client's committed HEAD is already on the
// project's remote branch. A branch that is absent, deleted, or ahead of
// origin has no box-worthy commit, so up fails with a push hint rather than
// silently running an older checkout. An empty head skips the check.
func (w *Workspace) VerifyClientHead(project, branch, head string) error {
	head = strings.TrimSpace(head)
	if head == "" {
		return nil
	}
	p, err := w.Project(project)
	if err != nil {
		return err
	}
	if hasOrigin(p.Clone) {
		// A just-pushed branch must be visible here. An offline fetch leaves the
		// last fetched refs in place, so the check stays conservative.
		_, _ = git(p.Clone, "fetch", "--prune", "origin")
	}
	tip, err := gitOutput(p.Clone, "rev-parse", "--verify", "origin/"+branch)
	if err != nil || tip == "" {
		return &LocalAheadError{Project: project, Branch: branch}
	}
	if _, err := git(p.Clone, "merge-base", "--is-ancestor", head, tip); err != nil {
		return &LocalAheadError{Project: project, Branch: branch}
	}
	return nil
}

// ExpectedWorktreePath returns where EnsureWorktree will look for a branch
// before any collision suffix is chosen. It lets a caller detect a box whose
// recorded worktree is gone before creating a replacement checkout.
func (w *Workspace) ExpectedWorktreePath(project, branch string) (string, error) {
	p, err := w.Project(project)
	if err != nil {
		return "", err
	}
	if branch == "" {
		branch = p.DefaultBranch
	}
	if branch == p.DefaultBranch {
		return p.Clone, nil
	}
	return w.WorktreePath(Key(project, branch)), nil
}

// Worktrees lists the checkouts of one project, or of every project when
// project is empty. The default branch is the managed clone; linked worktrees
// are reported with their flat key.
func (w *Workspace) Worktrees(project string) ([]Worktree, error) {
	projects, err := w.Projects()
	if err != nil {
		return nil, err
	}
	if project != "" {
		p, err := w.Project(project)
		if err != nil {
			return nil, err
		}
		projects = []Project{*p}
	}
	var out []Worktree
	for _, p := range projects {
		list, err := w.worktreesFor(p)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

// FindWorktree resolves a key, exact project@branch form, project name, or
// host path to one worktree.
func (w *Workspace) FindWorktree(target string) (*Worktree, error) {
	if target == "" {
		return nil, fmt.Errorf("%w: empty target", ErrWorktreeNotFound)
	}
	projects, err := w.Projects()
	if err != nil {
		return nil, err
	}
	byPath := filepath.Clean(target)
	if abs, err := filepath.Abs(target); err == nil {
		byPath = abs
	}
	seen := map[string]bool{}
	var matches []Worktree
	for _, p := range projects {
		list, err := w.worktreesFor(p)
		if err != nil {
			return nil, err
		}
		for i := range list {
			wt := list[i]
			if wt.Path != byPath && wt.Key != target && ExactKey(wt.Project, wt.Branch) != target {
				continue
			}
			if seen[wt.Path] {
				continue
			}
			seen[wt.Path] = true
			matches = append(matches, wt)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w %q", ErrWorktreeNotFound, target)
	case 1:
		return &matches[0], nil
	default:
		var names []string
		for _, wt := range matches {
			names = append(names, ExactKey(wt.Project, wt.Branch))
		}
		return nil, fmt.Errorf("worktree %q is ambiguous: matches %s", target, strings.Join(names, ", "))
	}
}

// RemoveWorktree removes one managed worktree. The default branch's clone and
// a worktree with uncommitted changes are refused unless force is set. The
// caller checks for boxes; this package only owns git and the filesystem.
func (w *Workspace) RemoveWorktree(target string, force bool) (*Worktree, error) {
	wt, err := w.FindWorktree(target)
	if err != nil {
		return nil, err
	}
	unlock, err := w.lock("project-" + wt.Project)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Resolve again under the project lock so a concurrent up cannot create a
	// worktree between the check and the removal.
	if wt, err = w.FindWorktree(target); err != nil {
		return nil, err
	}
	if wt.Default {
		return nil, fmt.Errorf("the default branch uses the managed clone %s; remove the project instead", wt.Path)
	}
	if !force && dirty(wt.Path) {
		return nil, fmt.Errorf("worktree %s has uncommitted changes", wt.Key)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.Path)
	if _, statErr := os.Stat(wt.Path); errors.Is(statErr, os.ErrNotExist) {
		// The checkout was removed by hand; prune its stale git registration
		// and treat the removal as complete.
		if p, err := w.Project(wt.Project); err == nil {
			_, _ = git(p.Clone, "worktree", "prune")
		}
		return wt, nil
	}
	if out, err := git(wt.Path, args...); err != nil {
		// The worktree path is itself a repository; run from its project when
		// possible so a removed or moved checkout still resolves.
		if p, perr := w.Project(wt.Project); perr == nil {
			out, err = git(p.Clone, args...)
		}
		if err != nil {
			return nil, fmt.Errorf("remove worktree %s: %w (%s)", wt.Key, err, strings.TrimSpace(out))
		}
	}
	if p, err := w.Project(wt.Project); err == nil {
		_, _ = git(p.Clone, "worktree", "prune")
	}
	return wt, nil
}

// RemoveProject removes every managed worktree and the clone of one project,
// then its registry entry. Dirty checkouts are refused unless force is set.
// The caller checks for boxes; this package only owns git and the filesystem.
func (w *Workspace) RemoveProject(name string, force bool) error {
	p, err := w.Project(name)
	if err != nil {
		return err
	}
	unlock, err := w.lock("project-" + name)
	if err != nil {
		return err
	}
	defer unlock()
	if p, err = w.Project(name); err != nil {
		return err
	}
	if !isGitDir(p.Clone) {
		// A missing clone is recoverable: drop the registry entry so the
		// project can be added again.
		if err := os.RemoveAll(p.Clone); err != nil {
			return fmt.Errorf("remove managed clone %s: %w", p.Clone, err)
		}
		return w.withRegistry(func(reg *registry) error {
			delete(reg.Project, name)
			return nil
		})
	}
	if !force && dirty(p.Clone) {
		return fmt.Errorf("project %q has uncommitted changes in %s", name, p.Clone)
	}
	list, err := w.worktreesFor(*p)
	if err != nil {
		return err
	}
	for _, wt := range list {
		if wt.Default {
			continue
		}
		if !force && dirty(wt.Path) {
			return fmt.Errorf("worktree %s has uncommitted changes", wt.Key)
		}
	}
	for _, wt := range list {
		if wt.Default {
			continue
		}
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, wt.Path)
		if out, err := git(p.Clone, args...); err != nil {
			if !force {
				return fmt.Errorf("remove worktree %s: %w (%s)", wt.Key, err, strings.TrimSpace(out))
			}
			_ = os.RemoveAll(wt.Path)
		}
	}
	if err := os.RemoveAll(p.Clone); err != nil {
		return fmt.Errorf("remove managed clone %s: %w", p.Clone, err)
	}
	return w.withRegistry(func(reg *registry) error {
		delete(reg.Project, name)
		return nil
	})
}

// ClientContext is the compact identity a client sends for default target
// resolution. It is metadata only.
type ClientContext = clientcontext.Context

// ParseClientContext decodes PLUTO_CLIENT_CONTEXT. An empty value is no
// context.
func ParseClientContext(value string) (*ClientContext, error) {
	return clientcontext.Parse(value)
}

// ClientContextFromEnv reads PLUTO_CLIENT_CONTEXT from the environment.
func ClientContextFromEnv() (*ClientContext, error) {
	return clientcontext.FromEnv()
}

// registry is projects.toml.
type registry struct {
	Project map[string]Project `toml:"project"`
}

func (w *Workspace) loadRegistry() (*registry, error) {
	reg := &registry{}
	data, err := os.ReadFile(w.ProjectsPath())
	if errors.Is(err, os.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project registry: %w", err)
	}
	if err := toml.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", w.ProjectsPath(), err)
	}
	for name, p := range reg.Project {
		if p.Clone == "" && p.Repo != "" {
			p.Clone = p.Repo
			p.Repo = ""
			reg.Project[name] = p
		}
	}
	return reg, nil
}

func (w *Workspace) withRegistry(change func(*registry) error) error {
	unlock, err := w.lock("registry")
	if err != nil {
		return err
	}
	defer unlock()
	reg, err := w.loadRegistry()
	if err != nil {
		return err
	}
	if err := change(reg); err != nil {
		return err
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(reg); err != nil {
		return fmt.Errorf("encode project registry: %w", err)
	}
	if err := os.MkdirAll(w.Root, 0o755); err != nil {
		return fmt.Errorf("create workspace root: %w", err)
	}
	return writeFileAtomic(w.ProjectsPath(), []byte(b.String()), 0o644)
}

func (w *Workspace) touchFetch(name string) {
	now := time.Now().UTC()
	_ = w.withRegistry(func(reg *registry) error {
		p, ok := reg.Project[name]
		if !ok {
			return nil
		}
		p.LastFetch = &now
		reg.Project[name] = p
		return nil
	})
}

func (w *Workspace) worktreesFor(p Project) ([]Worktree, error) {
	out, err := git(p.Clone, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees for project %q: %w", p.Name, err)
	}
	var list []Worktree
	var cur *Worktree
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				list = append(list, *cur)
			}
			path := strings.TrimPrefix(line, "worktree ")
			cur = &Worktree{Project: p.Name, Path: filepath.Clean(path), Key: filepath.Base(path)}
		case cur != nil && strings.HasPrefix(line, "branch refs/heads/"):
			cur.Branch = strings.TrimPrefix(line, "branch refs/heads/")
		case cur != nil && line == "detached":
			cur.Branch = "(detached)"
		}
	}
	if cur != nil {
		list = append(list, *cur)
	}
	managedRoot := filepath.Clean(w.WorktreesPath()) + string(filepath.Separator)
	for i := range list {
		wt := &list[i]
		if filepath.Clean(wt.Path) == filepath.Clean(p.Clone) {
			wt.Default = true
			wt.Key = p.Name
			wt.Branch = p.DefaultBranch
			wt.Managed = true
			continue
		}
		wt.Managed = strings.HasPrefix(filepath.Clean(wt.Path)+string(filepath.Separator), managedRoot)
	}
	return list, nil
}

func (w *Workspace) lock(name string) (func(), error) {
	dir := filepath.Join(w.Root, ".locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace lock directory: %w", err)
	}
	return lockFile(filepath.Join(dir, name+".lock"))
}

func (w *Workspace) remoteBranchExists(repo, branch string) bool {
	_, err := git(repo, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return err == nil
}

func (w *Workspace) localBranchExists(repo, branch string) bool {
	_, err := git(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func hasOrigin(repo string) bool {
	out, err := git(repo, "remote", "get-url", "origin")
	return err == nil && strings.TrimSpace(out) != ""
}

func isGitDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func branchAt(dir string) string {
	out, err := git(dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func dirty(dir string) bool {
	out, err := git(dir, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) != ""
}

// validateSource refuses a URL that embeds credentials. Git's configured
// credential helper or SSH agent owns authentication; the registry stores no
// secret.
func validateSource(source string) error {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme == "" {
		return nil
	}
	if parsed.User == nil {
		return nil
	}
	_, hasPassword := parsed.User.Password()
	if hasPassword || parsed.Scheme != "ssh" {
		return errors.New("repository URL must not contain credentials; configure Git's credential helper or SSH agent")
	}
	return nil
}

func isLocalPath(source string) bool {
	if strings.HasPrefix(source, "file://") {
		return false
	}
	if strings.Contains(source, "://") {
		return false
	}
	// scp-like user@host:path is a remote, not a local path.
	if strings.Contains(source, "@") && strings.Contains(source, ":") {
		return false
	}
	return true
}

func sameSource(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		if isLocalPath(s) {
			if abs, err := filepath.Abs(s); err == nil {
				return filepath.Clean(abs)
			}
		}
		return strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	}
	return norm(a) == norm(b)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}
