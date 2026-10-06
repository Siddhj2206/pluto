package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vsock"
)

// Client talks to a box's agent through Firecracker's vsock mux.
type Client struct {
	dial        func() (net.Conn, error)
	callTimeout time.Duration
	syncTimeout time.Duration
}

// NewClient dials the box's vsock mux UDS.
func NewClient(vsockUDS string) *Client {
	return &Client{
		dial:        func() (net.Conn, error) { return vsock.ConnectWithin(vsockUDS, Port, 15*time.Second) },
		callTimeout: 15 * time.Second,
		syncTimeout: 10 * time.Minute,
	}
}

func (c *Client) call(req Request, payload io.Reader, timeout time.Duration) (*Response, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	writeAll(conn, append(data, '\n'))
	if payload != nil {
		if _, err := io.Copy(conn, payload); err != nil {
			return nil, err
		}
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "agent request failed"
		}
		return nil, errors.New(resp.Error)
	}
	return &resp, nil
}

// Ping checks that the agent answers.
func (c *Client) Ping() error {
	_, err := c.call(Request{Op: "ping"}, nil, c.callTimeout)
	return err
}

// Status returns the agent's phase state.
func (c *Client) Status() (state.Phases, error) {
	resp, err := c.call(Request{Op: "status"}, nil, c.callTimeout)
	if err != nil {
		return state.Phases{}, err
	}
	if resp.Status == nil {
		return state.Phases{}, errors.New("agent returned no status")
	}
	return *resp.Status, nil
}

// Sync streams a git bundle to the agent. remotes is the host worktree's
// remote list, mirrored into the box after the clone; empty means the worktree
// has none and the box is local-only.
func (c *Client) Sync(bundle, worktree, branch string, remotes []state.Remote) error {
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = c.call(Request{Op: "sync", Worktree: worktree, Branch: branch, Remotes: remotes, Bytes: info.Size()}, f, c.syncTimeout)
	return err
}

// Apply sends the contract and returns the agent's current status.
func (c *Client) Apply(ct *contract.Contract, worktree string) (state.Phases, error) {
	resp, err := c.call(Request{Op: "apply", Contract: ct, Worktree: worktree}, nil, c.callTimeout)
	if err != nil {
		return state.Phases{}, err
	}
	if resp.Status == nil {
		return state.Phases{}, errors.New("agent returned no status")
	}
	return *resp.Status, nil
}

// Logs returns a phase log or a service journal.
func (c *Client) Logs(phase, service string, lines int) (string, error) {
	resp, err := c.call(Request{Op: "logs", Phase: phase, Service: service, Lines: lines}, nil, c.callTimeout)
	if err != nil {
		return "", err
	}
	return resp.Log, nil
}

// JobStatus returns the agent's latest job, or nil when none ran.
func (c *Client) JobStatus() (*state.Job, error) {
	resp, err := c.call(Request{Op: "job"}, nil, c.callTimeout)
	if err != nil {
		return nil, err
	}
	return resp.Job, nil
}

// JobLog returns the tail of a job's output as recorded in the box.
func (c *Client) JobLog(jobID string, lines int) (string, error) {
	resp, err := c.call(Request{Op: "job-log", JobID: jobID, Lines: lines}, nil, c.callTimeout)
	if err != nil {
		return "", err
	}
	return resp.Log, nil
}

// Run streams a job's output through emit and returns its recorded outcome.
// The call is unbounded: a job runs until its command exits, not until the
// client looks away.
func (c *Client) Run(jobID string, spec contract.Exec, worktree string, emit func([]byte)) (*state.Job, error) {
	conn, err := c.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	data, err := json.Marshal(Request{Op: "run", JobID: jobID, Exec: &spec, Worktree: worktree})
	if err != nil {
		return nil, err
	}
	writeAll(conn, append(data, '\n'))

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("job stream: %w", err)
		}
		var event api.RunEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode job event: %w", err)
		}
		switch event.Type {
		case api.RunOutput:
			emit(event.Data)
		case api.RunExit:
			if event.Job == nil {
				return nil, errors.New("job stream ended without an outcome")
			}
			return event.Job, nil
		case api.RunError:
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unknown job event %q", event.Type)
		}
	}
}
