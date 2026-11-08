package host

import (
	"archive/tar"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newTaskHarness is newHarness with a StateDir, which task runs need for logs
// and uploads.
func newTaskHarness(t *testing.T) *harness {
	t.Helper()
	x := newHarness(t)
	x.h.StateDir = t.TempDir()
	must(t, x.h.ensureID(context.Background()))
	return x
}

func (x *harness) boxFor(repo string, tk task.Task) string {
	x.t.Helper()
	name, err := x.h.boxFor(repo, tk)
	must(x.t, err)
	return name
}

// putSource stores a repo's last good config, as a fetch would.
func (x *harness) putSource(tasks ...task.Task) {
	x.t.Helper()
	cfg := task.Config{Tasks: map[string]task.Task{}}
	for _, tk := range tasks {
		cfg.Tasks[tk.Name] = tk
	}
	data, err := json.Marshal(cfg)
	must(x.t, err)
	must(x.t, x.h.Store.PutSource(context.Background(), store.Source{Repo: testRepo, URL: "https://github.com/o/r.git",
		Branch: "main", SHA: "abc", Config: string(data), FetchedAt: x.now}))
}

// putRun stores a task run in the test repo's box, filling in what tests don't set.
func (x *harness) putRun(r store.Run) store.Run {
	x.t.Helper()
	if r.ID == "" {
		r.ID = "r_" + newID()
	}
	r.Repo, r.Kind = testRepo, store.KindTask
	if r.Box == "" {
		r.Box = x.boxFor(testRepo, task.Task{})
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = x.now
	}
	must(x.t, x.h.Store.PutRun(context.Background(), r))
	return r
}

func nightly(expr string) task.Task {
	return task.Task{Name: "nightly", Run: []string{"make test"}, Schedule: expr, Timeout: task.DefaultTimeout}
}

func TestFireSchedulesQueuesOnlyAtSlots(t *testing.T) {
	tests := []struct {
		name       string
		checkedAgo time.Duration // How long ago the schedule was last checked.
		wantRuns   int
		wantNote   string // A substring of the run's note, or "" for no note.
	}{
		{"nothing before the next slot", 0, 0, ""},
		{"one queued run at a slot, with no note", time.Minute, 1, ""},
		{"missed slots collapse into one run", 5 * time.Minute, 1, "5 missed slots became this one run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			ctx := context.Background()
			x.putSource(nightly("* * * * *"))
			checked := x.now.Add(-tt.checkedAgo)
			must(t, x.h.Store.PutSchedule(ctx, store.Schedule{Repo: testRepo, Task: "nightly", Expr: "* * * * *", CheckedAt: checked}))

			must(t, x.h.fireSchedules(ctx))

			queued, err := x.h.Store.Queued(ctx)
			must(t, err)
			if len(queued) != tt.wantRuns {
				t.Fatalf("queued %d runs, want %d: %+v", len(queued), tt.wantRuns, queued)
			}
			schedules, err := x.h.Store.Schedules(ctx)
			must(t, err)
			if tt.wantRuns == 0 {
				if !schedules[0].CheckedAt.Equal(checked) {
					t.Fatalf("CheckedAt moved to %v before any slot passed", schedules[0].CheckedAt)
				}
				return
			}
			if !schedules[0].CheckedAt.Equal(x.now) {
				t.Errorf("CheckedAt = %v, want %v", schedules[0].CheckedAt, x.now)
			}
			r := queued[0]
			if r.Kind != store.KindTask || r.Job != "nightly" || r.Trigger != store.TriggerSchedule {
				t.Errorf("run = %+v", r)
			}
			if tt.wantNote == "" && r.Note != "" {
				t.Errorf("note = %q, want none", r.Note)
			}
			if tt.wantNote != "" && !strings.Contains(r.Note, tt.wantNote) {
				t.Errorf("note = %q, want it to contain %q", r.Note, tt.wantNote)
			}
		})
	}
}

func TestFireSchedulesSkipsASlotWhileTheTaskIsStillGoing(t *testing.T) {
	x := newTaskHarness(t)
	ctx := context.Background()
	x.putSource(nightly("* * * * *"))
	must(t, x.h.Store.PutSchedule(ctx, store.Schedule{Repo: testRepo, Task: "nightly", Expr: "* * * * *", CheckedAt: x.now.Add(-time.Minute)}))
	previous := x.putRun(store.Run{State: store.StateRunning, Job: "nightly", Trigger: store.TriggerSchedule, StartedAt: x.now})

	must(t, x.h.fireSchedules(ctx))

	queued, err := x.h.Store.Queued(ctx)
	must(t, err)
	if len(queued) != 0 {
		t.Fatalf("an overlapping slot queued %+v", queued)
	}
	runs, err := x.h.Store.Runs(ctx, 10)
	must(t, err)
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	var skipped store.Run
	for _, r := range runs {
		if r.State == store.StateSkipped {
			skipped = r
		}
	}
	if skipped.ID == "" {
		t.Fatalf("no skipped run in %+v", runs)
	}
	if skipped.Kind != store.KindTask || skipped.Job != "nightly" || skipped.Trigger != store.TriggerSchedule {
		t.Errorf("skipped run = %+v", skipped)
	}
	if !strings.Contains(skipped.Reason, "previous run was still going") {
		t.Errorf("reason = %q", skipped.Reason)
	}
	if got, _ := x.h.Store.Run(ctx, previous.ID); got.State != store.StateRunning {
		t.Errorf("the running run changed to %s", got.State)
	}
}

func TestNoteGapMarksActiveRunsOnlyAfterALongGap(t *testing.T) {
	tests := []struct {
		name     string
		gap      time.Duration
		wantNote bool
	}{
		{"a short pause", 30 * time.Second, false},
		{"just under a minute", 59 * time.Second, false},
		{"a long gap", 5 * time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			ctx := context.Background()
			must(t, x.h.noteGap(ctx)) // Starts the clock.
			active := x.putRun(store.Run{State: store.StateRunning, StartedAt: x.now, Deadline: x.now.Add(time.Hour)})
			finished := x.putRun(store.Run{State: store.StatePassed, FinishedAt: x.now, Deadline: x.now.Add(time.Hour)})

			x.now = x.now.Add(tt.gap)
			must(t, x.h.noteGap(ctx))

			got, err := x.h.Store.Run(ctx, active.ID)
			must(t, err)
			if tt.wantNote {
				if !strings.Contains(got.Note, "machine asleep or host stopped") {
					t.Errorf("note = %q", got.Note)
				}
				if want := active.Deadline.Add(tt.gap); !got.Deadline.Equal(want) {
					t.Errorf("deadline = %v, want %v", got.Deadline, want)
				}
			} else {
				if got.Note != "" {
					t.Errorf("note = %q, want none", got.Note)
				}
				if !got.Deadline.Equal(active.Deadline) {
					t.Errorf("deadline moved to %v", got.Deadline)
				}
			}
			if done, _ := x.h.Store.Run(ctx, finished.ID); done.Note != "" {
				t.Errorf("a finished run got note %q", done.Note)
			}
		})
	}
}

func TestReconcileTaskFinishesARunFromItsBoxState(t *testing.T) {
	tests := []struct {
		name       string
		status     string // What the box reports about the run's script.
		overdue    bool   // The run's deadline has passed.
		cancel     bool   // Someone asked to cancel the run.
		gone       bool   // The box no longer exists.
		wantState  string
		wantReason string
	}{
		{"exit 0 passes", "exit 0", false, false, false, store.StatePassed, ""},
		{"exit 7 fails", "exit 7", false, false, false, store.StateFailed, "a step exited with code 7"},
		{"dead becomes lost", "dead", false, false, false, store.StateLost, "the run's process ended without reporting a result"},
		{"past its time limit fails", "alive", true, false, false, store.StateFailed, "it reached its time limit"},
		{"a missing box is lost", "alive", false, false, true, store.StateLost, "the box disappeared"},
		{"a requested cancel is cancelled", "alive", false, true, false, store.StateCancelled, ""},
		{"a live run within its limit keeps going", "alive", false, false, false, store.StateRunning, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			ctx := context.Background()
			run := x.putRun(store.Run{State: store.StateRunning, StartedAt: x.now, Deadline: x.now.Add(time.Hour)})
			if tt.overdue {
				run.Deadline = x.now.Add(-time.Second)
				run = x.putRun(run)
			}
			if !tt.gone {
				x.eng.boxes[run.Box] = &fakeBox{running: true, status: tt.status}
			}
			if tt.cancel {
				x.h.mu.Lock()
				x.h.cancels = map[string]bool{run.ID: true}
				x.h.mu.Unlock()
			}

			must(t, x.h.reconcileTask(ctx, run))

			got, err := x.h.Store.Run(ctx, run.ID)
			must(t, err)
			if got.State != tt.wantState || got.Reason != tt.wantReason {
				t.Fatalf("state = %s (%q), want %s (%q)", got.State, got.Reason, tt.wantState, tt.wantReason)
			}
			if tt.wantState == store.StateRunning {
				if slices.Contains(x.eng.stopped, run.Box) {
					t.Error("a live run's box was stopped")
				}
				return
			}
			if got.FinishedAt.IsZero() {
				t.Error("a finished run has no finish time")
			}
			if !slices.Contains(x.eng.stopped, run.Box) {
				t.Errorf("the finished run's box wasn't stopped; stopped = %v", x.eng.stopped)
			}
			if len(x.eng.removed) != 0 {
				t.Errorf("a durable box was removed: %v", x.eng.removed)
			}
		})
	}
}

func TestCollectOrphansNeverRemovesADurableBox(t *testing.T) {
	x := newTaskHarness(t)
	durable := x.boxFor(testRepo, task.Task{})
	x.eng.boxes[durable] = &fakeBox{running: true, spec: engine.Spec{Name: durable, Durable: true}}
	x.eng.boxes["pluto-stray"] = &fakeBox{running: true, spec: engine.Spec{Name: "pluto-stray"}}

	must(t, x.h.collectOrphans(context.Background()))

	if !slices.Contains(x.eng.removed, "pluto-stray") {
		t.Error("a non-durable box with no record should be removed")
	}
	if slices.Contains(x.eng.removed, durable) {
		t.Error("a durable box was removed")
	}
	if _, ok := x.eng.boxes[durable]; !ok {
		t.Error("the durable box is gone")
	}
}

func TestCheckAllowedGatesPrivilegeAndSecrets(t *testing.T) {
	privileged := []string{task.Privileged}
	tests := []struct {
		name     string
		rootful  bool
		grant    bool
		requires []string
		needs    []string
		stored   map[string]string
		wantErr  string            // A substring of the error, or "" when the run is allowed.
		wantEnv  map[string]string // Only the secrets the task lists.
	}{
		{"privilege without a grant", false, false, privileged, nil, nil, "pluto box grant --privileged", nil},
		{"granted, but the engine is rootful", true, true, privileged, nil, nil, "rootless engine", nil},
		{"granted on a rootless engine", false, true, privileged, nil, nil, "", nil},
		{"a listed secret that isn't set", false, false, nil, []string{"API_TOKEN"}, nil, "API_TOKEN", nil},
		{"a listed secret is passed, others are not", false, false, nil, []string{"API_TOKEN"},
			map[string]string{"API_TOKEN": "tok", "OTHER": "x"}, "", map[string]string{"API_TOKEN": "tok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			ctx := context.Background()
			x.eng.rootful = tt.rootful
			if tt.grant {
				must(t, x.h.Store.Grant(ctx, testRepo, task.Privileged))
			}
			for name, value := range tt.stored {
				must(t, x.h.Store.SetSecret(ctx, testRepo, name, value))
			}
			plan := Plan{Task: task.Task{Name: "t", Run: []string{"x"}, Timeout: time.Hour, Secrets: tt.needs}, Requires: tt.requires}

			env, err := x.h.checkAllowed(ctx, store.Run{Repo: testRepo}, plan)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(env) != len(tt.wantEnv) {
					t.Fatalf("env = %v, want %v", env, tt.wantEnv)
				}
				for k, v := range tt.wantEnv {
					if env[k] != v {
						t.Fatalf("env = %v, want %v", env, tt.wantEnv)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestBoxForIsOneBoxPerRepo(t *testing.T) {
	build := task.Task{Name: "build"}
	nightly := task.Task{Name: "nightly"}
	tests := []struct {
		name         string
		repoA, repoB string
		taskA, taskB task.Task
		same         bool
	}{
		{"same repo and task", testRepo, testRepo, build, build, true},
		{"same repo, different tasks share a box", testRepo, testRepo, build, nightly, true},
		{"different repos get different boxes", testRepo, "o/other", build, build, false},
		{"names differing only in punctuation differ", "o/r", "o-r", build, build, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			a := x.boxFor(tt.repoA, tt.taskA)
			b := x.boxFor(tt.repoB, tt.taskB)
			if (a == b) != tt.same {
				t.Fatalf("boxes %q and %q: same = %v, want %v", a, b, a == b, tt.same)
			}
		})
	}
}

// writeTar writes a tree with empty regular files at names, as the working tree
// upload does.
func writeTar(t *testing.T, path string, names ...string) string {
	t.Helper()
	f, err := os.Create(path)
	must(t, err)
	defer f.Close()
	tw := tar.NewWriter(f)
	for _, name := range names {
		must(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: 0}))
	}
	must(t, tw.Close())
	return path
}

func TestStaleFilesFindsFilesTheRepoDeleted(t *testing.T) {
	tests := []struct {
		name        string
		manifest    []string // What the box got last time, or nil for a first run.
		tree        []string
		wantRemoved []string
	}{
		{"a first run removes nothing", nil, []string{"a.txt", "b.txt"}, nil},
		{"files gone from the tree are listed", []string{"a.txt", "b.txt", "dir/c.txt"}, []string{"a.txt", "new.txt"}, []string{"b.txt", "dir/c.txt"}},
		{"unsafe names in a manifest are never removed", []string{"../escape", "a.txt"}, []string{"a.txt"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newTaskHarness(t)
			const box = "pluto-box"
			if tt.manifest != nil {
				must(t, x.h.saveManifest(box, tt.manifest))
			}
			tree := writeTar(t, filepath.Join(t.TempDir(), "tree.tar"), tt.tree...)

			removed, names, err := x.h.staleFiles(box, tree)
			must(t, err)
			if strings.Join(removed, ",") != strings.Join(tt.wantRemoved, ",") {
				t.Errorf("removed = %v, want %v", removed, tt.wantRemoved)
			}
			if strings.Join(names, ",") != strings.Join(tt.tree, ",") {
				t.Errorf("names = %v, want %v", names, tt.tree)
			}
		})
	}
}

func TestRunScriptRunsEachStepInOrderAndStopsAtAFailure(t *testing.T) {
	for _, steps := range []int{1, 2, 3} {
		t.Run(stepFile(steps-1), func(t *testing.T) {
			script := runScript("/var/tmp/pluto-r1", steps)
			last := -1
			for i := range steps {
				at := strings.Index(script, stepFile(i))
				if at <= last {
					t.Fatalf("%s is at %d, not after %d", stepFile(i), at, last)
				}
				last = at
			}
			if strings.Contains(script, stepFile(steps)) {
				t.Errorf("the script mentions a step that doesn't exist")
			}
			if got := strings.Count(script, "|| exit $?"); got != steps {
				t.Errorf("%d steps can stop the script, want %d", got, steps)
			}
			if !strings.Contains(script, `echo $code > "$d/exit.tmp"`) {
				t.Error("the script doesn't write its exit code last")
			}
		})
	}
}

func TestOnMainsFollowsTheMainsSupply(t *testing.T) {
	tests := []struct {
		name   string
		supply map[string]string // Files under the supply directory, or nil for no directory.
		want   bool
	}{
		{"mains online", map[string]string{"AC/type": "Mains\n", "AC/online": "1\n", "BAT0/type": "Battery\n"}, true},
		{"mains offline", map[string]string{"AC/type": "Mains\n", "AC/online": "0\n", "BAT0/type": "Battery\n"}, false},
		{"no supply directory assumes mains", nil, true},
		{"no supplies listed assumes mains", map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "power_supply")
			if tt.supply == nil {
				dir = filepath.Join(t.TempDir(), "missing")
			} else {
				must(t, os.MkdirAll(dir, 0o755))
			}
			for rel, body := range tt.supply {
				path := filepath.Join(dir, filepath.FromSlash(rel))
				must(t, os.MkdirAll(filepath.Dir(path), 0o755))
				must(t, os.WriteFile(path, []byte(body), 0o644))
			}
			if got := onMains(dir); got != tt.want {
				t.Fatalf("onMains = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnMainsWithOnlyABattery(t *testing.T) {
	for status, want := range map[string]bool{"Discharging": false, "Charging": true} {
		dir := t.TempDir()
		bat := filepath.Join(dir, "BAT0")
		if err := os.Mkdir(bat, 0o755); err != nil {
			t.Fatal(err)
		}
		for file, value := range map[string]string{"type": "Battery\n", "status": status + "\n"} {
			if err := os.WriteFile(filepath.Join(bat, file), []byte(value), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := onMains(dir); got != want {
			t.Errorf("battery %s: onMains = %v, want %v", status, got, want)
		}
	}
}

// A laptop that sleeps past a run's limit must not fail the run on waking. The
// time asleep has to be added before anything checks the limit.
func TestSleepMovesTheDeadlineBeforeItIsChecked(t *testing.T) {
	x := newHarness(t)
	x.h.StateDir = t.TempDir()
	ctx := context.Background()
	if err := x.h.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	box := "pluto-box"
	x.eng.boxes[box] = &fakeBox{running: true, status: "alive"}
	r := store.Run{ID: "r_sleep", Repo: testRepo, Box: box, Kind: store.KindTask, State: store.StateRunning,
		Job: "long", CreatedAt: x.now, StartedAt: x.now, Deadline: x.now.Add(time.Hour)}
	if err := x.h.Store.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(8 * time.Hour)
	if err := x.h.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := x.h.Store.Run(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateRunning || got.Note == "" {
		t.Fatalf("after sleeping past the limit: %+v", got)
	}
}

func TestCancelBeatsAQueuedRunOnlyOnce(t *testing.T) {
	x := newHarness(t)
	ctx := context.Background()
	r := store.Run{ID: "r_q", Repo: testRepo, Box: "b", Kind: store.KindTask, State: store.StateQueued, CreatedAt: x.now}
	if err := x.h.Store.PutRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	moved, err := x.h.Store.MoveQueued(ctx, r.ID, store.StateCancelled, x.now)
	if err != nil || !moved {
		t.Fatalf("cancel: moved=%v err=%v", moved, err)
	}
	moved, err = x.h.Store.MoveQueued(ctx, r.ID, store.StateWaiting, x.now)
	if err != nil || moved {
		t.Fatalf("a cancelled run must not start: moved=%v err=%v", moved, err)
	}
}

func TestBoxForIgnoresCaseAndKeepsExistingBox(t *testing.T) {
	x := newTaskHarness(t)
	if a, b := x.boxFor("O/R", task.Task{}), x.boxFor("o/r", task.Task{}); a != b {
		t.Fatalf("case changed the box: %q vs %q", a, b)
	}
	// A box made under an older spelling keeps its name.
	must(t, x.h.Store.PutBox(context.Background(), store.Box{Name: "pluto-old-box", Repo: "o/old"}))
	if got := x.boxFor("O/Old", task.Task{}); got != "pluto-old-box" {
		t.Fatalf("got %q", got)
	}
}

func TestImageVolumesAreKeptAcrossANewImageAndDeletedByReset(t *testing.T) {
	ctx := context.Background()
	x := newTaskHarness(t)
	x.eng.imageVolumes = []string{"/var/lib/containers", "/root/.cache", "/workspace/data", "/rootkit", "/", "/proc/x", "/sys", "/dev"}
	r := x.putRun(store.Run{})
	plan := Plan{Image: "img:1"}
	must(t, x.h.ensureBox(ctx, r, plan))

	box, err := x.h.Store.BoxByName(ctx, r.Box)
	must(t, err)
	if len(box.DataVolumes) != 4 || box.DataVolumes["/var/lib/containers"] == "" || box.DataVolumes["/rootkit"] == "" ||
		box.DataVolumes["/root/.cache"] == "" || box.DataVolumes["/workspace/data"] == "" {
		t.Fatalf("recorded volumes = %v", box.DataVolumes)
	}
	vol := box.DataVolumes["/var/lib/containers"]
	if x.eng.boxes[r.Box].spec.Volumes[vol] != "/var/lib/containers" {
		t.Fatalf("not mounted: %v", x.eng.boxes[r.Box].spec.Volumes)
	}

	if x.eng.boxes[r.Box].spec.Volumes[box.DataVolumes["/workspace/data"]] != "/workspace/data" {
		t.Fatalf("nested path not mounted: %v", x.eng.boxes[r.Box].spec.Volumes)
	}
	notes, err := x.h.ensureBoxNotes(ctx, r, Plan{Image: "img:1b"})
	must(t, err)
	if len(notes) != 4 || !strings.Contains(notes[0], "/") {
		t.Fatalf("notes = %q", notes)
	}
	for _, v := range x.eng.boxes[r.Box].spec.Volumes {
		if v == "/" || v == "/sys" || v == "/dev" || v == "/proc/x" {
			t.Fatalf("system path mounted: %v", x.eng.boxes[r.Box].spec.Volumes)
		}
	}
	// A new image that declares one path keeps all volumes, mounting one.
	x.eng.imageVolumes = []string{"/var/lib/containers"}
	must(t, x.h.ensureBox(ctx, r, Plan{Image: "img:2"}))
	box, err = x.h.Store.BoxByName(ctx, r.Box)
	must(t, err)
	if box.DataVolumes["/var/lib/containers"] != vol || len(box.DataVolumes) != 4 {
		t.Fatalf("volumes after a new image = %v", box.DataVolumes)
	}
	mounts := x.eng.boxes[r.Box].spec.Volumes
	if mounts[vol] != "/var/lib/containers" || mounts[box.DataVolumes["/rootkit"]] != "" {
		t.Fatalf("mounts = %v", mounts)
	}
	// Going back to the old image mounts the kept volume again.
	x.eng.imageVolumes = []string{"/var/lib/containers", "/rootkit"}
	must(t, x.h.ensureBox(ctx, r, Plan{Image: "img:1"}))
	if got := x.eng.boxes[r.Box].spec.Volumes[box.DataVolumes["/rootkit"]]; got != "/rootkit" {
		t.Fatalf("mounts = %v", x.eng.boxes[r.Box].spec.Volumes)
	}

	if got := x.h.boxBytes(ctx, box); got != 6<<20 {
		t.Errorf("boxBytes = %d, want workspace, home and two image volumes", got)
	}

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"repo":"` + testRepo + `"}`)
	x.h.handleBoxReset(rec, httptest.NewRequest("POST", "/", body))
	if rec.Code != 204 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	for _, v := range []string{box.WorkspaceVolume, box.HomeVolume, vol, box.DataVolumes["/rootkit"]} {
		if !slices.Contains(x.eng.removedVols, v) {
			t.Errorf("reset left volume %s (removed %v)", v, x.eng.removedVols)
		}
	}
}

func TestPruneSourcesRemovesOnlyUnmappedCopies(t *testing.T) {
	ctx := context.Background()
	x := newTaskHarness(t)
	x.putSource()
	root := filepath.Join(x.h.StateDir, "sources")
	live := filepath.Join(root, slug(testRepo)+".git")
	stale := filepath.Join(root, slug("Old/Spelling")+".git")
	other := filepath.Join(root, "notes")
	for _, d := range []string{live, stale, other} {
		must(t, os.MkdirAll(filepath.Join(d, "x"), 0o700))
	}
	outside := t.TempDir()
	must(t, os.Symlink(outside, filepath.Join(root, "link.git")))
	must(t, x.h.pruneSources(ctx))
	for d, want := range map[string]bool{live: true, stale: false, other: true, outside: true} {
		if _, err := os.Stat(d); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", d, err == nil, want)
		}
	}
}

func TestBoxForReturnsAStoreError(t *testing.T) {
	x := newTaskHarness(t)
	x.h.Store.Close()
	if _, err := x.h.boxFor(testRepo, task.Task{}); err == nil {
		t.Fatal("expected an error, not a new box name")
	}
}

// waitsByID reads every run through the API, as the CLI does.
func (x *harness) waitsByID() map[string]string {
	x.t.Helper()
	rec := x.call(http.MethodGet, "/v1/runs")
	var runs []store.Run
	must(x.t, json.Unmarshal(rec.Body.Bytes(), &runs))
	out := map[string]string{}
	for _, r := range runs {
		out[r.ID] = r.Waiting
	}
	return out
}

func (x *harness) state(id string) string {
	x.t.Helper()
	r, err := x.h.Store.Run(context.Background(), id)
	must(x.t, err)
	return r.State
}

func TestQueuedRunsSayWhyTheyWait(t *testing.T) {
	x := newTaskHarness(t)
	x.putRun(store.Run{ID: "r_busy", Job: "build", State: store.StateRunning})
	x.putRun(store.Run{ID: "r_same", Job: "test", State: store.StateQueued})
	x.putRun(store.Run{ID: "r_other", Box: "other-box", Job: "lint", State: store.StateQueued})

	must(t, x.h.startQueued(context.Background()))
	x.h.prepares.Wait()

	waits := x.waitsByID()
	if want := "waiting for r_busy (task build) to finish in this repo's box"; waits["r_same"] != want {
		t.Errorf("same box: %q, want %q", waits["r_same"], want)
	}
	if want := "waiting for a free slot: this host runs one thing at a time, and r_busy (task build) is running"; waits["r_other"] != want {
		t.Errorf("other box: %q, want %q", waits["r_other"], want)
	}
	if waits["r_busy"] != "" {
		t.Errorf("a running run has a wait: %q", waits["r_busy"])
	}
	rec := x.call(http.MethodGet, "/v1/runs/r_same/logs")
	if got := rec.Header().Get(HeaderWaiting); got != waits["r_same"] {
		t.Errorf("log header: %q", got)
	}
}

func TestRunsSomeoneWaitsOnGoBeforeScheduledOnes(t *testing.T) {
	x := newTaskHarness(t)
	x.putRun(store.Run{ID: "r_sched", Box: "box-a", Job: "nightly", Trigger: store.TriggerSchedule, State: store.StateQueued, CreatedAt: x.now.Add(-time.Hour)})
	x.putRun(store.Run{ID: "r_manual", Box: "box-b", Job: "test", Trigger: store.TriggerManual, State: store.StateQueued})

	must(t, x.h.startQueued(context.Background()))
	x.h.prepares.Wait()

	if got := x.state("r_manual"); got == store.StateQueued {
		t.Fatal("the manual run didn't start, though someone is waiting on it")
	}
	if got := x.state("r_sched"); got != store.StateQueued {
		t.Fatalf("the older scheduled run took the only slot: %s", got)
	}
	if w := x.waitsByID()["r_sched"]; !strings.Contains(w, "free slot") {
		t.Errorf("scheduled run's wait: %q", w)
	}
}

func TestScheduledRunsWaitForQueuedCIJobs(t *testing.T) {
	x := newTaskHarness(t)
	x.putRun(store.Run{ID: "r_sched", Job: "nightly", Trigger: store.TriggerSchedule, State: store.StateQueued})
	x.h.status = map[string]RepoStatus{testRepo: {Queued: 1}}

	must(t, x.h.startQueued(context.Background()))
	if got := x.state("r_sched"); got != store.StateQueued {
		t.Fatalf("a scheduled run started ahead of a queued CI job: %s", got)
	}
	if w := x.waitsByID()["r_sched"]; !strings.Contains(w, "queued CI jobs") {
		t.Errorf("wait: %q", w)
	}

	// A CI job that can't start mustn't hold scheduled work back forever.
	x.h.status = map[string]RepoStatus{testRepo: {Queued: 1, Problem: "downloading the runner image"}}
	must(t, x.h.startQueued(context.Background()))
	x.h.prepares.Wait()
	if got := x.state("r_sched"); got == store.StateQueued {
		t.Fatal("a stuck CI job held back the scheduled run")
	}
}

func TestCIStatusSaysJobsWaitForASlot(t *testing.T) {
	x := newTaskHarness(t)
	x.putRun(store.Run{ID: "r_busy", Job: "build", State: store.StateRunning})
	x.queueJob()
	repo, err := x.h.Store.Repo(context.Background(), testRepo)
	must(t, err)
	st, err := x.h.pollRepo(context.Background(), repo)
	must(t, err)
	if want := "waiting for a free slot: this host runs one thing at a time, and r_busy (task build) is running"; st.Waiting != want {
		t.Fatalf("waiting = %q, want %q", st.Waiting, want)
	}
}
