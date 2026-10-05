package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Request is one agent operation: one request per connection, a
// newline-delimited JSON line, optionally followed by Bytes of binary
// payload (the sync bundle).
type Request struct {
	Op       string             `json:"op"`
	Contract *contract.Contract `json:"contract,omitempty"`
	Worktree string             `json:"worktree,omitempty"`
	Branch   string             `json:"branch,omitempty"`
	Origin   string             `json:"origin,omitempty"`
	Bytes    int64              `json:"bytes,omitempty"`
	Phase    string             `json:"phase,omitempty"`
	Service  string             `json:"service,omitempty"`
	Lines    int                `json:"lines,omitempty"`
	JobID    string             `json:"job_id,omitempty"`
	Exec     *contract.Exec     `json:"exec,omitempty"`
}

// Response is the agent's reply.
type Response struct {
	OK     bool          `json:"ok"`
	Error  string        `json:"error,omitempty"`
	Status *state.Phases `json:"status,omitempty"`
	Job    *state.Job    `json:"job,omitempty"`
	Log    string        `json:"log,omitempty"`
}

// Serve accepts one request per connection until the listener closes.
func (a *Agent) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go a.handle(conn)
	}
}

func (a *Agent) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, Response{Error: "invalid request: " + err.Error()})
		return
	}
	// A run streams one event per line instead of a single response.
	if req.Op == "run" {
		a.streamRun(conn, req)
		return
	}
	writeResponse(conn, a.dispatch(reader, req))
}

// streamRun runs a job and writes its events as they happen. A job that
// never starts is reported as one error event.
func (a *Agent) streamRun(conn net.Conn, req Request) {
	if req.Exec == nil {
		writeRunEvent(conn, api.RunEvent{Type: api.RunError, Error: "exec is required"})
		return
	}
	job, err := a.RunJob(req.JobID, *req.Exec, req.Worktree, func(data []byte) {
		writeRunEvent(conn, api.RunEvent{Type: api.RunOutput, Data: data})
	})
	if err != nil {
		writeRunEvent(conn, api.RunEvent{Type: api.RunError, Error: err.Error()})
		return
	}
	writeRunEvent(conn, api.RunEvent{Type: api.RunExit, Job: job})
}

func (a *Agent) dispatch(reader *bufio.Reader, req Request) Response {
	switch req.Op {
	case "ping":
		return Response{OK: true}
	case "status":
		st := a.Status()
		return Response{OK: true, Status: &st}
	case "sync":
		if err := a.receiveBundle(reader, req); err != nil {
			return Response{Error: err.Error()}
		}
		st := a.Status()
		return Response{OK: true, Status: &st}
	case "apply":
		if req.Contract == nil {
			return Response{Error: "contract is required"}
		}
		st := a.Apply(req.Contract, req.Worktree)
		return Response{OK: true, Status: &st}
	case "logs":
		log, err := a.Logs(req.Phase, req.Service, req.Lines)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Log: log}
	case "job":
		return Response{OK: true, Job: a.Job()}
	case "job-log":
		log, err := a.JobLog(req.JobID, req.Lines)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Log: log}
	default:
		return Response{Error: "unknown op " + req.Op}
	}
}

// maxBundleBytes caps a sync payload; a larger repository needs a different
// transport (see docs/adr/0008).
const maxBundleBytes = 1 << 30

// syncTimeout bounds a bundle clone inside the box.
const syncTimeout = 10 * time.Minute

func (a *Agent) receiveBundle(reader *bufio.Reader, req Request) error {
	if req.Bytes <= 0 {
		return fmt.Errorf("sync: bytes is required")
	}
	if req.Bytes > maxBundleBytes {
		return fmt.Errorf("sync: bundle of %d bytes exceeds the %d byte limit", req.Bytes, int64(maxBundleBytes))
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	tmp, err := os.CreateTemp(a.root, "incoming-*.bundle")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.CopyN(tmp, reader, req.Bytes); err != nil {
		tmp.Close()
		return fmt.Errorf("read bundle: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return a.Sync(ctx, tmp.Name(), req.Worktree, req.Branch, req.Origin)
}

func writeResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	writeAll(conn, append(data, '\n'))
}

func writeRunEvent(conn net.Conn, event api.RunEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	writeAll(conn, append(data, '\n'))
}

func writeAll(conn net.Conn, data []byte) {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return
		}
		data = data[n:]
	}
}
