// Package daemon serves pluto's HTTP+JSON API over a unix socket.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

// BoxRunner is the runner surface the daemon needs. It is implemented by
// *runner.Runner and faked in tests.
type BoxRunner interface {
	Up(ctx context.Context, box *state.Box) (*state.Box, error)
	Pause(box *state.Box) (*state.Box, error)
	Attach(ctx context.Context, box *state.Box) (api.AttachInfo, error)
	Reconcile(box *state.Box) (*state.Box, error)
	Refresh(box *state.Box) (*state.Box, error)
	RunJob(ctx context.Context, box *state.Box, spec contract.Exec, emit func([]byte)) (*state.Box, *state.Job, error)
	Logs(box *state.Box, phase, service string, lines int) (string, error)
	JobLog(box *state.Box, jobID string, lines int) (string, error)
	Metrics(box *state.Box) (json.RawMessage, error)
	AutoPauseWindow(box *state.Box) time.Duration
	ContractStale(box *state.Box) bool
	Destroy(id string) error
	Import(srcDir string) (string, error)
	Images() ([]api.ImageInfo, error)
}

// Server is the host daemon's HTTP surface.
type Server struct {
	store   *state.Store
	runner  BoxRunner
	version string
	srv     *http.Server
	ln      net.Listener
	// Logf receives daemon notices (auto-pause outcomes, schedule skips and
	// failures). Nil is silent.
	Logf func(format string, args ...any)
	// Now returns the daemon's view of the current time; nil means time.Now.
	// Tests replace it to drive the scheduler deterministically.
	Now func() time.Time

	// sessionMu guards sessionSamples: the last cumulative cgroup counters
	// the auto-pause loop saw for each box, so it can tell whether a declared
	// session burned CPU/IO since the previous tick. The agent reports
	// counters; the comparison (and so the busy policy) stays here. Only the
	// loop advances a sample (rememberSessions); status reads are read-only.
	sessionMu          sync.Mutex
	sessionSamples     map[string]state.SessionUsage
	MaxRunningBoxes    int
	QueueCapacity      int
	QueueAgingInterval time.Duration
	queueDispatchMu    sync.Mutex
	queueRunning       int
	queueReservedBoxes map[string]bool
	webhookMu          sync.RWMutex
	workItemMu         sync.Mutex
	webhooks           map[string]githubWebhook
	genericWebhooks    map[string]githubWebhook
}

// New builds the server around a store and a runner.
func New(store *state.Store, runner BoxRunner, version string) *Server {
	s := &Server{store: store, runner: runner, version: version, MaxRunningBoxes: 4, QueueCapacity: 100, QueueAgingInterval: 5 * time.Minute, webhooks: make(map[string]githubWebhook), genericWebhooks: make(map[string]githubWebhook)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/boxes", s.handleList)
	mux.HandleFunc("POST /v1/boxes", s.handleCreate)
	mux.HandleFunc("GET /v1/boxes/{id}", s.handleGet)
	mux.HandleFunc("DELETE /v1/boxes/{id}", s.handleDelete)
	mux.HandleFunc("POST /v1/boxes/{id}/up", s.handleUp)
	mux.HandleFunc("POST /v1/boxes/{id}/pause", s.handlePause)
	mux.HandleFunc("POST /v1/boxes/{id}/attach", s.handleAttach)
	mux.HandleFunc("POST /v1/boxes/{id}/run", s.handleRun)
	mux.HandleFunc("POST /v1/boxes/{id}/queue", s.handleQueueRequest)
	mux.HandleFunc("GET /v1/queue", s.handleQueueList)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks", s.handleListTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("POST /v1/tasks/{id}/runs", s.handleCreateTaskRun)
	mux.HandleFunc("GET /v1/tasks/{id}/runs/{run_id}", s.handleGetTaskRun)
	mux.HandleFunc("POST /v1/events", s.handlePostCommitEvent)
	mux.HandleFunc("GET /v1/boxes/{id}/logs", s.handleLogs)
	mux.HandleFunc("GET /v1/boxes/{id}/metrics", s.handleMetrics)
	mux.HandleFunc("POST /v1/images", s.handleImportImage)
	mux.HandleFunc("GET /v1/images", s.handleListImages)
	s.srv = &http.Server{Handler: mux}
	return s
}

// WebhookHandler exposes only the inbound webhook route for a TLS proxy.
func (s *Server) WebhookHandler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /github/{source}", s.handleGitHubPush)
	m.HandleFunc("POST /generic/{source}", s.handleGenericWebhook)
	return m
}

// Listen binds the unix socket, replacing a stale one. It refuses when a
// live daemon already listens there.
func (s *Server) Listen(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	if conn, err := net.Dial("unix", socketPath); err == nil {
		conn.Close()
		return fmt.Errorf("a pluto daemon is already listening on %s", socketPath)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.ln = ln
	return nil
}

// Serve blocks until Shutdown.
func (s *Server) Serve() error { return s.srv.Serve(s.ln) }

// Shutdown stops the server.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	boxes, _, err := s.store.Boxes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Health{
		Status:       "ok",
		Version:      s.version,
		StateVersion: state.StateVersion,
		Boxes:        len(boxes),
	})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	boxes, recordErrs, err := s.store.Boxes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for i, box := range boxes {
		// Reconcile is best effort: a systemd hiccup must not fail a listing.
		if reconciled, err := s.runner.Reconcile(box); err == nil {
			boxes[i] = reconciled
		}
	}
	writeJSON(w, http.StatusOK, api.ListResponse{Boxes: boxes, Errors: recordErrs})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateBoxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Worktree == "" {
		if req.RepoURL == "" {
			writeError(w, http.StatusBadRequest, errors.New("worktree or repo_url is required"))
			return
		}
		worktree, project, branch, err := s.cloneRemote(req.RepoURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.Worktree, req.Project, req.Branch = worktree, project, branch
	}
	box, created, err := s.store.CreateBoxWithRepo(req.Project, req.Branch, req.Worktree, req.RepoURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, box)
}

// cloneRemote creates the host-side source checkout used to load the trusted
// default-branch contract and build the initial bundle. Git's configured
// credential helper and SSH agent are inherited; no credential is retained.
func (s *Server) cloneRemote(repoURL string) (string, string, string, error) {
	if repoURL == "" {
		return "", "", "", errors.New("invalid repository URL")
	}
	if parsed, err := url.Parse(repoURL); err == nil && parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if hasPassword || parsed.Scheme != "ssh" {
			return "", "", "", errors.New("repository URL must not contain credentials; configure Git's credential helper or SSH agent")
		}
	}
	root := filepath.Join(s.store.Root(), "projects")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", "", err
	}
	name := filepath.Base(strings.TrimSuffix(repoURL, ".git"))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "project"
	}
	digest := sha256.Sum256([]byte(repoURL))
	worktree := filepath.Join(root, fmt.Sprintf("%s-%x", name, digest[:5]))
	if _, err := os.Stat(filepath.Join(worktree, ".git")); errors.Is(err, os.ErrNotExist) {
		cmd := exec.Command("git", "clone", "--", repoURL, worktree)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", "", fmt.Errorf("clone remote repository: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return "", "", "", err
	}
	out, err := exec.Command("git", "-C", worktree, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", "", "", fmt.Errorf("resolve repository default branch: %w", err)
	}
	return worktree, name, strings.TrimSpace(string(out)), nil
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if reconciled, err := s.runner.Reconcile(box); err == nil {
		box = reconciled
	}
	// Best effort: surface the agent's live contract phases, then fold the
	// fresh view into the auto-pause clock. Only the loop pauses boxes; a
	// status look must not change the lifecycle.
	if box.State == state.StateRunning {
		if refreshed, err := s.runner.Refresh(box); err == nil {
			box, _ = s.evaluateAutoPause(refreshed, s.now())
		} else {
			// The daemon has no live view; say so instead of reporting a
			// stale idle clock. This touches only the response copy.
			box.AutoPauseSetting = "unknown"
		}
	}
	// The worktree's contract lives on this host, so the host daemon is the
	// one place that can compare it with what the box applied; a remote CLI
	// may not have the worktree at all. Response-only, equal contracts are
	// silent.
	if s.runner.ContractStale(box) {
		box.ContractStale = true
	}
	writeJSON(w, http.StatusOK, box)
}

// lookup validates the path id and fetches the box, writing the error itself
// when it cannot.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*state.Box, bool) {
	id := r.PathValue("id")
	if !state.ValidID(id) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", id))
		return nil, false
	}
	box, err := s.store.Box(id)
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return box, true
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidID(id) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", id))
		return
	}
	err := s.runner.Destroy(id)
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.forgetSessions(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	box, err := s.runner.Up(r.Context(), box)
	if err != nil {
		if errors.Is(err, envcache.ErrBuilding) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, box)
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	box, err := s.runner.Pause(box)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, box)
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req api.AttachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Session != "" {
		declared, err := sessionDeclared(box, req.Session)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !declared {
			writeJSON(w, http.StatusBadRequest, api.Error{
				Error:   fmt.Sprintf("box %s has no session %q", state.ShortID(box.ID), req.Session),
				Session: true,
			})
			return
		}
	}
	info, err := s.runner.Attach(r.Context(), box)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// sessionDeclared resolves a session name against the worktree's current
// contract, at request time like a job name (ADR 0007). A missing contract is
// simply no sessions; a broken one is the caller's to fix.
func sessionDeclared(box *state.Box, name string) (bool, error) {
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return false, err
	}
	return slices.Contains(ct.SessionNames(), name), nil
}

// handleRun runs a job and relays its event stream as newline-delimited
// JSON. A run refused before it starts (a concurrent job) is a normal error
// response; once events have flowed, failures arrive as an error event.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req api.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if (req.Job == "") == (len(req.Argv) == 0) {
		writeError(w, http.StatusBadRequest, errors.New("run takes exactly one of a job name or an ad-hoc argv"))
		return
	}
	spec, err := resolveRun(box, req)
	if err != nil {
		if errors.Is(err, contract.ErrNoSuchJob) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stream := &runStream{w: w}
	_, job, err := s.runner.RunJob(r.Context(), box, spec, func(data []byte) {
		stream.event(api.RunEvent{Type: api.RunOutput, Data: data})
	})
	if err != nil {
		switch {
		case stream.started:
			stream.event(api.RunEvent{Type: api.RunError, Error: err.Error()})
		case errors.Is(err, state.ErrJobRunning), errors.Is(err, envcache.ErrBuilding):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	stream.event(api.RunEvent{Type: api.RunExit, Job: job})
}

func (s *Server) handleQueueRequest(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	var req api.QueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	item := state.QueueItem{Source: state.QueueExplicit, BoxID: box.ID, Job: req.Job, Argv: req.Argv}
	if req.Up {
		if req.Job != "" || len(req.Argv) > 0 {
			writeError(w, http.StatusBadRequest, errors.New("queue request takes up or a job/argv"))
			return
		}
	} else {
		if (req.Job == "") == (len(req.Argv) == 0) {
			writeError(w, http.StatusBadRequest, errors.New("queue request takes exactly one of up, job, or argv"))
			return
		}
		if _, err := resolveRun(box, api.RunRequest{Job: req.Job, Argv: req.Argv}); err != nil {
			if errors.Is(err, contract.ErrNoSuchJob) {
				writeError(w, http.StatusBadRequest, err)
			} else {
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}
	}
	queued, err := s.store.Enqueue(item, s.QueueCapacity, s.now())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, state.ErrQueueFull) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, api.QueueResponse{Item: *queued})
}

func (s *Server) handleQueueList(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Queue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.QueueListResponse{Items: items})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req api.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.IdempotencyKey == "" || req.BoxID == "" || req.Job == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, errors.New("box_id, job, prompt, and idempotency_key are required"))
		return
	}
	box, err := s.store.Box(req.BoxID)
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := resolveJob(box, req.Job); err != nil {
		if errors.Is(err, contract.ErrNoSuchJob) {
			writeError(w, http.StatusBadRequest, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	if req.Project == "" {
		req.Project = box.Project
	}
	if req.Ref == "" {
		req.Ref = box.Ref
	}
	task, err := s.store.CreateTask(state.Task{Source: req.Source, Project: req.Project, Ref: req.Ref, BoxID: req.BoxID, IdempotencyKey: req.IdempotencyKey}, state.TaskRun{Prompt: req.Prompt, Job: req.Job}, state.QueueItem{}, s.QueueCapacity, s.now())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, state.ErrQueueFull) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, state.ErrIdempotencyConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusAccepted, api.TaskResponse{Task: *task})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.store.Tasks()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.TaskListResponse{Tasks: tasks})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Task(r.PathValue("id"))
	if errors.Is(err, state.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.TaskResponse{Task: *task})
}

func (s *Server) handleGetTaskRun(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.Task(r.PathValue("id"))
	if errors.Is(err, state.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, run := range task.Runs {
		if run.ID == r.PathValue("run_id") {
			writeJSON(w, http.StatusOK, run)
			return
		}
	}
	writeError(w, http.StatusNotFound, errors.New("run not found"))
}

func (s *Server) handleCreateTaskRun(w http.ResponseWriter, r *http.Request) {
	var req api.TaskRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	task, err := s.store.Task(r.PathValue("id"))
	if errors.Is(err, state.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.IdempotencyKey == "" || req.Job == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, errors.New("job, prompt, and idempotency_key are required"))
		return
	}
	box, err := s.store.Box(task.BoxID)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Errorf("task execution box is unavailable: %w", err))
		return
	}
	if _, err := resolveJob(box, req.Job); err != nil {
		if errors.Is(err, contract.ErrNoSuchJob) {
			writeError(w, http.StatusBadRequest, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	updated, err := s.store.AppendTaskRun(task.ID, state.TaskRun{Job: req.Job, Prompt: req.Prompt, IdempotencyKey: req.IdempotencyKey}, s.QueueCapacity, s.now())
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, state.ErrQueueFull) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, state.ErrIdempotencyConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	for _, run := range updated.Runs {
		if run.IdempotencyKey == req.IdempotencyKey {
			writeJSON(w, http.StatusAccepted, api.TaskRunResponse{Run: run})
			return
		}
	}
	writeError(w, http.StatusInternalServerError, errors.New("queued run was not retained"))
}

// resolveRun resolves a run request against the worktree's current
// .pluto.toml: a named job or an ad-hoc argv under the top-level env. Names
// resolve here, at run time, not from whatever the box applied (ADR 0007).
func resolveRun(box *state.Box, req api.RunRequest) (contract.Exec, error) {
	if req.Job != "" {
		return resolveJob(box, req.Job)
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return contract.Exec{}, err
	}
	return ct.AdHocExec(req.Argv), nil
}

// resolveJob resolves a declared job name against the worktree's current
// contract, at run time (ADR 0007).
func resolveJob(box *state.Box, name string) (contract.Exec, error) {
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return contract.Exec{}, err
	}
	return ct.ExecJob(name)
}

// runStream writes a job's events as newline-delimited JSON. The response
// head is held back until the first event, so a refused run can still answer
// with a proper error status.
type runStream struct {
	w       http.ResponseWriter
	enc     *json.Encoder
	started bool
}

func (s *runStream) event(event api.RunEvent) {
	if !s.started {
		s.started = true
		s.w.Header().Set("Content-Type", "application/x-ndjson")
		s.w.WriteHeader(http.StatusOK)
		s.enc = json.NewEncoder(s.w)
	}
	_ = s.enc.Encode(event)
	// Output must reach the client as it happens, not when the job ends.
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// handleLogs returns a phase log or a service journal from the box's agent,
// or a job's recorded output from the host.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	phase := r.URL.Query().Get("phase")
	service := r.URL.Query().Get("service")
	job := r.URL.Query().Get("job")
	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if job != "" {
		rec, err := box.ResolveJob(job)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		log, err := s.runner.JobLog(box, rec.ID, lines)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, api.LogsResponse{Log: log})
		return
	}
	log, err := s.runner.Logs(box, phase, service, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.LogsResponse{Log: log})
}

// handleMetrics returns the box's latest Firecracker metrics snapshot. A box
// that has not started (or has not flushed yet) reports 404 rather than an
// empty snapshot.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	metrics, err := s.runner.Metrics(box)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, fmt.Errorf("box %s has no metrics yet", state.ShortID(box.ID)))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.MetricsResponse{Metrics: metrics})
}

func (s *Server) handleImportImage(w http.ResponseWriter, r *http.Request) {
	var req api.ImportImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	version, err := s.runner.Import(req.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ImportImageResponse{Version: version})
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.runner.Images()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ImagesResponse{Images: images})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, api.Error{Error: err.Error(), Contract: errors.Is(err, contract.ErrInvalid)})
}
