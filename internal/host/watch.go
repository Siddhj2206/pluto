package host

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

// Watching connected repos for pushes (ADR 0011). A look is one git ls-remote,
// which costs none of the token's API requests. A branch whose commit is new or
// has changed since the last look is a push: its commit is fetched, its .pluto
// is read, and each task with push: true is queued.

// repoURL is where to fetch a connected repo from.
func (h *Host) repoURL(repo string) string {
	if h.RepoURL != nil {
		return h.RepoURL(repo)
	}
	return "https://github.com/" + repo + ".git"
}

// botSuffix ends the author name of a bot account, such as dependabot[bot].
const botSuffix = "[bot]"

// watchRepos starts a background look at each connected repo that is due. Like
// refreshSources, it never makes the loop wait for git.
func (h *Host) watchRepos(ctx context.Context) {
	if h.Config.WatchEvery <= 0 || h.StateDir == "" {
		return
	}
	repos, err := h.Store.Repos(ctx)
	if err != nil {
		log.Printf("listing repos to watch: %v", err)
		return
	}
	for _, repo := range repos {
		due, err := h.watchDue(ctx, repo.Name)
		if err != nil {
			log.Printf("watching %s: %v", repo.Name, err)
			continue
		}
		if !due {
			continue
		}
		h.mu.Lock()
		busy := h.watching[repo.Name]
		if !busy {
			if h.watching == nil {
				h.watching = map[string]bool{}
			}
			h.watching[repo.Name] = true
		}
		h.mu.Unlock()
		if busy {
			continue
		}
		h.prepares.Add(1)
		go func() {
			defer h.prepares.Done()
			h.checkRepo(ctx, repo.Name)
			h.mu.Lock()
			delete(h.watching, repo.Name)
			h.mu.Unlock()
		}()
	}
}

// watchDue reports whether it's time to look at a repo again. After a failure
// the wait grows, so a repo that can't be read isn't asked every few seconds.
func (h *Host) watchDue(ctx context.Context, repo string) (bool, error) {
	h.mu.Lock()
	b := h.watchWait[repo]
	h.mu.Unlock()
	if b != nil && h.now().Before(b.until) {
		return false, nil
	}
	w, err := h.Store.Watch(ctx, repo)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return h.now().Sub(w.CheckedAt) >= h.Config.WatchEvery, nil
}

// checkRepo looks at a repo's branches once and handles every push it finds.
func (h *Host) checkRepo(ctx context.Context, repo string) {
	url := h.repoURL(repo)
	token := h.tokenFor(ctx, repo, url)
	heads, def, err := h.remoteHeads(ctx, url, token)
	if err != nil {
		h.watchFailed(ctx, repo, withContentsHint(err, token))
		return
	}
	w, err := h.Store.Watch(ctx, repo)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("watching %s: %v", repo, err)
		return
	}
	known, err := h.Store.Heads(ctx, repo)
	if err != nil {
		log.Printf("watching %s: %v", repo, err)
		return
	}
	w.Repo, w.DefaultBranch, w.CheckedAt = repo, def, h.now()
	if !w.Baselined {
		// The first look only records where every branch is, so connecting a repo
		// doesn't run its whole history.
		var errs []error
		for branch, sha := range heads {
			errs = append(errs, h.Store.PutHead(ctx, repo, branch, sha))
		}
		w.Baselined, w.Problem = true, ""
		// The watch goes last: its existing means every head above was recorded.
		if err := errors.Join(append(errs, h.Store.PutWatch(ctx, w))...); err != nil {
			log.Printf("watching %s: %v", repo, err)
			return
		}
		h.watchWorked(repo)
		return
	}
	// The default branch is saved before any run is queued, because a run's
	// secrets depend on it.
	if err := h.Store.PutWatch(ctx, w); err != nil {
		log.Printf("watching %s: %v", repo, err)
		return
	}
	var errs []error
	for _, branch := range slices.Sorted(maps.Keys(heads)) {
		if known[branch] == heads[branch] {
			continue
		}
		sha, err := h.handlePush(ctx, repo, url, branch, def)
		if err != nil {
			// The head stays as it was, so the next look finds this push again.
			errs = append(errs, fmt.Errorf("branch %s: %w", branch, err))
			continue
		}
		errs = append(errs, h.Store.PutHead(ctx, repo, branch, sha))
	}
	for branch := range known {
		if _, ok := heads[branch]; !ok {
			errs = append(errs, h.Store.DeleteHead(ctx, repo, branch))
		}
	}
	if err := errors.Join(errs...); err != nil {
		h.watchFailed(ctx, repo, err)
		return
	}
	w.Problem = ""
	if err := h.Store.PutWatch(ctx, w); err != nil {
		log.Printf("watching %s: %v", repo, err)
		return
	}
	h.watchWorked(repo)
}

// watchFailed shows why a look failed and puts off the next one.
func (h *Host) watchFailed(ctx context.Context, repo string, err error) {
	log.Printf("watching %s for pushes: %v", repo, err)
	w, werr := h.Store.Watch(ctx, repo)
	if werr != nil && !errors.Is(werr, store.ErrNotFound) {
		log.Printf("watching %s: %v", repo, werr)
		return
	}
	w.Repo, w.CheckedAt = repo, h.now()
	w.Problem = strings.ReplaceAll(err.Error(), "\n", "; ")
	if perr := h.Store.PutWatch(ctx, w); perr != nil {
		log.Printf("watching %s: %v", repo, perr)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watchWait == nil {
		h.watchWait = map[string]*retry{}
	}
	b := h.watchWait[repo]
	if b == nil {
		b = &retry{}
		h.watchWait[repo] = b
	}
	wait := h.Config.WatchEvery << min(b.failures, 16)
	b.failures++
	b.until = h.now().Add(min(wait, h.Config.MaxBackoff))
}

// watchWorked ends a repo's backoff.
func (h *Host) watchWorked(repo string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.watchWait, repo)
}

// remoteHeads lists a repo's branches and its default branch, which is empty if
// the remote doesn't say. Names git would accept but Pluto won't use are left out.
func (h *Host) remoteHeads(ctx context.Context, url, token string) (heads map[string]string, def string, err error) {
	out, err := git(ctx, h.StateDir, token, "ls-remote", "--symref", "--", url, "HEAD", "refs/heads/*")
	if err != nil {
		return nil, "", err
	}
	heads, def = parseHeads(string(out))
	return heads, def, nil
}

// parseHeads reads git ls-remote --symref output: "ref: refs/heads/main\tHEAD"
// names the default branch, and "<sha>\trefs/heads/<name>" is a branch.
func parseHeads(out string) (heads map[string]string, def string) {
	heads = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		left, right, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if target, isRef := strings.CutPrefix(left, "ref: "); isRef {
			if branch, ok := strings.CutPrefix(target, "refs/heads/"); ok && right == "HEAD" && branchName.MatchString(branch) {
				def = branch
			}
			continue
		}
		if branch, ok := strings.CutPrefix(right, "refs/heads/"); ok && branchName.MatchString(branch) {
			heads[branch] = left
		}
	}
	return heads, def
}

// handlePush queues what a new commit on a branch asks for, and returns the
// commit the branch was really at when it was fetched, which may be newer than
// the one the look saw.
func (h *Host) handlePush(ctx context.Context, repo, url, branch, def string) (string, error) {
	dir, sha, err := h.fetchRef(ctx, repo, url, branch, pushRef)
	if err != nil {
		return "", err
	}
	out, err := git(ctx, dir, "", "log", "-1", "--format=%an", sha)
	if err != nil {
		return "", err
	}
	actor := strings.TrimSpace(string(out))
	sub := Submission{Ref: branch, SHA: sha, Trigger: store.TriggerPush, Actor: actor}
	cfg, err := readConfig(ctx, dir, sha)
	if err != nil {
		// Say so in the history rather than let the push pass unnoticed.
		_, err = h.record(ctx, repo, Plan{Task: task.Task{Name: configTask}}, sub, store.StateFailed, err.Error())
		return sha, err
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(cfg.Tasks)) {
		t := cfg.Tasks[name]
		if !t.Push {
			continue
		}
		plan := Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}
		if branch != def && strings.HasSuffix(actor, botSuffix) {
			reason := fmt.Sprintf("%s is a bot, and a bot's branch doesn't run in this repo's box, where the default branch's tasks also run", actor)
			_, err := h.record(ctx, repo, plan, sub, store.StateRefused, reason)
			errs = append(errs, err)
			continue
		}
		// A look that failed part-way is repeated, and a host can stop between
		// queueing a run and saving the branch's commit. Neither may run it twice.
		if there, err := h.pushRunExists(ctx, repo, name, branch, sha); err != nil || there {
			errs = append(errs, err)
			continue
		}
		if err := h.replaceQueued(ctx, repo, name, branch); err != nil {
			errs = append(errs, err)
			continue
		}
		_, err := h.Submit(ctx, repo, plan, sub)
		errs = append(errs, err)
	}
	return sha, errors.Join(errs...)
}

// configTask is the task name a push whose .pluto can't be read is recorded under.
const configTask = "config"

// pushRunExists reports whether a commit's run of a task is already queued or
// going for the branch.
func (h *Host) pushRunExists(ctx context.Context, repo, name, branch, sha string) (bool, error) {
	queued, err := h.Store.Queued(ctx)
	if err != nil {
		return false, err
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return false, err
	}
	same := func(r store.Run) bool {
		return r.Kind == store.KindTask && r.Repo == repo && r.Job == name && r.Ref == branch && r.SHA == sha &&
			r.Trigger == store.TriggerPush && (r.State == store.StateQueued || r.Active())
	}
	return slices.ContainsFunc(queued, same) || slices.ContainsFunc(live, same), nil
}

// replaceQueued cancels the runs of a task still waiting for a slot for the
// branch, because a newer push has taken their place. A run that has started
// is left to finish.
func (h *Host) replaceQueued(ctx context.Context, repo, name, branch string) error {
	queued, err := h.Store.Queued(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, q := range queued {
		if q.Kind != store.KindTask || q.Repo != repo || q.Job != name || q.Ref != branch || q.Trigger != store.TriggerPush {
			continue
		}
		_, err := h.Store.MoveQueuedBecause(ctx, q.ID, store.StateCancelled, "a newer push replaced it", h.now())
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// archiveCommit makes a tar of a commit's files from the repo's bare copy, and
// returns its path. If the copy has lost the commit, it's fetched again by its
// ID; a force-push can make it unreachable, and then it's gone for good.
func (h *Host) archiveCommit(ctx context.Context, repo, sha string) (string, error) {
	dir := h.sourceDir(repo)
	if _, err := git(ctx, dir, "", "cat-file", "-e", sha+"^{commit}"); err != nil {
		if err := h.fetchCommit(ctx, repo, h.repoURL(repo), sha); err != nil {
			return "", fmt.Errorf("commit %s is gone from %s, which a force-push can cause, and fetching it again failed: %w", shortSHA(sha), repo, err)
		}
	}
	return h.archiveTree(ctx, dir, sha)
}

// fetchCommit fetches one commit by its ID into the repo's bare copy.
func (h *Host) fetchCommit(ctx context.Context, repo, url, sha string) error {
	unlock := h.lockSource(repo)
	defer unlock()
	dir, err := h.ensureBare(ctx, repo)
	if err != nil {
		return err
	}
	token := h.tokenFor(ctx, repo, url)
	if _, err := git(ctx, dir, token, "fetch", "--quiet", "--no-tags", "--depth=1", "--", url, sha); err != nil {
		return withContentsHint(err, token)
	}
	return nil
}

// archiveTree writes a commit's files as a tar in the uploads directory.
func (h *Host) archiveTree(ctx context.Context, dir, sha string) (string, error) {
	uploads := filepath.Join(h.StateDir, "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(uploads, "source-*.tar")
	if err != nil {
		return "", err
	}
	f.Close()
	if _, err := git(ctx, dir, "", "archive", "--format=tar", "--output="+f.Name(), sha); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
