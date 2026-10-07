// Package api defines the wire vocabulary shared by the daemon and its clients.
package api

import (
	"encoding/json"

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

// CreateBoxRequest is the body of POST /v1/boxes.
type CreateBoxRequest struct {
	Worktree string `json:"worktree"`
	Project  string `json:"project"`
	Branch   string `json:"branch"`
	RepoURL  string `json:"repo_url,omitempty"`
}

// ListResponse is the body of GET /v1/boxes.
type ListResponse struct {
	Boxes  []*state.Box        `json:"boxes"`
	Errors []state.RecordError `json:"errors,omitempty"`
}

// Error is the body of any failed request.
type Error struct {
	Error string `json:"error"`
	// Contract reports the fact that the failure came from the worktree's
	// .pluto.toml. The CLI turns the fact into the edit-and-retry hint;
	// hints themselves never ride the wire (ADR 0009).
	Contract bool `json:"contract,omitempty"`
	// Session reports the fact that an attach named a session the box's
	// worktree does not declare. The CLI turns the fact into the status hint.
	Session bool `json:"session,omitempty"`
}

// AttachRequest is the body of POST /v1/boxes/{id}/attach. Session is empty
// for a plain shell attach.
type AttachRequest struct {
	Session string `json:"session,omitempty"`
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
	Version         string `json:"version"`
	SourceDateEpoch int64  `json:"source_date_epoch,omitempty"`
	KernelSHA256    string `json:"kernel_sha256,omitempty"`
	RootfsSHA256    string `json:"rootfs_sha256,omitempty"`
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

// MetricsResponse is the body of GET /v1/boxes/{id}/metrics: the latest
// Firecracker metrics snapshot, passed through exactly as Firecracker emitted
// it so pluto stays decoupled from Firecracker's metrics schema.
type MetricsResponse struct {
	Metrics json.RawMessage `json:"metrics"`
}

// LogsResponse is the body of GET /v1/boxes/{id}/logs.
type LogsResponse struct {
	Log string `json:"log"`
}

// RunRequest is the body of POST /v1/boxes/{id}/run: either a declared job's
// name, resolved against the worktree's current .pluto.toml, or an ad-hoc
// command's argv.
type RunRequest struct {
	Argv []string `json:"argv,omitempty"`
	Job  string   `json:"job,omitempty"`
}

// QueueRequest is an explicit request to wake a box or run a job without
// waiting for host capacity.
type QueueRequest struct {
	Job  string   `json:"job,omitempty"`
	Argv []string `json:"argv,omitempty"`
	Up   bool     `json:"up,omitempty"`
}
type QueueResponse struct {
	Item state.QueueItem `json:"item"`
}
type QueueListResponse struct {
	Items []state.QueueItem `json:"items"`
}

// PostCommitEvent is a local Git hook notification. The daemon resolves the
// worktree to an existing box; receiving it never creates or starts one.
type PostCommitEvent struct {
	Worktree string `json:"worktree"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch"`
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
