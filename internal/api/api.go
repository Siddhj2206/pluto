// Package api defines the wire vocabulary shared by the daemon and its clients.
package api

import "github.com/Siddhj2206/pluto/internal/state"

// Health is the daemon's liveness report.
type Health struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	StateVersion int    `json:"state_version"`
	Boxes        int    `json:"boxes"`
}

// CreateBoxRequest is the body of POST /v1/boxes.
type CreateBoxRequest struct {
	Worktree string `json:"worktree"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
}

// ListResponse is the body of GET /v1/boxes.
type ListResponse struct {
	Boxes  []*state.Box        `json:"boxes"`
	Errors []state.RecordError `json:"errors,omitempty"`
}

// Error is the body of any failed request.
type Error struct {
	Error string `json:"error"`
}
