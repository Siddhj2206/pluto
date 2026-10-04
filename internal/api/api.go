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

// AttachInfo is the body of POST /v1/boxes/{id}/attach: everything a client
// needs to open an ssh session into a box.
type AttachInfo struct {
	User string `json:"user"`
	UDS  string `json:"uds"`
	Key  string `json:"key"`
	Port uint32 `json:"port"`
}

// ImageInfo describes one imported image version.
type ImageInfo struct {
	Version      string `json:"version"`
	BuiltAt      string `json:"built_at,omitempty"`
	KernelSHA256 string `json:"kernel_sha256,omitempty"`
	RootfsSHA256 string `json:"rootfs_sha256,omitempty"`
}

// ImportImageRequest is the body of POST /v1/images.
type ImportImageRequest struct {
	Path string `json:"path"`
}

// ImportImageResponse is the response of POST /v1/images.
type ImportImageResponse struct {
	Version string `json:"version"`
}

// ImagesResponse is the body of GET /v1/images.
type ImagesResponse struct {
	Images []ImageInfo `json:"images"`
}

// LogsResponse is the body of GET /v1/boxes/{id}/logs.
type LogsResponse struct {
	Log string `json:"log"`
}

// RunRequest is the body of POST /v1/boxes/{id}/run: the command's argv.
type RunRequest struct {
	Argv []string `json:"argv"`
}

// RunEvent is one line of a job's event stream. The agent streams it to the
// daemon, and the daemon relays it to the run's client.
type RunEvent struct {
	Type  string     `json:"type"`
	Data  []byte     `json:"data,omitempty"`
	Job   *state.Job `json:"job,omitempty"`
	Error string     `json:"error,omitempty"`
}

// Job event types.
const (
	RunOutput = "output" // a chunk of the job's output
	RunExit   = "exit"   // the job finished; Job carries the outcome
	RunError  = "error"  // the job never started
)
