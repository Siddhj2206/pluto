package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/Siddhj2206/pluto/internal/engine"
	"github.com/Siddhj2206/pluto/internal/store"
	"github.com/Siddhj2206/pluto/internal/task"
)

const maxUpload = 8 << 30

// HeaderState carries a run's state with its log, so one poll can follow both.
const HeaderState = "X-Pluto-State"

// HeaderWaiting carries why a queued run hasn't started.
const HeaderWaiting = "X-Pluto-Waiting"

// UploadResponse names a stored copy of a working tree.
type UploadResponse struct {
	Upload string `json:"upload"`
}

// RunTaskRequest queues a task from a working tree.
type RunTaskRequest struct {
	Repo   string `json:"repo"`
	Plan   Plan   `json:"plan"`
	Upload string `json:"upload"`
}

// RepoRequest names a repo, and sometimes a privilege or secret in it.
type RepoRequest struct {
	Repo  string `json:"repo"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// BoxInfo describes a durable box. Bytes is -1 when it can't be measured.
type BoxInfo struct {
	store.Box
	Running bool  `json:"running"`
	Bytes   int64 `json:"bytes"`
}

// ScheduleEnableRequest turns on a repo's schedules from one of its branches.
type ScheduleEnableRequest struct {
	Repo   string `json:"repo"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// ScheduleInfo describes one scheduled task.
type ScheduleInfo struct {
	Repo      string    `json:"repo"`
	Task      string    `json:"task"`
	Expr      string    `json:"expr"`
	Next      time.Time `json:"next,omitzero"`
	LastRun   string    `json:"last_run,omitempty"`
	LastState string    `json:"last_state,omitempty"`
	LastNote  string    `json:"last_note,omitempty"`
}

// SourceInfo describes where a repo's schedules come from.
type SourceInfo struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// ScheduleList is the schedules view.
type ScheduleList struct {
	Sources   []SourceInfo   `json:"sources"`
	Schedules []ScheduleInfo `json:"schedules"`
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		fail(w, http.StatusBadRequest, "invalid request: "+err.Error(), "")
		return false
	}
	return true
}

func (h *Host) taskLogs(w http.ResponseWriter, r *http.Request, run store.Run) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		offset = 0
	}
	w.Header().Set(HeaderState, run.State)
	if waiting := h.withWait(run).Waiting; waiting != "" {
		w.Header().Set(HeaderWaiting, waiting)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	f, err := os.Open(h.logPath(run.ID))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("writing logs: %v", err)
	}
}

func (h *Host) handleCancel(w http.ResponseWriter, r *http.Request) {
	run, ok := h.findRun(w, r)
	if !ok {
		return
	}
	switch {
	case run.Kind != store.KindTask:
		fail(w, http.StatusConflict, "run "+run.ID+" is a CI job, so GitHub controls it.", "cancel it on GitHub")
	case run.State == store.StateQueued || run.Active():
		if run.State == store.StateQueued {
			moved, err := h.Store.MoveQueued(r.Context(), run.ID, store.StateCancelled, h.now())
			if err != nil {
				fail(w, http.StatusInternalServerError, err.Error(), "")
				return
			}
			if moved {
				if run.Upload != "" {
					os.Remove(run.Upload)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// The loop has just started it, so it needs stopping like any other.
		}
		h.mu.Lock()
		if h.cancels == nil {
			h.cancels = map[string]bool{}
		}
		h.cancels[run.ID] = true
		cancel := h.preparing[run.ID]
		h.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		fail(w, http.StatusConflict, "run "+run.ID+" has already finished: "+run.State+".", "")
	}
}

func (h *Host) handleUpload(w http.ResponseWriter, r *http.Request) {
	id := newID()
	path := h.uploadPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload)); err != nil {
		os.Remove(path)
		fail(w, http.StatusBadRequest, "couldn't store the repo's files: "+err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, UploadResponse{Upload: id})
}

func (h *Host) handleRunTask(w http.ResponseWriter, r *http.Request) {
	var req RunTaskRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Repo == "" || len(req.Plan.Task.Run) == 0 || req.Plan.Task.Timeout <= 0 {
		fail(w, http.StatusBadRequest, "a repo and a task with a run list and time limit are required", "")
		return
	}
	upload := ""
	if req.Upload != "" {
		if filepath.Base(req.Upload) != req.Upload {
			fail(w, http.StatusBadRequest, "invalid upload", "")
			return
		}
		upload = h.uploadPath(req.Upload)
		if _, err := os.Stat(upload); err != nil {
			fail(w, http.StatusBadRequest, "that upload no longer exists", "")
			return
		}
	}
	run, err := h.Submit(r.Context(), req.Repo, req.Plan, "working tree", "", store.TriggerManual, upload, "")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Host) handleBoxes(w http.ResponseWriter, r *http.Request) {
	boxes, err := h.Store.Boxes(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	out := make([]BoxInfo, 0, len(boxes))
	for _, b := range boxes {
		running, err := h.Engine.Running(r.Context(), b.Name)
		if err != nil && !errors.Is(err, engine.ErrNotFound) {
			fail(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
		out = append(out, BoxInfo{Box: b, Running: running, Bytes: h.boxBytes(r.Context(), b)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Host) boxBytes(ctx context.Context, b store.Box) int64 {
	var total uint64
	for _, v := range b.AllVolumes() {
		size, err := h.Engine.VolumeBytes(ctx, v)
		if err != nil {
			return -1
		}
		total += size
	}
	return int64(total)
}

func (h *Host) handleBoxReset(w http.ResponseWriter, r *http.Request) {
	var req RepoRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Repo == "" {
		fail(w, http.StatusBadRequest, "a repo is required", "") // An empty name would list every box.
		return
	}
	boxes, err := h.Store.Boxes(r.Context(), req.Repo)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if len(boxes) == 0 {
		fail(w, http.StatusNotFound, req.Repo+" has no box yet.", "")
		return
	}
	live, err := h.Store.Live(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	for _, b := range boxes {
		if i := slices.IndexFunc(live, func(run store.Run) bool { return run.Box == b.Name && run.Active() }); i >= 0 {
			fail(w, http.StatusConflict, "run "+live[i].ID+" is using this box.", "pluto cancel "+live[i].ID)
			return
		}
	}
	for _, b := range boxes {
		errs := []error{h.Engine.Remove(r.Context(), b.Name)}
		for _, v := range b.AllVolumes() {
			errs = append(errs, h.Engine.RemoveVolume(r.Context(), v))
		}
		if err := errors.Join(errs...); err != nil {
			fail(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
		os.Remove(h.manifestPath(b.Name))
		if err := h.Store.DeleteBox(r.Context(), b.Name); err != nil {
			fail(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) handleGrant(w http.ResponseWriter, r *http.Request) {
	var req RepoRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Repo == "" || req.Name != task.Privileged {
		fail(w, http.StatusBadRequest, "the only privilege so far is "+task.Privileged, "")
		return
	}
	rootless, err := h.Engine.Rootless(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if !rootless {
		fail(w, http.StatusConflict, "this host's engine runs as root, and a privileged box on it would amount to root on the host.", "use rootless podman")
		return
	}
	if err := h.Store.Grant(r.Context(), req.Repo, req.Name); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) handleRevoke(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.Store.Revoke(r.Context(), q.Get("repo"), q.Get("name")); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) handleSecretSet(w http.ResponseWriter, r *http.Request) {
	var req RepoRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Repo == "" || !task.ValidSecretName(req.Name) {
		fail(w, http.StatusBadRequest, "a secret's name should look like MY_SECRET", "")
		return
	}
	if err := h.Store.SetSecret(r.Context(), req.Repo, req.Name, req.Value); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) handleSecretList(w http.ResponseWriter, r *http.Request) {
	names, err := h.Store.SecretNames(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (h *Host) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	found, err := h.Store.DeleteSecret(r.Context(), q.Get("repo"), q.Get("name"))
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if !found {
		fail(w, http.StatusNotFound, "no secret named "+q.Get("name"), "pluto secret list")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Host) handleScheduleEnable(w http.ResponseWriter, r *http.Request) {
	var req ScheduleEnableRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Repo == "" || req.URL == "" || !branchName.MatchString(req.Branch) {
		fail(w, http.StatusBadRequest, "a repo, its URL and a valid branch name are required", "")
		return
	}
	// Enabling again over a working setup keeps its last good config if this fails.
	src, err := h.Store.SourceOf(r.Context(), req.Repo)
	existed := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	src.Repo, src.URL, src.Branch = req.Repo, req.URL, req.Branch
	if err := h.Store.PutSource(r.Context(), src); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if _, err := h.refreshSource(r.Context(), src); err != nil {
		if !existed {
			if derr := h.Store.DeleteSource(r.Context(), req.Repo); derr != nil {
				log.Printf("removing source %s: %v", req.Repo, derr)
			}
		}
		fail(w, http.StatusBadGateway, "couldn't read "+req.Branch+": "+err.Error(), "")
		return
	}
	h.handleSchedules(w, r)
}

func (h *Host) handleSchedules(w http.ResponseWriter, r *http.Request) {
	sources, err := h.Store.Sources(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	schedules, err := h.Store.Schedules(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	out := ScheduleList{Sources: []SourceInfo{}, Schedules: []ScheduleInfo{}}
	for _, s := range sources {
		out.Sources = append(out.Sources, SourceInfo{Repo: s.Repo, Branch: s.Branch, SHA: s.SHA, Problem: s.Problem})
	}
	for _, sc := range schedules {
		info := ScheduleInfo{Repo: sc.Repo, Task: sc.Task, Expr: sc.Expr}
		if parsed, err := task.ParseSchedule(sc.Expr); err == nil {
			info.Next = parsed.Next(h.now())
		}
		last, err := h.Store.LastTaskRun(r.Context(), sc.Repo, sc.Task)
		if err == nil {
			info.LastRun, info.LastState, info.LastNote = last.ID, last.State, joinNote(last.Reason, last.Note)
		}
		out.Schedules = append(out.Schedules, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// ScheduleRunRequest runs one of a repo's tasks from its schedule branch, now.
type ScheduleRunRequest struct {
	Repo string `json:"repo"`
	Task string `json:"task"`
}

func (h *Host) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	var req ScheduleRunRequest
	if !decodeBody(w, r, &req) {
		return
	}
	src, err := h.Store.SourceOf(r.Context(), req.Repo)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusConflict, req.Repo+" doesn't have a branch to run from yet.", "pluto schedule enable")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	// Fetch now, so a task pushed a minute ago is found and the commit is known.
	// If the fetch fails, the last good config still answers.
	if fresh, err := h.refreshSource(r.Context(), src); err == nil {
		src = fresh
	}
	var cfg task.Config
	if err := json.Unmarshal([]byte(src.Config), &cfg); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	t, ok := cfg.Tasks[req.Task]
	if !ok {
		fail(w, http.StatusNotFound, "there's no task named "+req.Task+" on "+src.Branch+".", "pluto task list   (and commit the task if it's new)")
		return
	}
	run, err := h.Submit(r.Context(), req.Repo, Plan{Task: t, Image: cfg.Image, Requires: cfg.Requires}, src.Branch, src.SHA, store.TriggerBranch, "", "")
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleBoxEnter opens a repo's durable box without a run, starting it if needed.
func (h *Host) handleBoxEnter(w http.ResponseWriter, r *http.Request) {
	var req RepoRequest
	if !decodeBody(w, r, &req) {
		return
	}
	boxes, err := h.Store.Boxes(r.Context(), req.Repo)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if req.Repo == "" || len(boxes) == 0 {
		fail(w, http.StatusNotFound, req.Repo+" has no box yet. A repo gets one the first time it runs a task.", "pluto run <task>")
		return
	}
	box := boxes[0].Name
	running, err := h.Engine.Running(r.Context(), box)
	if errors.Is(err, engine.ErrNotFound) {
		fail(w, http.StatusConflict, req.Repo+"'s box is gone. Its next run creates it again.", "pluto run <task>")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	if !running {
		if err := h.Engine.Resume(r.Context(), box); err != nil {
			fail(w, http.StatusInternalServerError, err.Error(), "")
			return
		}
	}
	resp := EnterResponse{Argv: h.Engine.EnterArgv(box, workspaceDir)}
	live, err := h.Store.Live(r.Context())
	if err == nil {
		if i := slices.IndexFunc(live, func(run store.Run) bool { return run.Box == box && run.Active() }); i >= 0 {
			resp.Warning = "Run " + live[i].ID + " is using this box, and your shell ends when that run does."
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Host) handleScheduleDisable(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.DeleteSource(r.Context(), r.URL.Query().Get("repo")); err != nil {
		fail(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
