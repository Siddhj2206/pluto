// Package host is the long-running part of Pluto: it watches GitHub for jobs and
// keeps boxes in line with them, in one reconcile loop.
package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/store"
)

// Config holds the host's limits.
type Config struct {
	MaxJobs       int           // Boxes running or waiting at once.
	KeepFailed    time.Duration // How long a failed box stays enterable.
	MaxKept       int           // Most failed boxes kept at once.
	WaitLimit     time.Duration // How long a box waits for a job.
	IdleLimit     time.Duration // How long a box waits once no job is queued for it.
	PollEvery     time.Duration // How often to ask GitHub for jobs.
	SweepEvery    time.Duration // How often to look for leftover runner registrations.
	RefreshImage  time.Duration // How often to refresh the runner image.
	MaxBackoff    time.Duration // Longest wait between attempts after boxes fail to start.
	KeepRuns      int           // How many finished runs to remember.
	RefreshSource time.Duration // How often to check a repo's branch for schedule changes.
	MaxLogBytes   int64         // A task's log stops growing at this size.
	MinFreeBytes  uint64        // New boxes are refused below this.
	Image         string
}

// DefaultConfig returns the host's standard limits.
func DefaultConfig() Config {
	return Config{
		MaxJobs:       1,
		KeepFailed:    24 * time.Hour,
		MaxKept:       3,
		WaitLimit:     10 * time.Minute,
		IdleLimit:     2 * time.Minute,
		PollEvery:     15 * time.Second,
		SweepEvery:    10 * time.Minute,
		RefreshImage:  24 * time.Hour,
		MaxBackoff:    15 * time.Minute,
		KeepRuns:      1000,
		RefreshSource: 15 * time.Minute,
		MaxLogBytes:   256 << 20,
		MinFreeBytes:  5 << 30,
		Image:         runnerImage,
	}
}

// StuckJob is a job with Pluto's label that this host's boxes can't take.
type StuckJob struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}

// RepoStatus is what the last check of a repo found.
type RepoStatus struct {
	Name    string     `json:"name"`
	Queued  int        `json:"queued"`
	Stuck   []StuckJob `json:"stuck,omitempty"`
	Problem string     `json:"problem,omitempty"`
}

// Host owns boxes and runs.
type Host struct {
	Store  *store.Store
	Engine engine.Engine
	GitHub func(token string) github.API
	Config Config
	Now    func() time.Time
	// FreeBytes reports free disk space where boxes are stored.
	FreeBytes func(ctx context.Context) (uint64, error)
	// StateDir holds task logs, uploaded trees and copies of repos.
	StateDir string
	// StayAwake asks the OS not to sleep for a while. Nil means don't ask.
	StayAwake func(ctx context.Context, d time.Duration)

	mu       sync.Mutex
	status   map[string]RepoStatus
	pulling  bool
	pullErr  string
	lastPull time.Time

	hostID    string
	lastPoll  time.Time
	lastSweep time.Time
	backoff   map[string]*retry

	lastTick  time.Time // Wall clock of the last pass, to notice sleep.
	lastSaved time.Time
	lastAwake time.Time

	preparing  map[string]context.CancelFunc // Runs being set up in the background.
	cancels    map[string]bool               // Runs someone asked to cancel.
	refreshing map[string]bool               // Repos being fetched in the background.
	fetching   map[string]*sync.Mutex        // One git fetch at a time per bare repo.
	prepares   sync.WaitGroup
}

// retry spaces out attempts for a repo whose boxes keep failing to start.
type retry struct {
	failures int
	until    time.Time
}

func (h *Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Labels are the labels this host's runners carry. GitHub adds none to one-time
// runners, so self-hosted must be included.
func Labels() []string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return []string{"self-hosted", "pluto", "linux", arch}
}

// Eligible reports whether a job can land on this host's runners, and whether it
// was meant for Pluto at all.
func Eligible(labels []string) (eligible, ours bool) {
	mine := Labels()
	eligible = true
	for _, l := range labels {
		l = strings.ToLower(l)
		if l == "pluto" {
			ours = true
		}
		if !slices.Contains(mine, l) {
			eligible = false
		}
	}
	return eligible && ours, ours
}

// ensureID loads the host's ID. The API and the loop can both get here first.
func (h *Host) ensureID(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hostID != "" {
		return nil
	}
	id, err := h.Store.HostID(ctx, newID)
	if err != nil {
		return err
	}
	h.hostID = id
	return nil
}

// Tick runs one pass of the reconcile loop.
func (h *Host) Tick(ctx context.Context) error {
	if err := h.ensureID(ctx); err != nil {
		return err
	}
	// Sleep goes first, so a run's time limit moves before anything checks it.
	errs := []error{h.noteGap(ctx), h.reconcile(ctx), h.enforceKept(ctx), h.collectOrphans(ctx), h.tickTasks(ctx)}
	if h.now().Sub(h.lastPoll) >= h.Config.PollEvery {
		h.lastPoll = h.now()
		errs = append(errs, h.poll(ctx))
	}
	if h.now().Sub(h.lastSweep) >= h.Config.SweepEvery {
		h.lastSweep = h.now()
		errs = append(errs, h.sweepRunners(ctx), h.Store.Prune(ctx, h.Config.KeepRuns), h.sweepFiles(ctx))
	}
	return errors.Join(errs...)
}

// Run ticks until ctx ends.
func (h *Host) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		if err := h.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("tick: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Host) reconcile(ctx context.Context) error {
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range live {
		if err := h.reconcileRun(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (h *Host) reconcileRun(ctx context.Context, r store.Run) error {
	if r.Kind == store.KindTask {
		return h.reconcileTask(ctx, r)
	}
	running, err := h.Engine.Running(ctx, r.Box)
	if errors.Is(err, engine.ErrNotFound) {
		if r.Active() {
			return h.finish(ctx, r, store.StateLost, "the box disappeared", false)
		}
		r.BoxKept = false
		r.KeepUntil = time.Time{}
		return h.Store.PutRun(ctx, r)
	}
	if err != nil {
		return err
	}
	if !r.Active() {
		return nil // A kept box may be stopped: pluto enter restarts it.
	}
	if !running {
		// The machine probably restarted. The job may have finished first, and
		// the box's files still say how it went. Its entry script won't run the
		// runner again.
		if err := h.Engine.Resume(ctx, r.Box); err != nil {
			return h.finish(ctx, r, store.StateLost, "the box stopped during the run and wouldn't restart", false)
		}
	}
	out, err := h.Engine.Exec(ctx, r.Box, "sh", hooksDir+"/status.sh")
	if err != nil {
		if !running {
			return nil // A restarted box may not be ready yet. Check again next tick.
		}
		return err
	}
	s := parseStatus(out)
	wasWaiting := r.State == store.StateWaiting
	r = absorb(r, s, h.now())
	switch {
	case s.Exited && s.Refused:
		return h.finish(ctx, r, store.StateRefused, "the job might run code from another repository", false)
	case s.Exited && s.Result != "":
		return h.finishExited(ctx, r, s)
	case s.Exited && s.ExitCode == interruptedCode:
		return h.finish(ctx, r, store.StateLost, "the machine restarted during the run", false)
	case s.Exited:
		return h.finishExited(ctx, r, s)
	case !running:
		return h.finish(ctx, r, store.StateLost, "the box stopped during the run, usually because the machine restarted", false)
	case !s.jobStarted() && h.idleTooLong(r):
		return h.expireIdle(ctx, r)
	}
	if wasWaiting && s.jobStarted() {
		delete(h.backoff, r.Repo)
		return h.Store.PutRun(ctx, r)
	}
	return nil
}

// idleTooLong reports whether a waiting box should give up: at its time limit,
// or sooner when the last check found nothing queued for it, such as a cancelled job.
func (h *Host) idleTooLong(r store.Run) bool {
	age := h.now().Sub(r.CreatedAt)
	if age > h.Config.WaitLimit {
		return true
	}
	h.mu.Lock()
	st, known := h.status[r.Repo]
	h.mu.Unlock()
	return known && st.Problem == "" && st.Queued == 0 && age > h.Config.IdleLimit
}

// expireIdle removes a waiting box, unless GitHub has just given it a job. The
// box only learns that seconds later, when its pre-job hook runs.
func (h *Host) expireIdle(ctx context.Context, r store.Run) error {
	repo, err := h.Store.Repo(ctx, r.Repo)
	if err != nil {
		return err
	}
	busy, err := h.GitHub(repo.Token).RunnerBusy(ctx, r.Repo, r.RunnerID)
	if err != nil {
		return err // Can't tell. Try again next tick.
	}
	if busy {
		return nil
	}
	return h.finish(ctx, r, store.StateExpired, "no job arrived", false)
}

// absorb fills in a run from its box's own account of the job.
func absorb(r store.Run, s boxStatus, now time.Time) store.Run {
	if !s.jobStarted() {
		return r
	}
	r.GitHubRunID, r.Workflow, r.Job, r.Event = s.GitHubRunID, s.Workflow, s.Job, s.Event
	r.Ref, r.SHA, r.Workspace = s.Ref, s.SHA, s.Workspace
	r.State = store.StateRunning
	if r.StartedAt.IsZero() {
		r.StartedAt = now
	}
	return r
}

func (h *Host) finishExited(ctx context.Context, r store.Run, s boxStatus) error {
	switch s.Result {
	case "Succeeded", "SucceededWithIssues":
		return h.finish(ctx, r, store.StatePassed, "", false)
	case "Canceled", "Cancelled", "Abandoned":
		return h.finish(ctx, r, store.StateCancelled, "", false)
	case "Failed":
		return h.finish(ctx, r, store.StateFailed, "", true)
	}
	if !s.jobStarted() {
		h.noteFailure(r.Repo)
		return h.finish(ctx, r, store.StateFailed, fmt.Sprintf("the runner exited with code %d before taking a job", s.ExitCode), false)
	}
	return h.finish(ctx, r, store.StateFailed, fmt.Sprintf("the runner exited with code %d and no job result", s.ExitCode), true)
}

// finish records an outcome, and keeps or removes the box.
func (h *Host) finish(ctx context.Context, r store.Run, state, reason string, keep bool) error {
	r.State, r.Reason, r.FinishedAt = state, reason, h.now()
	if keep {
		r.BoxKept, r.KeepUntil = true, h.now().Add(h.Config.KeepFailed)
		return h.Store.PutRun(ctx, r)
	}
	r.BoxKept, r.KeepUntil = false, time.Time{}
	if err := h.Store.PutRun(ctx, r); err != nil {
		return err
	}
	err := h.Engine.Remove(ctx, r.Box)
	if state == store.StateExpired || state == store.StateLost || r.GitHubRunID == 0 {
		h.deleteRunner(ctx, r)
	}
	return err
}

// deleteRunner is best effort. sweepRunners catches anything it misses.
func (h *Host) deleteRunner(ctx context.Context, r store.Run) {
	repo, err := h.Store.Repo(ctx, r.Repo)
	if err != nil {
		return
	}
	if err := h.GitHub(repo.Token).DeleteRunner(ctx, r.Repo, r.RunnerID); err != nil {
		log.Printf("run %s: removing its runner registration: %v", r.ID, err)
	}
}

// sweepRunners deletes this host's registrations that no live box owns, such as
// one left by a crash or by a failed delete. Other hosts' runners aren't touched.
func (h *Host) sweepRunners(ctx context.Context) error {
	repos, err := h.Store.Repos(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, repo := range repos {
		gh := h.GitHub(repo.Token)
		runners, err := gh.Runners(ctx, repo.Name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		live, err := h.Store.Live(ctx) // Read after listing, so a new runner always counts as live.
		if err != nil {
			return err
		}
		for _, r := range runners {
			if r.Busy || !strings.HasPrefix(r.Name, h.boxPrefix()) {
				continue
			}
			if !slices.ContainsFunc(live, func(run store.Run) bool { return run.Box == r.Name }) {
				log.Printf("removing leftover runner registration %s", r.Name)
				errs = append(errs, gh.DeleteRunner(ctx, repo.Name, r.ID))
			}
		}
	}
	return errors.Join(errs...)
}

func (h *Host) boxPrefix() string { return "pluto-" + h.hostID + "-" }

// startPull downloads or refreshes the image in the background, so a slow
// download doesn't stop the loop.
func (h *Host) startPull(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pulling {
		return
	}
	h.pulling = true
	go func() {
		err := h.Engine.Pull(ctx, h.Config.Image)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.pulling = false
		h.lastPull = h.now()
		h.pullErr = ""
		if err != nil {
			h.pullErr = err.Error()
			log.Printf("pulling %s: %v", h.Config.Image, err)
		}
	}()
}

// imageReady reports whether boxes can start now, starting a pull if needed. A
// failed refresh doesn't matter while the older image is still present.
func (h *Host) imageReady(ctx context.Context) (bool, error) {
	has, err := h.Engine.HasImage(ctx, h.Config.Image)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	due := h.now().Sub(h.lastPull) >= h.Config.RefreshImage
	pullErr := h.pullErr
	h.mu.Unlock()
	if due {
		h.startPull(ctx)
	}
	if !has && pullErr != "" {
		return false, fmt.Errorf("couldn't download the runner image: %s", pullErr)
	}
	return has, nil
}

// enforceKept removes failed boxes past their time, and the oldest beyond the limit.
func (h *Host) enforceKept(ctx context.Context) error {
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	var kept []store.Run
	for _, r := range live {
		if r.BoxKept {
			kept = append(kept, r)
		}
	}
	var errs []error
	for i, r := range kept {
		if h.now().After(r.KeepUntil) || len(kept)-i > h.Config.MaxKept {
			errs = append(errs, h.release(ctx, r))
		}
	}
	return errors.Join(errs...)
}

func (h *Host) release(ctx context.Context, r store.Run) error {
	if err := h.Engine.Remove(ctx, r.Box); err != nil {
		return err
	}
	r.BoxKept, r.KeepUntil = false, time.Time{}
	return h.Store.PutRun(ctx, r)
}

// collectOrphans removes Pluto's containers with no record, such as a box started
// just before a crash.
func (h *Host) collectOrphans(ctx context.Context) error {
	names, err := h.Engine.List(ctx)
	if err != nil {
		return err
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		if !slices.ContainsFunc(live, func(r store.Run) bool { return r.Box == name }) {
			log.Printf("removing box %s, which has no record", name)
			errs = append(errs, h.Engine.Remove(ctx, name))
		}
	}
	return errors.Join(errs...)
}

// poll asks GitHub for waiting jobs, and starts boxes for those that fit.
func (h *Host) poll(ctx context.Context) error {
	repos, err := h.Store.Repos(ctx)
	if err != nil {
		return err
	}
	statuses := map[string]RepoStatus{}
	var errs []error
	for _, repo := range repos {
		st, err := h.pollRepo(ctx, repo)
		if err != nil {
			st.Problem = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", repo.Name, err))
		}
		st.Name = repo.Name
		statuses[repo.Name] = st
	}
	h.mu.Lock()
	h.status = statuses
	h.mu.Unlock()
	return errors.Join(errs...)
}

func (h *Host) pollRepo(ctx context.Context, repo store.Repo) (RepoStatus, error) {
	var st RepoStatus
	gh := h.GitHub(repo.Token)
	jobs, err := gh.QueuedJobs(ctx, repo.Name)
	if err != nil {
		return st, err
	}
	for _, j := range jobs {
		eligible, ours := Eligible(j.Labels)
		switch {
		case eligible:
			st.Queued++
		case ours:
			st.Stuck = append(st.Stuck, StuckJob{Name: j.Name, Labels: j.Labels, URL: j.URL})
		}
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return st, err
	}
	active, waiting := 0, 0
	for _, r := range live {
		if r.Active() {
			active++
			if r.Kind == store.KindJob && r.State == store.StateWaiting && r.Repo == repo.Name {
				waiting++
			}
		}
	}
	need := min(st.Queued-waiting, h.Config.MaxJobs-active)
	if need <= 0 {
		return st, nil
	}
	if b := h.backoff[repo.Name]; b != nil && h.now().Before(b.until) {
		st.Problem = fmt.Sprintf("boxes keep failing to start, so Pluto will try again in %s", b.until.Sub(h.now()).Round(time.Second))
		return st, nil
	}
	if h.FreeBytes != nil {
		free, err := h.FreeBytes(ctx)
		if err == nil && free < h.Config.MinFreeBytes {
			return st, fmt.Errorf("only %d GiB of disk is free, and a new box needs at least %d GiB", free>>30, h.Config.MinFreeBytes>>30)
		}
	}
	ready, err := h.imageReady(ctx)
	if err != nil {
		return st, err
	}
	if !ready {
		st.Problem = "downloading the runner image; jobs will start when it's ready"
		return st, nil
	}
	for range need {
		if err := h.startBox(ctx, repo.Name, gh); err != nil {
			h.noteFailure(repo.Name)
			return st, err
		}
	}
	return st, nil
}

// noteFailure pushes back the next attempt for a repo, doubling each time.
func (h *Host) noteFailure(repo string) {
	if h.backoff == nil {
		h.backoff = map[string]*retry{}
	}
	b := h.backoff[repo]
	if b == nil {
		b = &retry{}
		h.backoff[repo] = b
	}
	wait := h.Config.PollEvery << min(b.failures, 16)
	b.failures++
	b.until = h.now().Add(min(wait, h.Config.MaxBackoff))
}

func newID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (h *Host) startBox(ctx context.Context, repo string, gh github.API) error {
	id := newID()
	box := h.boxPrefix() + id
	runnerID, jit, err := gh.CreateRunner(ctx, repo, box, Labels())
	if err != nil {
		return err
	}
	r := store.Run{ID: "r_" + id, Repo: repo, Box: box, RunnerID: runnerID, State: store.StateWaiting, CreatedAt: h.now()}
	if err := h.Store.PutRun(ctx, r); err != nil {
		_ = gh.DeleteRunner(ctx, repo, runnerID)
		return err
	}
	err = h.Engine.Start(ctx, engine.Spec{
		Name:  box,
		Image: h.Config.Image,
		Env: map[string]string{
			"PLUTO_JIT":                       jit,
			"ACTIONS_RUNNER_HOOK_JOB_STARTED": hooksDir + "/pre-job.sh",
		},
		Cmd:     []string{"sh", "-c", entryScript},
		Files:   map[string][]byte{"pre-job.sh": []byte(preJobHook), "status.sh": []byte(statusScript)},
		CopyDir: hooksDir,
	})
	if err != nil {
		_ = gh.DeleteRunner(ctx, repo, runnerID)
		r.State, r.Reason, r.FinishedAt = store.StateFailed, "the box didn't start: "+err.Error(), h.now()
		return errors.Join(err, h.Store.PutRun(ctx, r))
	}
	return nil
}

// Status returns what the last check of each repo found.
func (h *Host) Status(name string) RepoStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.status[name]
	st.Name = name
	return st
}
