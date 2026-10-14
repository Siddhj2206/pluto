// Package api defines the wire vocabulary shared by the daemon and its clients.
package api

import (
	"github.com/Siddhj2206/pluto/internal/provider"
	"github.com/Siddhj2206/pluto/internal/state"
)

type ProviderListResponse struct {
	Providers []ProviderStatus `json:"providers"`
}

type ProviderStatus struct {
	Info   provider.Info   `json:"info"`
	Status provider.Status `json:"status"`
}

type ProviderApprovalRequest struct {
	Approved bool `json:"approved"`
}

// Health is the daemon's liveness report.
type Health struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	StateVersion int    `json:"state_version"`
	Boxes        int    `json:"boxes"`
}

// CreateContainerBoxRequest is the body of POST /v1/box: a workspace path
// resolves through the vcs adapter to its one container box, seeded from an
// optional base image.
type CreateContainerBoxRequest struct {
	Workspace string `json:"workspace"`
	Image     string `json:"image,omitempty"`
}

// ListResponse is the body of GET /v1/box.
type ListResponse struct {
	Boxes  []*state.Box        `json:"boxes"`
	Errors []state.RecordError `json:"errors,omitempty"`
}

// Error is the body of any failed request.
type Error struct {
	Error string `json:"error"`
	// Contract reports the fact that the failure came from the workspace's
	// pluto.yml. The CLI turns the fact into the edit-and-retry hint; hints
	// themselves never ride the wire (ADR 0009).
	Contract bool `json:"contract,omitempty"`
}

// TerminalEvent is one line of a box terminal's stream (POST
// /v1/box/{id}/attach). Output carries program output; Exit closes the stream
// with the terminal process's code; Error reports a failure.
type TerminalEvent struct {
	Type     string `json:"type"`
	Data     []byte `json:"data,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Box terminal event types.
const (
	TerminalOutput = "output" // a chunk of the terminal's output
	TerminalExit   = "exit"   // the terminal process ended
	TerminalError  = "error"  // the terminal could not be started
)

// RunRequest is the body of POST /v1/run and POST /v1/ci: run a named task in
// its workspace's box. A manual run uses the workspace's durable box; a CI run
// uses a fresh ephemeral one.
type RunRequest struct {
	Workspace string `json:"workspace"`
	Task      string `json:"task"`
}

// RunListResponse is the body of GET /v1/run: the host's recorded runs,
// newest first.
type RunListResponse struct {
	Runs []*state.Run `json:"runs"`
}

// RunLogEntry is one step's retained output in a run.
type RunLogEntry struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	ExitCode int    `json:"exit_code"`
	Log      string `json:"log"`
}

// RunLogsResponse is the body of GET /v1/run/{id}/logs: the retained step logs
// of one run, in step order.
type RunLogsResponse struct {
	RunID string        `json:"run_id"`
	Steps []RunLogEntry `json:"steps"`
}

// RunEvent is one line of a run's event stream: the daemon relays a task run's
// output and its final recorded run to the client.
type RunEvent struct {
	Type  string     `json:"type"`
	Data  []byte     `json:"data,omitempty"`
	Run   *state.Run `json:"run,omitempty"`
	Error string     `json:"error,omitempty"`
}

// Run event types.
const (
	RunOutput = "output" // a chunk of the run's output
	RunExit   = "exit"   // the run finished; Run carries the outcome
	RunError  = "error"  // the run never started
)
