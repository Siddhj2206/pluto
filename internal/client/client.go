// Package client is a typed HTTP client for the host daemon's unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// ErrUnreachable reports that the daemon could not be reached.
var ErrUnreachable = errors.New("cannot reach the pluto daemon")

// ErrNotFound reports a box that does not exist.
var ErrNotFound = errors.New("box not found")

// Client talks to the daemon over its unix socket.
type Client struct {
	hc     *http.Client
	base   string
	socket string
}

// New dials the daemon at socketPath.
func New(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{hc: &http.Client{Transport: tr}, base: "http://pluto", socket: socketPath}
}

func (c *Client) do(method, path string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w at %s: %v", ErrUnreachable, c.socket, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr api.Error
		if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
			return resp.StatusCode, errors.New(apiErr.Error)
		}
		return resp.StatusCode, fmt.Errorf("daemon returned %s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// Health reports daemon liveness.
func (c *Client) Health() (*api.Health, error) {
	var h api.Health
	if _, err := c.do("GET", "/v1/health", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// CreateBox creates or returns the box for a worktree. The bool reports
// whether a new box was created.
func (c *Client) CreateBox(req api.CreateBoxRequest) (*state.Box, bool, error) {
	var box state.Box
	status, err := c.do("POST", "/v1/boxes", req, &box)
	if err != nil {
		return nil, false, err
	}
	return &box, status == http.StatusCreated, nil
}

// ListBoxes returns all boxes plus any unreadable records.
func (c *Client) ListBoxes() (*api.ListResponse, error) {
	var list api.ListResponse
	if _, err := c.do("GET", "/v1/boxes", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// Box returns one box by ID.
func (c *Client) Box(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("GET", "/v1/boxes/"+id, nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// DestroyBox removes a box and its disk.
func (c *Client) DestroyBox(id string) error {
	_, err := c.do("DELETE", "/v1/boxes/"+id, nil, nil)
	return err
}
