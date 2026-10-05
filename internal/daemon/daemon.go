// Package daemon serves pluto's HTTP+JSON API over a unix socket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
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
	RunJob(ctx context.Context, box *state.Box, argv []string, emit func([]byte)) (*state.Box, *state.Job, error)
	Logs(box *state.Box, phase, service string, lines int) (string, error)
	JobLog(box *state.Box, jobID string, lines int) (string, error)
	AutoPauseWindow(box *state.Box) time.Duration
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
	// Logf receives daemon notices (auto-pause outcomes). Nil is silent.
	Logf func(format string, args ...any)
}

// New builds the server around a store and a runner.
func New(store *state.Store, runner BoxRunner, version string) *Server {
	s := &Server{store: store, runner: runner, version: version}
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
	mux.HandleFunc("GET /v1/boxes/{id}/logs", s.handleLogs)
	mux.HandleFunc("POST /v1/images", s.handleImportImage)
	mux.HandleFunc("GET /v1/images", s.handleListImages)
	s.srv = &http.Server{Handler: mux}
	return s
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
		writeError(w, http.StatusBadRequest, errors.New("worktree is required"))
		return
	}
	box, created, err := s.store.CreateBox(req.Project, req.Branch, req.Worktree)
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
			box, _ = s.evaluateAutoPause(refreshed, time.Now())
		} else {
			// The daemon has no live view; say so instead of reporting a
			// stale idle clock. This touches only the response copy.
			box.AutoPauseSetting = "unknown"
		}
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
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUp(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	box, err := s.runner.Up(r.Context(), box)
	if err != nil {
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
	info, err := s.runner.Attach(r.Context(), box)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
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
	if len(req.Argv) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("argv is required"))
		return
	}
	stream := &runStream{w: w}
	_, job, err := s.runner.RunJob(r.Context(), box, req.Argv, func(data []byte) {
		stream.event(api.RunEvent{Type: api.RunOutput, Data: data})
	})
	if err != nil {
		switch {
		case stream.started:
			stream.event(api.RunEvent{Type: api.RunError, Error: err.Error()})
		case errors.Is(err, state.ErrJobRunning):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	stream.event(api.RunEvent{Type: api.RunExit, Job: job})
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
	writeJSON(w, status, api.Error{Error: err.Error()})
}
