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
	"net/url"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// ErrUnreachable reports that the daemon could not be reached.
var ErrUnreachable = errors.New("cannot reach the pluto daemon")

// ErrNotFound reports a box that does not exist.
var ErrNotFound = errors.New("box not found")

// HTTPError is a daemon response that failed with an HTTP status. The CLI
// reads the status and the contract fact to pick the hint it prints.
type HTTPError struct {
	Status  int
	Message string
	// Contract carries the daemon's fact that a contract load failed.
	Contract bool
}

func (e *HTTPError) Error() string { return e.Message }

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
		return resp.StatusCode, httpError(resp, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// httpError builds the typed failure from a failed response, keeping the
// daemon's contract fact.
func httpError(resp *http.Response, data []byte) *HTTPError {
	var apiErr api.Error
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
		return &HTTPError{Status: resp.StatusCode, Message: apiErr.Error, Contract: apiErr.Contract}
	}
	return &HTTPError{Status: resp.StatusCode, Message: fmt.Sprintf("daemon returned %s", resp.Status)}
}

// Health reports daemon liveness.
func (c *Client) Health() (*api.Health, error) {
	var h api.Health
	if _, err := c.do("GET", "/v1/health", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Providers returns the host's configured provider lifecycle and safe status.
func (c *Client) Providers() ([]api.ProviderStatus, error) {
	var out api.ProviderListResponse
	if _, err := c.do("GET", "/v1/providers", nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// ProviderAction performs one owner-approved provider lifecycle action.
func (c *Client) ProviderAction(id, action string, approved bool) (*api.ProviderStatus, error) {
	method := "POST"
	path := "/v1/providers/" + url.PathEscape(id) + "/" + action
	if action == "remove" {
		method = "DELETE"
		path = "/v1/providers/" + url.PathEscape(id)
	}
	var out api.ProviderStatus
	if _, err := c.do(method, path, api.ProviderApprovalRequest{Approved: approved}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AttachTerminal opens a box's shared terminal and streams its output to out,
// returning the terminal process's exit code. With watch set the connection is
// read-only and in is ignored; otherwise in supplies keystrokes.
func (c *Client) AttachTerminal(id string, watch bool, in io.Reader, out io.Writer) (int, error) {
	var body io.Reader = http.NoBody
	if !watch && in != nil {
		pr, pw := io.Pipe()
		go func() {
			_, err := io.Copy(pw, in)
			_ = pw.CloseWithError(err)
		}()
		body = pr
	}
	target := c.base + "/v1/box/" + url.PathEscape(id) + "/attach"
	if watch {
		target += "?watch=true"
	}
	req, err := http.NewRequest("POST", target, body)
	if err != nil {
		return 0, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w at %s: %v", ErrUnreachable, c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(resp.Body)
		return 0, httpError(resp, data)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var event api.TerminalEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("terminal stream ended without a result")
			}
			return 0, fmt.Errorf("decode terminal event: %w", err)
		}
		switch event.Type {
		case api.TerminalOutput:
			if out != nil {
				if _, err := out.Write(event.Data); err != nil {
					return 0, err
				}
			}
		case api.TerminalExit:
			return event.ExitCode, nil
		case api.TerminalError:
			return 1, errors.New(event.Error)
		default:
			return 0, fmt.Errorf("unknown terminal event %q", event.Type)
		}
	}
}

// RunTask runs a named task in its workspace's durable box, streaming the
// step's output to stdout, and returns the recorded run. The daemon keeps the
// run going even if this client goes away.
func (c *Client) RunTask(req api.RunTaskRequest, stdout io.Writer) (*state.Run, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("POST", c.base+"/v1/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrUnreachable, c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(resp.Body)
		return nil, httpError(resp, data)
	}

	dec := json.NewDecoder(resp.Body)
	for {
		var event api.RunEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("run stream ended without a result")
			}
			return nil, fmt.Errorf("decode run event: %w", err)
		}
		switch event.Type {
		case api.RunOutput:
			if _, err := stdout.Write(event.Data); err != nil {
				return nil, err
			}
		case api.RunExit:
			if event.Run == nil {
				return nil, errors.New("run stream ended without a result")
			}
			return event.Run, nil
		case api.RunError:
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unknown run event %q", event.Type)
		}
	}
}

// ListRuns returns the host's recorded runs, newest first.
func (c *Client) ListRuns() (*api.RunListResponse, error) {
	var list api.RunListResponse
	if _, err := c.do("GET", "/v1/run", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// Run returns one recorded run by full id or an unambiguous id prefix.
func (c *Client) Run(id string) (*state.Run, error) {
	var run state.Run
	if _, err := c.do("GET", "/v1/run/"+url.PathEscape(id), nil, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// RunLogs returns a run's retained per-step output.
func (c *Client) RunLogs(id string) (*api.RunLogsResponse, error) {
	var resp api.RunLogsResponse
	if _, err := c.do("GET", "/v1/run/"+url.PathEscape(id)+"/logs", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateContainerBox resolves a workspace through the vcs adapter to its one
// container box, creating and starting it on first use. The bool reports
// whether a new box was created.
func (c *Client) CreateContainerBox(req api.CreateContainerBoxRequest) (*state.Box, bool, error) {
	var box state.Box
	status, err := c.do("POST", "/v1/box", req, &box)
	if err != nil {
		return nil, false, err
	}
	return &box, status == http.StatusCreated, nil
}

// ListContainerBoxes returns all boxes.
func (c *Client) ListContainerBoxes() (*api.ListResponse, error) {
	var list api.ListResponse
	if _, err := c.do("GET", "/v1/box", nil, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// ContainerBox returns one box by ID.
func (c *Client) ContainerBox(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("GET", "/v1/box/"+url.PathEscape(id), nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// StartContainerBox starts a stopped box.
func (c *Client) StartContainerBox(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("POST", "/v1/box/"+url.PathEscape(id)+"/start", nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// StopContainerBox stops a box, keeping its record and volumes.
func (c *Client) StopContainerBox(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("POST", "/v1/box/"+url.PathEscape(id)+"/stop", nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// RemoveContainerBox destroys a box, its container, and its durable volumes.
func (c *Client) RemoveContainerBox(id string) error {
	_, err := c.do("DELETE", "/v1/box/"+url.PathEscape(id), nil, nil)
	return err
}
