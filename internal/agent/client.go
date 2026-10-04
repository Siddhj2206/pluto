package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

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

// Sync streams a git bundle to the agent.
func (c *Client) Sync(bundle, worktree, branch string) error {
	f, err := os.Open(bundle)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = c.call(Request{Op: "sync", Worktree: worktree, Branch: branch, Bytes: info.Size()}, f, c.syncTimeout)
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
