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

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Server is the host daemon's HTTP surface.
type Server struct {
	store   *state.Store
	version string
	srv     *http.Server
	ln      net.Listener
}

// New builds the server around a store.
func New(store *state.Store, version string) *Server {
	s := &Server{store: store, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/boxes", s.handleList)
	mux.HandleFunc("POST /v1/boxes", s.handleCreate)
	mux.HandleFunc("GET /v1/boxes/{id}", s.handleGet)
	mux.HandleFunc("DELETE /v1/boxes/{id}", s.handleDelete)
	s.srv = &http.Server{Handler: mux}
	return s
}

// Listen binds the unix socket, replacing a stale one.
func (s *Server) Listen(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
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
	id := r.PathValue("id")
	if !state.ValidID(id) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", id))
		return
	}
	box, err := s.store.Box(id)
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, box)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !state.ValidID(id) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", id))
		return
	}
	err := s.store.DestroyBox(id)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, api.Error{Error: err.Error()})
}
