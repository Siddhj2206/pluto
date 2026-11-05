package host

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

const (
	workspaceDir = "/workspace"
	homeDir      = "/root" // Task boxes run as root, which rootless engines map to the user.
	// logChunk is how much log a single read takes from a box.
	logChunk = 1 << 20
	// startGrace is how long a run's script gets to record itself after starting.
	startGrace = time.Minute
	// lateAfter is when a scheduled run stops counting as on time.
	lateAfter = 2 * time.Minute
	// gapAfter is how long the loop can pause before Pluto reports it. The loop
	// ticks every few seconds, so a gap this long means sleep or a stopped host.
	gapAfter = time.Minute
	// maxSlots stops the count of missed slots after a very long absence.
	maxSlots = 10000
)

// Plan is what a task run will do. It's stored with the run, so a run queued
// before a host restart still knows its work.
type Plan struct {
	Task     task.Task `json:"task"`
	Image    string    `json:"image,omitempty"`
	Requires []string  `json:"requires,omitempty"`
}

func (p Plan) privileged() bool { return slices.Contains(p.Requires, task.Privileged) }

func (p Plan) image(fallback string) string {
	if p.Image != "" {
		return p.Image
	}
	return fallback
}

func slug(s string) string {
	sum := sha256.Sum256([]byte(s))
	clean := regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(clean, "-") + "-" + hex.EncodeToString(sum[:3])
}

// boxFor decides which durable box a task's runs use. Everything else follows
// its answer, so this is the one place to change for another policy, such as a
// box per task (ADR 0008).
func (h *Host) boxFor(repo string, _ task.Task) string {
	return "pluto-" + h.hostID + "-box-" + slug(repo)
}

func (h *Host) logPath(id string) string {
	return filepath.Join(h.StateDir, "logs", id+".log")
}

func (h *Host) manifestPath(box string) string {
	return filepath.Join(h.StateDir, "manifests", box)
}

func (h *Host) uploadPath(id string) string {
	return filepath.Join(h.StateDir, "uploads", id+".tar")
}

func runDir(id string) string { return "/var/tmp/pluto-" + id }

// Submit queues a task run.
func (h *Host) Submit(ctx context.Context, repo string, plan Plan, ref, sha, trigger, upload, note string) (store.Run, error) {
	spec, err := json.Marshal(plan)
	if err != nil {
		return store.Run{}, err
	}
	if err := h.ensureID(ctx); err != nil {
		return store.Run{}, err
	}
	r := store.Run{
		ID: "r_" + newID(), Repo: repo, Box: h.boxFor(repo, plan.Task), State: store.StateQueued,
		Kind: store.KindTask, Job: plan.Task.Name, Ref: ref, SHA: sha, Trigger: trigger, Note: note,
		Spec: string(spec), Upload: upload, Workspace: workspaceDir, CreatedAt: h.now(),
	}
	return r, h.Store.PutRun(ctx, r)
}

// tickTasks does the task side of one pass of the loop.
func (h *Host) tickTasks(ctx context.Context) error {
	errs := []error{h.fireSchedules(ctx), h.startQueued(ctx)}
	h.refreshSources(ctx)
	h.keepAwake(ctx)
	return errors.Join(errs...)
}

// noteGap notices when the loop didn't run for a while. It compares wall clocks,
// because the monotonic clock can stop during suspend. Machine sleep and a
// stopped host look the same from here, so the note says either.
func (h *Host) noteGap(ctx context.Context) error {
	current := h.now()
	now := current.Round(0)
	restored := false
	if h.lastTick.IsZero() {
		saved, err := h.Store.Meta(ctx, "last_tick")
		if err != nil {
			return err
		}
		if v, err := strconv.ParseInt(saved, 10, 64); err == nil {
			h.lastTick = time.Unix(v, 0)
			restored = true
		} else {
			h.lastTick = now
		}
	}
	from := h.lastTick
	gap := now.Sub(from.Round(0))
	// Within one run of the host, the monotonic clock pauses during suspend and
	// the wall clock doesn't. Their difference is time asleep, so a slow engine
	// that held up the loop isn't mistaken for it.
	if !restored && h.Now == nil {
		gap -= current.Sub(from)
	}
	if gap < gapAfter {
		if now.Sub(h.lastSaved) >= 30*time.Second {
			h.lastSaved = now
			if err := h.Store.SetMeta(ctx, "last_tick", strconv.FormatInt(now.Unix(), 10)); err != nil {
				return err
			}
		}
		h.lastTick = current
		return nil
	}
	h.lastTick, h.lastSaved = current, now
	if err := h.Store.SetMeta(ctx, "last_tick", strconv.FormatInt(now.Unix(), 10)); err != nil {
		return err
	}
	log.Printf("the loop didn't run from %s to %s", from.Format(time.DateTime), now.Format(time.DateTime))
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range live {
		if !r.Active() || h.isPreparing(r.ID) {
			continue
		}
		r.Note = joinNote(r.Note, fmt.Sprintf("machine asleep or host stopped from %s to %s", from.Format("Jan 2 15:04"), now.Format("Jan 2 15:04")))
		if !r.Deadline.IsZero() {
			r.Deadline = r.Deadline.Add(gap) // Time asleep doesn't count against the limit.
		}
		errs = append(errs, h.Store.PutRun(ctx, r))
	}
	return errors.Join(errs...)
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

// fireSchedules queues every task whose time has come. All the slots missed
// while the machine was asleep or the host was stopped collapse into one run.
func (h *Host) fireSchedules(ctx context.Context) error {
	schedules, err := h.Store.Schedules(ctx)
	if err != nil {
		return err
	}
	if len(schedules) == 0 {
		return nil
	}
	sources, err := h.Store.Sources(ctx)
	if err != nil {
		return err
	}
	now := h.now()
	var errs []error
	for _, sc := range schedules {
		parsed, err := task.ParseSchedule(sc.Expr)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", sc.Repo, sc.Task, err))
			continue
		}
		slot := parsed.Next(sc.CheckedAt)
		if slot.After(now) {
			continue
		}
		missed := 1
		for next := parsed.Next(slot); !next.After(now) && missed < maxSlots; next = parsed.Next(slot) {
			slot = next
			missed++
		}
		i := slices.IndexFunc(sources, func(s store.Source) bool { return s.Repo == sc.Repo })
		if i < 0 {
			errs = append(errs, h.Store.DeleteSchedule(ctx, sc.Repo, sc.Task))
			continue
		}
		if err := h.fire(ctx, sc, sources[i], slot, missed); err != nil {
			errs = append(errs, err)
			continue
		}
		sc.CheckedAt = now
		errs = append(errs, h.Store.PutSchedule(ctx, sc))
	}
	return errors.Join(errs...)
}

func (h *Host) fire(ctx context.Context, sc store.Schedule, src store.Source, slot time.Time, missed int) error {
	var cfg task.Config
	if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
		return fmt.Errorf("%s: reading its saved config: %w", sc.Repo, err)
	}
	t, ok := cfg.Tasks[sc.Task]
	if !ok {
		return h.Store.DeleteSchedule(ctx, sc.Repo, sc.Task)
	}
	plan := Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}
	active, err := h.taskActive(ctx, sc.Repo, sc.Task)
	if err != nil {
		return err
	}
	if active {
		r, err := h.Submit(ctx, sc.Repo, plan, src.Branch, "", store.TriggerSchedule, "", "")
		if err != nil {
			return err
		}
		r.State, r.Reason, r.FinishedAt = store.StateSkipped, fmt.Sprintf("the %s slot passed while the previous run was still going", slot.Format("Jan 2 15:04")), h.now()
		return h.Store.PutRun(ctx, r)
	}
	note := ""
	if h.now().Sub(slot) > lateAfter || missed > 1 {
		note = fmt.Sprintf("was due %s, and the machine was asleep or the host stopped", slot.Format("Jan 2 15:04"))
		if missed > 1 {
			note += fmt.Sprintf("; %d missed slots became this one run", missed)
		}
	}
	_, err = h.Submit(ctx, sc.Repo, plan, src.Branch, "", store.TriggerSchedule, "", note)
	return err
}

func (h *Host) taskActive(ctx context.Context, repo, name string) (bool, error) {
	queued, err := h.Store.Queued(ctx)
	if err != nil {
		return false, err
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return false, err
	}
	same := func(r store.Run) bool {
		return r.Kind == store.KindTask && r.Repo == repo && r.Job == name && (r.Active() || r.State == store.StateQueued)
	}
	return slices.ContainsFunc(queued, same) || slices.ContainsFunc(live, same), nil
}

// startQueued gives free slots to the oldest queued runs. A durable box runs one
// thing at a time, whatever the host's slot count.
func (h *Host) startQueued(ctx context.Context) error {
	queued, err := h.Store.Queued(ctx)
	if err != nil || len(queued) == 0 {
		return err
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	active := 0
	busy := map[string]bool{}
	for _, r := range live {
		if r.Active() {
			active++
			busy[r.Box] = true
		}
	}
	for _, r := range queued {
		if active >= h.Config.MaxJobs {
			return nil
		}
		if busy[r.Box] {
			continue
		}
		moved, err := h.Store.MoveQueued(ctx, r.ID, store.StateWaiting, h.now())
		if err != nil {
			return err
		}
		if !moved {
			continue // Cancelled since the list was read.
		}
		r.State = store.StateWaiting
		active++
		busy[r.Box] = true
		h.startPrepare(ctx, r)
	}
	return nil
}

func (h *Host) isPreparing(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.preparing[id]
	return ok
}

// startPrepare gets a run going in the background, because downloading an image
// or copying a repo can take far longer than a tick.
func (h *Host) startPrepare(ctx context.Context, r store.Run) {
	runCtx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	if h.preparing == nil {
		h.preparing = map[string]context.CancelFunc{}
	}
	h.preparing[r.ID] = cancel
	h.mu.Unlock()
	h.prepares.Add(1)
	go func() {
		defer h.prepares.Done()
		defer cancel()
		err := h.prepare(runCtx, r)
		// A cancelled context can mean the user cancelled, or the host is stopping
		// and the next start picks the run up again.
		if err != nil && ctx.Err() == nil {
			if h.takeCancel(r.ID) {
				err = h.endPrepare(r, store.StateCancelled, "")
			} else {
				log.Printf("run %s: %v", r.ID, err)
				err = h.endPrepare(r, store.StateFailed, err.Error())
			}
			if err != nil {
				log.Printf("run %s: recording its result: %v", r.ID, err)
			}
		}
		h.mu.Lock()
		delete(h.preparing, r.ID)
		h.mu.Unlock()
	}()
}

func (h *Host) endPrepare(r store.Run, state, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	current, err := h.Store.Run(ctx, r.ID)
	if err != nil {
		return err
	}
	return h.finishTask(ctx, current, state, reason)
}

func (h *Host) takeCancel(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.cancels[id]
	delete(h.cancels, id)
	return was
}

// prepare takes a run from a free slot to a started script.
func (h *Host) prepare(ctx context.Context, r store.Run) error {
	var plan Plan
	if err := json.Unmarshal([]byte(r.Spec), &plan); err != nil {
		return fmt.Errorf("reading the run's plan: %w", err)
	}
	tree := r.Upload
	if r.Trigger == store.TriggerSchedule || r.Trigger == store.TriggerBranch {
		var note string
		var err error
		plan, tree, r.SHA, note, err = h.checkoutSource(ctx, r.Repo, plan.Task.Name)
		if err != nil {
			return err
		}
		r.Note = joinNote(r.Note, note)
		spec, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		r.Spec = string(spec)
		r.Box = h.boxFor(r.Repo, plan.Task)
		if err := h.Store.PutRun(ctx, r); err != nil {
			return err
		}
		defer os.Remove(tree)
	}
	env, err := h.checkAllowed(ctx, r, plan)
	if err != nil {
		return err
	}
	if h.FreeBytes != nil {
		free, err := h.FreeBytes(ctx)
		if err == nil && free < h.Config.MinFreeBytes {
			return fmt.Errorf("only %d GiB of disk is free, and a run needs at least %d GiB", free>>30, h.Config.MinFreeBytes>>30)
		}
	}
	if err := h.ensureBox(ctx, r, plan); err != nil {
		return err
	}
	dir := runDir(r.ID)
	// A host that stopped just after starting the script finds it here. Starting
	// it again would run the work twice, and copying would nest the files.
	out, err := h.Engine.Exec(ctx, r.Box, "sh", "-c", `if [ -f `+dir+`/pid ]; then echo started; else rm -rf `+dir+`; fi`)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "started" {
		return h.markRunning(ctx, r, plan)
	}
	files := map[string][]byte{"run.sh": []byte(runScript(dir, len(plan.Task.Run)))}
	for i, step := range plan.Task.Run {
		files[stepFile(i)] = []byte(step + "\n")
	}
	if tree != "" {
		removed, names, err := h.staleFiles(r.Box, tree)
		if err != nil {
			return err
		}
		files["removed"] = []byte(strings.Join(removed, "\x00"))
		if err := h.Engine.CopyFiles(ctx, r.Box, dir, files); err != nil {
			return err
		}
		if len(removed) > 0 {
			if _, err := h.Engine.Exec(ctx, r.Box, "sh", "-c", `cd `+workspaceDir+` && xargs -0 rm -f -- < `+dir+`/removed`); err != nil {
				return fmt.Errorf("removing files deleted from the repo: %w", err)
			}
			// A directory the repo no longer has could block a file of the same name.
			// rmdir leaves any that still hold something, such as ignored files.
			argv := append([]string{"sh", "-c", `cd ` + workspaceDir + ` && for d; do rmdir -- "$d" 2>/dev/null; done; true`, "sh"}, emptiedDirs(removed)...)
			if _, err := h.Engine.Exec(ctx, r.Box, argv...); err != nil {
				return fmt.Errorf("removing directories deleted from the repo: %w", err)
			}
		}
		f, err := os.Open(tree)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := h.Engine.CopyTar(ctx, r.Box, workspaceDir, f); err != nil {
			return err
		}
		if err := h.saveManifest(r.Box, names); err != nil {
			return err
		}
	} else if err := h.Engine.CopyFiles(ctx, r.Box, dir, files); err != nil {
		return err
	}
	env["PLUTO_RUN"], env["PLUTO_TASK"], env["PLUTO_REF"], env["PLUTO_SHA"] = r.ID, plan.Task.Name, r.Ref, r.SHA
	// Stop now rather than start a run nobody wants. The script goes next, and
	// after that a cancel is the loop's to handle.
	if h.takeCancel(r.ID) {
		return h.endPrepare(r, store.StateCancelled, "") // Its context may already be cancelled.
	}
	if err := h.Engine.ExecDetached(ctx, r.Box, env, "sh", dir+"/run.sh"); err != nil {
		return err
	}
	return h.markRunning(ctx, r, plan)
}

// markRunning records a started script. The upload goes last, because until now a
// restart could still need it.
func (h *Host) markRunning(ctx context.Context, r store.Run, plan Plan) error {
	upload := r.Upload
	r.State, r.StartedAt, r.Deadline, r.Upload = store.StateRunning, h.now(), h.now().Add(plan.Task.Timeout), ""
	if err := h.Store.PutRun(ctx, r); err != nil {
		return err
	}
	if upload != "" {
		os.Remove(upload)
	}
	return nil
}

// checkAllowed applies the host's side of privilege and secrets, and returns the
// environment the run gets. A repo's config can only ask (ADR 0007).
func (h *Host) checkAllowed(ctx context.Context, r store.Run, plan Plan) (map[string]string, error) {
	if plan.privileged() {
		granted, err := h.Store.Granted(ctx, r.Repo, task.Privileged)
		if err != nil {
			return nil, err
		}
		if !granted {
			return nil, fmt.Errorf("this repo asks for privileged boxes, and this host hasn't allowed it. In the repo, run: pluto box grant --privileged")
		}
		rootless, err := h.Engine.Rootless(ctx)
		if err != nil {
			return nil, err
		}
		if !rootless {
			return nil, errors.New("privileged boxes need a rootless engine, because on a rootful one they amount to root on the host")
		}
	}
	values, err := h.Store.Secrets(ctx, r.Repo)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, name := range plan.Task.Secrets {
		value, ok := values[name]
		if !ok {
			return nil, fmt.Errorf("this task needs the secret %s, which isn't set. In the repo, run: pluto secret set %s", name, name)
		}
		env[name] = value
	}
	return env, nil
}

// ensureBox gets the repo's durable box running. Changing its image or privilege
// replaces the container and keeps the volumes (ADR 0008).
func (h *Host) ensureBox(ctx context.Context, r store.Run, plan Plan) error {
	image := plan.image(h.Config.Image)
	box, err := h.Store.BoxByName(ctx, r.Box)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if !exists {
		box = store.Box{Name: r.Box, Repo: r.Repo, WorkspaceVolume: r.Box + "-workspace", HomeVolume: r.Box + "-home", CreatedAt: h.now()}
	}
	running := false
	if exists {
		running, err = h.Engine.Running(ctx, box.Name)
		if errors.Is(err, engine.ErrNotFound) {
			exists = false
		} else if err != nil {
			return err
		}
	}
	if exists && box.Image == image && box.Privileged == plan.privileged() {
		if running {
			return nil
		}
		return h.Engine.Resume(ctx, box.Name)
	}
	if exists {
		if err := h.Engine.Remove(ctx, box.Name); err != nil {
			return err
		}
	}
	// A new container is the one moment the image may change, so refresh it. A
	// failed refresh is fine if an older copy is still here.
	pullErr := h.Engine.Pull(ctx, image)
	if pullErr != nil {
		has, err := h.Engine.HasImage(ctx, image)
		if err != nil {
			return err
		}
		if !has {
			return fmt.Errorf("couldn't download %s: %w", image, pullErr)
		}
		log.Printf("refreshing %s: %v", image, pullErr)
	}
	box.Image, box.Privileged = image, plan.privileged()
	if err := h.Store.PutBox(ctx, box); err != nil {
		return err
	}
	return h.Engine.Start(ctx, engine.Spec{
		Name:       box.Name,
		Image:      image,
		Durable:    true,
		Privileged: box.Privileged,
		Init:       true,
		Entrypoint: "sleep",
		Cmd:        []string{"infinity"},
		User:       "0",
		Workdir:    workspaceDir,
		Volumes:    map[string]string{box.WorkspaceVolume: workspaceDir, box.HomeVolume: homeDir},
	})
}

func stepFile(i int) string { return fmt.Sprintf("step-%d.sh", i) }

// runScript runs a task's steps in order and stops at the first failure. Writing
// exit last is what marks a result as complete.
func runScript(dir string, steps int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\nd=%s\necho $$ > \"$d/pid\"\ncd %s || exit 1\n", dir, workspaceDir)
	b.WriteString("if command -v bash >/dev/null 2>&1; then shell=bash; else shell=sh; fi\n(\n")
	for i := range steps {
		fmt.Fprintf(&b, "  printf '$ '; cat \"$d/%[1]s\"\n  \"$shell\" -e \"$d/%[1]s\" || exit $?\n", stepFile(i))
	}
	b.WriteString(") > \"$d/log\" 2>&1 < /dev/null\n")
	b.WriteString("code=$?\necho $code > \"$d/exit.tmp\"\nmv \"$d/exit.tmp\" \"$d/exit\"\n")
	return b.String()
}

// taskStatusScript says how a run's script is doing, and always reads exit first: a
// script that has just ended must not look like one that died.
func taskStatusScript(dir string) string {
	return fmt.Sprintf(`d=%s
if [ -f "$d/exit" ]; then echo "exit $(cat "$d/exit")"
elif [ ! -f "$d/pid" ]; then echo starting
elif kill -0 "$(cat "$d/pid")" 2>/dev/null; then echo alive
elif [ -f "$d/exit" ]; then echo "exit $(cat "$d/exit")"
else echo dead
fi`, dir)
}

func (h *Host) reconcileTask(ctx context.Context, r store.Run) error {
	if h.isPreparing(r.ID) {
		return nil
	}
	// The pass's list may be old: a prepare can finish and clear itself after it
	// was read. A prepare records its result before clearing, so this is current.
	r, err := h.Store.Run(ctx, r.ID)
	if err != nil {
		return err
	}
	if !r.Active() {
		return nil
	}
	if r.State == store.StateWaiting {
		h.startPrepare(ctx, r) // The host stopped before it finished.
		return nil
	}
	running, err := h.Engine.Running(ctx, r.Box)
	if errors.Is(err, engine.ErrNotFound) {
		return h.finishTask(ctx, r, store.StateLost, "the box disappeared")
	}
	if err != nil {
		return err
	}
	if !running {
		// The machine probably restarted. Its files still say how the run went.
		if err := h.Engine.Resume(ctx, r.Box); err != nil {
			return h.finishTask(ctx, r, store.StateLost, "the box stopped during the run and wouldn't restart")
		}
	}
	dir := runDir(r.ID)
	out, err := h.Engine.Exec(ctx, r.Box, "sh", "-c", taskStatusScript(dir))
	if err != nil {
		if !running {
			return nil // A restarted box may not be ready yet. Check again next tick.
		}
		// A box that can't answer, such as one whose step deleted sh, can still
		// be cancelled and still has a time limit.
		if h.takeCancel(r.ID) {
			return h.finishTask(ctx, r, store.StateCancelled, "")
		}
		if !r.Deadline.IsZero() && h.now().After(r.Deadline) {
			return h.finishTask(ctx, r, store.StateFailed, "it reached its time limit")
		}
		return err
	}
	status := strings.TrimSpace(string(out))
	if err := h.drainLog(ctx, r, false); err != nil {
		log.Printf("run %s: reading its log: %v", r.ID, err)
	}
	if code, ok := strings.CutPrefix(status, "exit "); ok {
		if code == "0" {
			return h.finishTask(ctx, r, store.StatePassed, "")
		}
		return h.finishTask(ctx, r, store.StateFailed, "a step exited with code "+code)
	}
	switch {
	case !running:
		// A restart kills everything in a box. Any exec left is gone.
		return h.finishTask(ctx, r, store.StateLost, "the machine or box restarted during the run")
	case status == "dead":
		return h.finishTask(ctx, r, store.StateLost, "the run's process ended without reporting a result")
	case status == "starting" && h.now().Sub(r.StartedAt) > startGrace:
		return h.finishTask(ctx, r, store.StateLost, "the run's script never started")
	case h.takeCancel(r.ID):
		return h.finishTask(ctx, r, store.StateCancelled, "")
	case !r.Deadline.IsZero() && h.now().After(r.Deadline):
		return h.finishTask(ctx, r, store.StateFailed, "it reached its time limit")
	}
	return nil
}

// drainLog copies new log output from the box to the host, which keeps logs safe
// from a box reset. Its offset is the host file's size, so a restart resumes it
// without losing or repeating anything.
func (h *Host) drainLog(ctx context.Context, r store.Run, final bool) error {
	path := h.logPath(r.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	reads := 16
	if final {
		reads = 1 << 12
	}
	for range reads {
		if size >= h.Config.MaxLogBytes {
			return nil
		}
		script := fmt.Sprintf(`[ -f %[1]s/log ] && tail -c +%[2]d %[1]s/log | head -c %[3]d`, runDir(r.ID), size+1, logChunk)
		data, err := h.Engine.Exec(ctx, r.Box, "sh", "-c", script)
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		size += int64(len(data))
		if len(data) < logChunk {
			return nil
		}
	}
	return nil
}

// finishTask records a result and stops the box, which ends anything still
// running in it. A durable box stays, ready for its next run.
func (h *Host) finishTask(ctx context.Context, r store.Run, state, reason string) error {
	if r.State == store.StateRunning {
		if err := h.drainLog(ctx, r, true); err != nil {
			log.Printf("run %s: reading its log: %v", r.ID, err)
		}
		if size := h.logSize(r.ID); size >= h.Config.MaxLogBytes {
			r.Note = joinNote(r.Note, fmt.Sprintf("log cut off at %d MiB", size>>20))
		}
	}
	if r.Upload != "" {
		os.Remove(r.Upload)
		r.Upload = ""
	}
	r.State, r.Reason, r.FinishedAt = state, reason, h.now()
	if err := h.Store.PutRun(ctx, r); err != nil {
		return err
	}
	h.takeCancel(r.ID)
	// Best effort, since a lost box can't answer. The files are only small ones.
	_, _ = h.Engine.Exec(ctx, r.Box, "rm", "-rf", runDir(r.ID))
	if err := h.Engine.Stop(ctx, r.Box); err != nil {
		log.Printf("run %s: stopping its box: %v", r.ID, err)
		return err
	}
	return nil
}

func (h *Host) logSize(id string) int64 {
	info, err := os.Stat(h.logPath(id))
	if err != nil {
		return 0
	}
	return info.Size()
}

// staleFiles compares a tree with what the box last received, and returns the
// tracked files to delete. Files the repo ignores aren't listed, so they stay.
func (h *Host) staleFiles(box, tree string) (removed, names []string, err error) {
	names, err = tarNames(tree)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the repo's files: %w", err)
	}
	old, err := os.ReadFile(h.manifestPath(box))
	if errors.Is(err, os.ErrNotExist) {
		return nil, names, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, name := range strings.Split(string(old), "\x00") {
		if name != "" && safeRelative(name) && !slices.Contains(names, name) {
			removed = append(removed, name)
		}
	}
	return removed, names, nil
}

// emptiedDirs lists the parents of removed files, deepest first.
func emptiedDirs(removed []string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, name := range removed {
		for d := path.Dir(name); d != "."; d = path.Dir(d) {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	slices.SortFunc(dirs, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	return dirs
}

func safeRelative(name string) bool {
	return filepath.IsLocal(filepath.FromSlash(name))
}

func tarNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			slices.Sort(names)
			return names, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeDir || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if !safeRelative(hdr.Name) {
			return nil, fmt.Errorf("the tree has an unsafe path: %s", hdr.Name)
		}
		names = append(names, hdr.Name)
	}
}

func (h *Host) saveManifest(box string, names []string) error {
	path := h.manifestPath(box)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(names, "\x00")), 0o600)
}

// sweepFiles deletes logs whose run has been pruned, and uploads nothing will use.
func (h *Host) sweepFiles(ctx context.Context) error {
	ids, err := h.Store.RunIDs(ctx)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(h.StateDir, "logs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".log"); ok && !ids[id] {
			os.Remove(filepath.Join(h.StateDir, "logs", e.Name()))
		}
	}
	queued, err := h.Store.Queued(ctx)
	if err != nil {
		return err
	}
	live, err := h.Store.Live(ctx)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, r := range append(queued, live...) {
		used[r.Upload] = true
	}
	dir := filepath.Join(h.StateDir, "uploads")
	entries, err = os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		info, err := e.Info()
		path := filepath.Join(dir, e.Name())
		if err == nil && !used[path] && h.now().Sub(info.ModTime()) > 24*time.Hour {
			os.Remove(path)
		}
	}
	return nil
}
