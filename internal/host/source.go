package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

const gitTimeout = 10 * time.Minute

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// tokenFor returns the connected repo's token, only when the URL is that exact
// repo on GitHub, so it can't reach any other server.
func (h *Host) tokenFor(ctx context.Context, repo, url string) string {
	if url != "https://github.com/"+repo+".git" {
		return ""
	}
	r, err := h.Store.Repo(ctx, repo)
	if err != nil {
		return ""
	}
	return r.Token
}

// git runs git without prompting. The token travels in git's environment, which
// keeps it off the command line that other users could see.
func git(ctx context.Context, dir, token string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh:git:file", "GIT_SSH_COMMAND=ssh -oBatchMode=yes")
	if token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+auth)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.Bytes(), nil
}

// fetchSource updates a repo's bare copy on the host and returns its newest commit.
func (h *Host) fetchSource(ctx context.Context, src store.Source) (dir, sha string, err error) {
	h.mu.Lock()
	if h.fetching == nil {
		h.fetching = map[string]*sync.Mutex{}
	}
	lock := h.fetching[src.Repo]
	if lock == nil {
		lock = &sync.Mutex{}
		h.fetching[src.Repo] = lock
	}
	h.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	dir = filepath.Join(h.StateDir, "sources", slug(src.Repo)+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", err
		}
		if _, err := git(ctx, dir, "", "init", "--quiet", "--bare"); err != nil {
			return "", "", err
		}
	}
	token := h.tokenFor(ctx, src.Repo, src.URL)
	refspec := "+refs/heads/" + src.Branch + ":refs/pluto/head"
	if _, err := git(ctx, dir, token, "fetch", "--quiet", "--no-tags", "--depth=1", "--", src.URL, refspec); err != nil {
		if token != "" {
			err = fmt.Errorf("%w (for a private repo, the token also needs Contents: read)", err)
		}
		return dir, "", err
	}
	out, err := git(ctx, dir, "", "rev-parse", "refs/pluto/head")
	if err != nil {
		return dir, "", err
	}
	return dir, strings.TrimSpace(string(out)), nil
}

// readConfig reads .pluto at a commit of a bare repo.
func readConfig(ctx context.Context, dir, sha string) (*task.Config, error) {
	out, err := git(ctx, dir, "", "ls-tree", "-r", "-z", "--name-only", sha, "--", task.Dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, name := range strings.Split(string(out), "\x00") {
		rel, ok := strings.CutPrefix(name, task.Dir+"/")
		if !ok {
			continue
		}
		data, err := git(ctx, dir, "", "show", sha+":"+name)
		if err != nil {
			return nil, err
		}
		files[rel] = data
	}
	return task.Parse(files)
}

// refreshSource fetches a repo and brings its schedules up to date. If the new
// config is broken, the last good one keeps running and the problem is reported.
func (h *Host) refreshSource(ctx context.Context, src store.Source) (store.Source, error) {
	src.FetchedAt = h.now()
	cfg, sha, err := func() (*task.Config, string, error) {
		dir, sha, err := h.fetchSource(ctx, src)
		if err != nil {
			return nil, "", err
		}
		cfg, err := readConfig(ctx, dir, sha)
		return cfg, sha, err
	}()
	// The fetch can take minutes. If the repo was disabled or moved to another
	// branch meanwhile, this answer is about something nobody wants any more.
	current, cerr := h.Store.SourceOf(ctx, src.Repo)
	if errors.Is(cerr, store.ErrNotFound) || (cerr == nil && (current.URL != src.URL || current.Branch != src.Branch)) {
		return src, errors.Join(err, errors.New("the repo's schedules changed during the fetch"))
	}
	if cerr != nil {
		return src, cerr
	}
	if err != nil {
		src.Problem = err.Error()
		return src, errors.Join(err, h.Store.PutSource(ctx, src))
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return src, err
	}
	src.SHA, src.Config, src.Problem = sha, string(data), ""
	if err := h.Store.PutSource(ctx, src); err != nil {
		return src, err
	}
	return src, h.syncSchedules(ctx, src.Repo, cfg)
}

func (h *Host) syncSchedules(ctx context.Context, repo string, cfg *task.Config) error {
	existing, err := h.Store.Schedules(ctx)
	if err != nil {
		return err
	}
	have := map[string]store.Schedule{}
	var errs []error
	for _, sc := range existing {
		if sc.Repo != repo {
			continue
		}
		if t, ok := cfg.Tasks[sc.Task]; !ok || t.Schedule == "" {
			errs = append(errs, h.Store.DeleteSchedule(ctx, repo, sc.Task))
			continue
		}
		have[sc.Task] = sc
	}
	for name, t := range cfg.Tasks {
		if t.Schedule == "" {
			continue
		}
		// A new or edited schedule starts from now, so it can't look missed.
		if sc, ok := have[name]; !ok || sc.Expr != t.Schedule {
			errs = append(errs, h.Store.PutSchedule(ctx, store.Schedule{Repo: repo, Task: name, Expr: t.Schedule, CheckedAt: h.now()}))
		}
	}
	return errors.Join(errs...)
}

// refreshSources starts background refreshes for sources that are due.
func (h *Host) refreshSources(ctx context.Context) {
	sources, err := h.Store.Sources(ctx)
	if err != nil {
		log.Printf("listing sources: %v", err)
		return
	}
	for _, src := range sources {
		if h.now().Sub(src.FetchedAt) < h.Config.RefreshSource {
			continue
		}
		h.mu.Lock()
		busy := h.refreshing[src.Repo]
		if !busy {
			if h.refreshing == nil {
				h.refreshing = map[string]bool{}
			}
			h.refreshing[src.Repo] = true
		}
		h.mu.Unlock()
		if busy {
			continue
		}
		h.prepares.Add(1)
		go func() {
			defer h.prepares.Done()
			if _, err := h.refreshSource(ctx, src); err != nil {
				log.Printf("refreshing %s: %v", src.Repo, err)
			}
			h.mu.Lock()
			delete(h.refreshing, src.Repo)
			h.mu.Unlock()
		}()
	}
}

// checkoutSource gets the plan and files a scheduled run will use. If the remote
// can't be reached, the last fetched commit still runs, and the run says so.
func (h *Host) checkoutSource(ctx context.Context, repo, name string) (plan Plan, tree, sha, note string, err error) {
	src, err := h.Store.SourceOf(ctx, repo)
	if errors.Is(err, store.ErrNotFound) {
		return plan, "", "", "", errors.New("this repo's schedules were disabled")
	}
	if err != nil {
		return plan, "", "", "", err
	}
	dir, sha, err := h.fetchSource(ctx, src)
	if err != nil {
		if src.SHA == "" || dir == "" {
			return plan, "", "", "", err
		}
		sha = src.SHA
		note = "couldn't fetch the branch, so this used the last commit fetched"
	}
	cfg, err := readConfig(ctx, dir, sha)
	if err != nil && src.SHA != "" && src.SHA != sha {
		// A broken push doesn't stop the work: the last good commit still runs.
		note = "the branch's config is broken (" + err.Error() + "), so this used the last good commit"
		sha = src.SHA
		cfg, err = readConfig(ctx, dir, sha)
	}
	if err != nil {
		return plan, "", "", "", err
	}
	t, ok := cfg.Tasks[name]
	if !ok {
		return plan, "", "", "", fmt.Errorf("task %s is no longer in %s", name, src.Branch)
	}
	uploads := filepath.Join(h.StateDir, "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		return plan, "", "", "", err
	}
	f, err := os.CreateTemp(uploads, "source-*.tar")
	if err != nil {
		return plan, "", "", "", err
	}
	f.Close()
	if _, err := git(ctx, dir, "", "archive", "--format=tar", "--output="+f.Name(), sha); err != nil {
		os.Remove(f.Name())
		return plan, "", "", "", err
	}
	return Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}, f.Name(), sha, note, nil
}

// pruneSources removes bare copies in the state directory's sources/ that no
// source maps to, such as one left under an old spelling of a repo. It only
// touches directories named like the ones fetchSource makes.
func (h *Host) pruneSources(ctx context.Context) error {
	if h.StateDir == "" {
		return nil
	}
	root := filepath.Join(h.StateDir, "sources")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sources, err := h.Store.Sources(ctx)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, s := range sources {
		keep[slug(s.Repo)+".git"] = true
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || keep[e.Name()] || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		errs = append(errs, os.RemoveAll(filepath.Join(root, e.Name())))
	}
	return errors.Join(errs...)
}
