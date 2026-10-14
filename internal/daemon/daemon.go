// Package daemon serves pluto's HTTP+JSON API over a unix socket.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	boxlife "github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/mask"
	"github.com/Siddhj2206/pluto/internal/provider"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/terminal"
)

// BoxManager is the container lifecycle the daemon drives: a workspace
// resolves to one box through the vcs adapter, and the box is created,
// started, stopped, inspected, listed, and removed through the container
// engine seam. It is implemented by *box.Manager and faked in tests.
type BoxManager interface {
	Ensure(ctx context.Context, req boxlife.CreateRequest) (*state.Box, bool, error)
	Start(ctx context.Context, id string) (*state.Box, error)
	Stop(ctx context.Context, id string) (*state.Box, error)
	Status(ctx context.Context, id string) (*state.Box, error)
	Remove(ctx context.Context, id string) error
	List(ctx context.Context) ([]*state.Box, error)
	Exec(ctx context.Context, id string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error)
	// Run executes a task's command under its lock policy: shared work runs
	// concurrently, while exclusive work waits for shared work to drain and
	// holds the box alone.
	Run(ctx context.Context, id string, lock contract.Lock, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error)
	// AcquireShared holds a box as a shared holder until release is called. It
	// is the seam a long-lived interactive session — an attach terminal
	// (#144) — registers through.
	AcquireShared(ctx context.Context, id string) (func(), error)
	// CreateEphemeral creates a fresh ephemeral box for one CI run, seeded
	// from the workspace's package layer and mounting no durable volumes.
	CreateEphemeral(ctx context.Context, workspace, image string) (*state.Box, error)
	// Discard removes an ephemeral box after a successful run.
	Discard(ctx context.Context, id string) error
	// Retain marks a failed ephemeral box for inspection.
	Retain(id string, at time.Time) (*state.Box, error)
	// Pin exempts a box from retention eviction; Unpin lifts the exemption.
	Pin(id string) (*state.Box, error)
	Unpin(id string) (*state.Box, error)
}

// boxReaper is the retention surface the daemon drives: evicting retained
// failed CI boxes and collecting orphaned durable boxes. *box.Manager
// implements it; the daemon holds it by assertion so the BoxManager interface
// stays focused on the request path.
type boxReaper interface {
	ReapRetained(ctx context.Context, now time.Time, maxAge time.Duration, maxCount int) (int, error)
	ReapOrphans(ctx context.Context, now time.Time, grace time.Duration) (int, error)
}

// Option configures a Server at construction.
type Option func(*Server)

// WithBoxManager injects the container box lifecycle. Without it, the
// container box routes report that the lifecycle is not configured.
func WithBoxManager(m BoxManager) Option { return func(s *Server) { s.boxes = m } }

// WithTerminalOpener injects the host terminal allocator the daemon owns for
// box attach/watch (#144). Tests use it to drive attach with an in-memory PTY
// instead of a kernel tty.
func WithTerminalOpener(open terminal.Opener) Option {
	return func(s *Server) { s.terminalOpener = open }
}

// Server is the host daemon's HTTP surface.
type Server struct {
	store   *state.Store
	boxes   BoxManager
	version string
	srv     *http.Server
	ln      net.Listener
	// Logf receives daemon notices (idle outcomes, schedule skips and
	// failures, retention prunes). Nil is silent.
	Logf func(format string, args ...any)
	// Now returns the daemon's view of the current time; nil means time.Now.
	// Tests replace it to drive the scheduler deterministically.
	Now func() time.Time
	// RunRetention bounds how long run records and their step logs are kept
	// before the retention loop prunes them. A non-positive window keeps
	// everything. It defaults to state.DefaultRunRetention (30 days).
	RunRetention time.Duration
	// RetainedMaxAge and RetainedMaxCount bound how long a retained failed CI
	// box is kept and how many are kept at once (#148): a box is evicted at the
	// earlier of the two bounds unless pinned. A non-positive value disables
	// that bound. They default to box.DefaultRetainedMaxAge/Count (48h, 5).
	RetainedMaxAge   time.Duration
	RetainedMaxCount int
	// OrphanGrace is how long an orphaned durable box must be unchanged before
	// the retention loop collects it. It defaults to box.DefaultOrphanGrace.
	OrphanGrace time.Duration
	providers   *provider.Registry
	// terminals owns each box's host terminal: the PTY master, its bounded
	// scrollback, and its subscriber fan-out (#144).
	terminals      *terminal.Hub
	terminalOpener terminal.Opener
}

// New builds the server around a store and the container box lifecycle.
func New(store *state.Store, version string, opts ...Option) *Server {
	providers, _ := provider.NewRegistry(provider.NewTailscale(nil), provider.NewOpenTunnelWithState(nil, filepath.Join(store.Root(), "opentunnel-routes.json")))
	s := &Server{store: store, version: version, RunRetention: state.DefaultRunRetention, RetainedMaxAge: boxlife.DefaultRetainedMaxAge, RetainedMaxCount: boxlife.DefaultRetainedMaxCount, OrphanGrace: boxlife.DefaultOrphanGrace, providers: providers, terminalOpener: terminal.Open}
	for _, opt := range opts {
		opt(s)
	}
	s.terminals = terminal.NewHub(s.terminalOpener)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/providers", s.handleProviderList)
	mux.HandleFunc("POST /v1/providers/{id}/{action}", s.handleProviderAction)
	mux.HandleFunc("DELETE /v1/providers/{id}", s.handleProviderRemove)
	mux.HandleFunc("GET /v1/box", s.handleBoxList)
	mux.HandleFunc("POST /v1/box", s.handleBoxCreate)
	mux.HandleFunc("GET /v1/box/{id}", s.handleBoxStatus)
	mux.HandleFunc("POST /v1/box/{id}/start", s.handleBoxStart)
	mux.HandleFunc("POST /v1/box/{id}/stop", s.handleBoxStop)
	mux.HandleFunc("DELETE /v1/box/{id}", s.handleBoxRemove)
	mux.HandleFunc("POST /v1/box/{id}/attach", s.handleBoxAttach)
	mux.HandleFunc("POST /v1/box/{id}/pin", s.handleBoxPin)
	mux.HandleFunc("POST /v1/box/{id}/unpin", s.handleBoxUnpin)
	mux.HandleFunc("POST /v1/run", s.handleRunTask)
	mux.HandleFunc("POST /v1/ci", s.handleRunCI)
	mux.HandleFunc("GET /v1/run", s.handleListRuns)
	mux.HandleFunc("GET /v1/run/{id}", s.handleGetRun)
	mux.HandleFunc("GET /v1/run/{id}/logs", s.handleRunLogs)
	s.srv = &http.Server{Handler: mux}
	return s
}

// SetProviders replaces the host's provider registry. It is primarily useful
// to embed alternate first-party providers while preserving the same API seam.
func (s *Server) SetProviders(registry *provider.Registry) { s.providers = registry }

func (s *Server) handleProviderList(w http.ResponseWriter, r *http.Request) {
	providers := s.providers.List()
	out := api.ProviderListResponse{Providers: make([]api.ProviderStatus, 0, len(providers))}
	for _, p := range providers {
		status, err := p.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		out.Providers = append(out.Providers, api.ProviderStatus{Info: p.Info(), Status: status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProviderAction(w http.ResponseWriter, r *http.Request) {
	p, err := s.providers.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req api.ProviderApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	var actionErr error
	switch r.PathValue("action") {
	case "install":
		actionErr = p.Install(r.Context(), req.Approved)
	case "enable":
		actionErr = p.Enable(r.Context(), req.Approved)
	case "status":
		status, err := p.Status(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, api.ProviderStatus{Info: p.Info(), Status: status})
		return
	case "disable":
		actionErr = p.Disable(r.Context())
	default:
		writeError(w, http.StatusNotFound, errors.New("unknown provider action"))
		return
	}
	if actionErr != nil {
		status := http.StatusBadRequest
		if errors.Is(actionErr, provider.ErrNotInstalled) {
			status = http.StatusConflict
		}
		writeError(w, status, actionErr)
		return
	}
	status, err := p.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ProviderStatus{Info: p.Info(), Status: status})
}

func (s *Server) handleProviderRemove(w http.ResponseWriter, r *http.Request) {
	p, err := s.providers.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err := p.Remove(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	status, err := p.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ProviderStatus{Info: p.Info(), Status: status})
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

// Shutdown stops the server. It ends every box terminal first, so no PTY
// outlives the daemon.
func (s *Server) Shutdown(ctx context.Context) error {
	s.terminals.CloseAll()
	var cleanupErr error
	for _, p := range s.providers.List() {
		if !slices.Contains(p.Info().Capabilities, provider.PublicServiceIngress) {
			continue
		}
		cleanupErr = errors.Join(cleanupErr, p.Disable(ctx))
	}
	return errors.Join(cleanupErr, s.srv.Shutdown(ctx))
}

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

// handleBoxCreate resolves a workspace to its one container box, creating and
// starting it on first use.
func (s *Server) handleBoxCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.boxManager(w)
	if !ok {
		return
	}
	var req api.CreateContainerBoxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Workspace == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace is required"))
		return
	}
	b, created, err := m.Ensure(r.Context(), boxlife.CreateRequest{Workspace: req.Workspace, Image: req.Image})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, b)
}

func (s *Server) handleBoxList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.boxManager(w)
	if !ok {
		return
	}
	boxes, err := m.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ListResponse{Boxes: boxes})
}

func (s *Server) handleBoxStatus(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	b, err := m.Status(r.Context(), id)
	if s.writeBoxLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleBoxStart(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	b, err := m.Start(r.Context(), id)
	if s.writeBoxLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleBoxStop(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	b, err := m.Stop(r.Context(), id)
	if s.writeBoxLookupError(w, err) {
		return
	}
	// A stopped box's terminal is gone with its container.
	s.terminals.Close(id)
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleBoxRemove(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	if err := m.Remove(r.Context(), id); s.writeBoxLookupError(w, err) {
		return
	}
	s.terminals.Close(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleBoxPin exempts a box from retention eviction (#148).
func (s *Server) handleBoxPin(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	b, err := m.Pin(id)
	if s.writeBoxLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleBoxUnpin lifts a box's retention exemption.
func (s *Server) handleBoxUnpin(w http.ResponseWriter, r *http.Request) {
	m, id, ok := s.boxManagerFor(w, r)
	if !ok {
		return
	}
	b, err := m.Unpin(id)
	if s.writeBoxLookupError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleRunTask runs a declared task in its workspace's durable box and adds a
// run with its steps to the task's history. Output streams as newline-delimited
// JSON: output chunks, then one exit event carrying the recorded run.
func (s *Server) handleRunTask(w http.ResponseWriter, r *http.Request) {
	m, ok := s.boxManager(w)
	if !ok {
		return
	}
	var req api.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Workspace == "" || req.Task == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace and task are required"))
		return
	}
	box, _, err := m.Ensure(r.Context(), boxlife.CreateRequest{Workspace: req.Workspace})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	task, err := s.resolveTask(box, project, req.Task)
	if err != nil {
		writeError(w, contractStatus(err), err)
		return
	}

	run := state.StartRun(state.NewID(), req.Task, box.ID, box.WorkspacePath, state.TriggerManual)
	run.Steps = []state.Step{state.NewStep(implicitStepName, task.exec.Command.String())}
	// Persist the running run before it executes, so output is on disk as it is
	// produced and a live client can follow it.
	if _, err := s.store.BeginRun(run); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	stream := &runStream{w: w}
	saved, execErr, storeErr := s.executeRun(r.Context(), m, box, &run, task, stream, nil)
	switch {
	case storeErr != nil:
		s.failRunStream(stream, w, storeErr)
	case execErr != nil:
		s.failRunStream(stream, w, execErr)
	default:
		stream.event(api.RunEvent{Type: api.RunExit, Run: saved})
	}
}

// handleRunCI runs a declared task once in a fresh ephemeral box and records the
// run (#148). The box is seeded from the workspace's provisioned package layer
// and mounts no durable volumes, so CI never reads or mutates a durable box's
// volumes. A successful run discards its box; a failed run retains it, and the
// run's details offer a working attach path.
func (s *Server) handleRunCI(w http.ResponseWriter, r *http.Request) {
	m, ok := s.boxManager(w)
	if !ok {
		return
	}
	var req api.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Workspace == "" || req.Task == "" {
		writeError(w, http.StatusBadRequest, errors.New("workspace and task are required"))
		return
	}
	// Create the fresh box first, so the workspace resolves through the vcs
	// adapter to its canonical path; every later failure discards it.
	box, err := m.CreateEphemeral(r.Context(), req.Workspace, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		s.failCI(w, r.Context(), m, box.ID, http.StatusInternalServerError, err)
		return
	}
	task, err := s.resolveTask(box, project, req.Task)
	if err != nil {
		s.failCI(w, r.Context(), m, box.ID, contractStatus(err), err)
		return
	}

	run := state.StartRun(state.NewID(), req.Task, box.ID, box.WorkspacePath, state.TriggerCI)
	run.Steps = []state.Step{state.NewStep(implicitStepName, task.exec.Command.String())}
	// Persist the running run before it executes, so output is on disk as it is
	// produced and a live client can follow it.
	if _, err := s.store.BeginRun(run); err != nil {
		s.discardEphemeral(r.Context(), m, box.ID)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	stream := &runStream{w: w}
	saved, execErr, storeErr := s.executeRun(r.Context(), m, box, &run, task, stream, func(run *state.Run) {
		// A failed run keeps its box for inspection and attach; a successful
		// one discards it. Retention is best-effort: a run whose box is
		// already gone is still recorded honestly.
		s.retainOrDiscard(r.Context(), m, box.ID, run, *run.FinishedAt)
	})
	switch {
	case storeErr != nil:
		s.failRunStream(stream, w, storeErr)
	case execErr != nil:
		s.failRunStream(stream, w, execErr)
	default:
		stream.event(api.RunEvent{Type: api.RunExit, Run: saved})
	}
}

// failRunStream reports a run failure to the client: on the event stream once
// it has started, or as an error response when nothing has been written yet.
func (s *Server) failRunStream(stream *runStream, w http.ResponseWriter, err error) {
	if stream.started {
		stream.event(api.RunEvent{Type: api.RunError, Error: err.Error()})
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

// retainOrDiscard keeps a failed CI run's box and discards a successful one's.
// It records on the run whether the box was retained, so the run's details can
// offer an attach path. Failures are logged, not fatal: the run is already
// decided, and a missing box is not worth failing an otherwise-recorded run.
func (s *Server) retainOrDiscard(ctx context.Context, m BoxManager, boxID string, run *state.Run, at time.Time) {
	if run.State == state.RunFailed {
		if _, err := m.Retain(boxID, at); err != nil {
			s.logf("ci run %s: retain box %s: %v", state.ShortID(run.ID), state.ShortID(boxID), err)
			return
		}
		run.Retained = true
		return
	}
	s.discardEphemeral(ctx, m, boxID)
}

// discardEphemeral removes a CI run's box, logging a failure rather than
// failing the request: the run's record is the durable outcome.
func (s *Server) discardEphemeral(ctx context.Context, m BoxManager, boxID string) {
	if err := m.Discard(ctx, boxID); err != nil {
		s.logf("discard ephemeral box %s: %v", state.ShortID(boxID), err)
	}
}

// failCI discards a CI run's half-built box and writes the failure, so a bad
// request never leaves an orphaned ephemeral box behind.
func (s *Server) failCI(w http.ResponseWriter, ctx context.Context, m BoxManager, boxID string, status int, err error) {
	s.discardEphemeral(ctx, m, boxID)
	writeError(w, status, err)
}

// contractStatus maps a run request error to an HTTP status: an unknown task or
// a service task is the caller's fault, anything else is the host's.
func contractStatus(err error) int {
	var service serviceTaskError
	if errors.Is(err, contract.ErrNoSuchTask) || errors.As(err, &service) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// implicitStepName labels the one step a single-command task produces.
const implicitStepName = "run"

// runOutputSink is the single host-side boundary a run's output crosses before
// it is written and streamed. It masks the output first, then appends to the
// run's retained log and streams to the client, so a stored log and a live
// stream never diverge and a secret reaches neither.
type runOutputSink struct {
	log    *state.RunLog
	stream *runStream
	// mask redacts declared secrets and mask literals from the stream. It holds
	// back a tail, so Close must be called when the output ends.
	mask *mask.Masker

	// logErr remembers a failed retained write so the run can be described
	// honestly afterward without failing an otherwise-successful command.
	logErr error
}

func (s *runOutputSink) Write(p []byte) (int, error) {
	out := p
	if s.mask != nil {
		out = s.mask.Mask(p)
	}
	s.emit(out)
	// The sink consumes every byte offered, holding back only what it must to
	// mask a value that spans chunks, so a caller sees a full write.
	return len(p), nil
}

// Close releases the masker's held-back tail. Call it when the output ends so
// the final bytes reach the log and the client.
func (s *runOutputSink) Close() {
	if s.mask != nil {
		s.emit(s.mask.Flush())
	}
}

// emit writes already-masked bytes to the retained log first, then the live
// stream, so the two never diverge.
func (s *runOutputSink) emit(out []byte) {
	if len(out) == 0 {
		return
	}
	if s.log != nil {
		if _, err := s.log.Write(out); err != nil && s.logErr == nil {
			s.logErr = err
		}
	}
	// A scheduled run has no client stream; it still crosses this one boundary
	// so a stored log is all that must be produced.
	if s.stream != nil {
		s.stream.event(api.RunEvent{Type: api.RunOutput, Data: out})
	}
}

// handleListRuns returns the host's recorded runs, newest first.
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.Runs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, api.RunListResponse{Runs: runs})
}

// handleGetRun returns one recorded run; the path value may be an unambiguous
// id prefix.
func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.ResolveRun(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, state.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleRunLogs returns a run's retained per-step output, in step order.
func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.ResolveRun(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, state.ErrRunNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	steps := make([]api.RunLogEntry, 0, len(run.Steps))
	for _, step := range run.Steps {
		log, err := s.store.ReadRunLog(step.Log)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		steps = append(steps, api.RunLogEntry{Name: step.Name, State: string(step.State), ExitCode: step.ExitCode, Log: log})
	}
	writeJSON(w, http.StatusOK, api.RunLogsResponse{RunID: run.ID, Steps: steps})
}

// boxManager returns the container lifecycle or reports it unavailable.
func (s *Server) boxManager(w http.ResponseWriter) (BoxManager, bool) {
	if s.boxes == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("the container box lifecycle is not configured"))
		return nil, false
	}
	return s.boxes, true
}

// boxManagerFor resolves a lifecycle and a validated box id from the path.
func (s *Server) boxManagerFor(w http.ResponseWriter, r *http.Request) (BoxManager, string, bool) {
	m, ok := s.boxManager(w)
	if !ok {
		return nil, "", false
	}
	id := r.PathValue("id")
	if !state.ValidID(id) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", id))
		return nil, "", false
	}
	return m, id, true
}

// writeBoxLookupError writes a not-found or failed lookup and reports whether
// it wrote a response.
func (s *Server) writeBoxLookupError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, state.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
		return true
	default:
		writeError(w, http.StatusInternalServerError, err)
		return true
	}
}

// now is the daemon's view of the current time. Tests replace Server.Now to
// drive the scheduler deterministically.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// runStream writes a run's events as newline-delimited JSON. The response
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, api.Error{Error: err.Error()})
}
