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
	"strconv"

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
	// Session carries the daemon's fact that an attach named an undeclared
	// session.
	Session bool
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
// daemon's contract and session facts.
func httpError(resp *http.Response, data []byte) *HTTPError {
	var apiErr api.Error
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != "" {
		return &HTTPError{Status: resp.StatusCode, Message: apiErr.Error, Contract: apiErr.Contract, Session: apiErr.Session}
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

// QueueRequest durably accepts asynchronous explicit work.
func (c *Client) QueueRequest(id string, req api.QueueRequest) (*state.QueueItem, error) {
	var out api.QueueResponse
	_, err := c.do("POST", "/v1/boxes/"+id+"/queue", req, &out)
	if err != nil {
		return nil, err
	}
	return &out.Item, nil
}

// Queue returns durable queue history, including rejected requests.
func (c *Client) Queue() ([]state.QueueItem, error) {
	var out api.QueueListResponse
	_, err := c.do("GET", "/v1/queue", nil, &out)
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

// CreateTask durably accepts a task and its first run.
func (c *Client) CreateTask(req api.TaskRequest) (*state.Task, error) {
	var out api.TaskResponse
	if _, err := c.do("POST", "/v1/tasks", req, &out); err != nil {
		return nil, err
	}
	return &out.Task, nil
}

// Tasks returns durable task history.
func (c *Client) Tasks() ([]state.Task, error) {
	var out api.TaskListResponse
	if _, err := c.do("GET", "/v1/tasks", nil, &out); err != nil {
		return nil, err
	}
	return out.Tasks, nil
}

// Task returns one task and its run history.
func (c *Client) Task(id string) (*state.Task, error) {
	var out api.TaskResponse
	if _, err := c.do("GET", "/v1/tasks/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out.Task, nil
}

// CreateTaskRun durably appends a follow-up run to a task.
func (c *Client) CreateTaskRun(id string, req api.TaskRunRequest) (*state.TaskRun, error) {
	var out api.TaskRunResponse
	if _, err := c.do("POST", "/v1/tasks/"+url.PathEscape(id)+"/runs", req, &out); err != nil {
		return nil, err
	}
	return &out.Run, nil
}

// PostCommit submits a local post-commit event through the daemon's durable
// event queue.
func (c *Client) PostCommit(event api.PostCommitEvent) error {
	_, err := c.do("POST", "/v1/events", event, nil)
	return err
}

// Box returns one box by ID.
func (c *Client) Box(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("GET", "/v1/boxes/"+id, nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// UpBox ensures the box is running and returns its updated record.
func (c *Client) UpBox(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("POST", "/v1/boxes/"+id+"/up", nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// PauseBox stops the box cleanly and returns its updated record.
func (c *Client) PauseBox(id string) (*state.Box, error) {
	var box state.Box
	if _, err := c.do("POST", "/v1/boxes/"+id+"/pause", nil, &box); err != nil {
		return nil, err
	}
	return &box, nil
}

// AttachBox ensures the box is running and returns its ssh connection details
// for a plain shell (session empty) or a declared session.
func (c *Client) AttachBox(id, session string) (*api.AttachInfo, error) {
	var info api.AttachInfo
	if _, err := c.do("POST", "/v1/boxes/"+id+"/attach", api.AttachRequest{Session: session}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ConnectService wakes a box if needed, waits for the declared service to be
// active, and returns the selected service route and client instructions.
func (c *Client) ConnectService(id string, req api.ConnectRequest) (*api.ConnectResponse, error) {
	var out api.ConnectResponse
	if _, err := c.do("POST", "/v1/boxes/"+id+"/connect", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImportImage installs a built artifact and returns its version.
func (c *Client) ImportImage(dir string) (string, error) {
	var resp api.ImportImageResponse
	if _, err := c.do("POST", "/v1/images", api.ImportImageRequest{Path: dir}, &resp); err != nil {
		return "", err
	}
	return resp.Version, nil
}

// ListImages returns the imported image versions.
func (c *Client) ListImages() ([]api.ImageInfo, error) {
	var resp api.ImagesResponse
	if _, err := c.do("GET", "/v1/images", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Images, nil
}

// Logs returns a box's phase log or service journal.
func (c *Client) Logs(id, phase, service string, lines int) (string, error) {
	query := url.Values{}
	if phase != "" {
		query.Set("phase", phase)
	}
	if service != "" {
		query.Set("service", service)
	}
	if lines > 0 {
		query.Set("lines", strconv.Itoa(lines))
	}
	var resp api.LogsResponse
	if _, err := c.do("GET", "/v1/boxes/"+id+"/logs?"+query.Encode(), nil, &resp); err != nil {
		return "", err
	}
	return resp.Log, nil
}

// JobLog returns a job's recorded output.
func (c *Client) JobLog(id, jobID string, lines int) (string, error) {
	query := url.Values{}
	query.Set("job", jobID)
	if lines > 0 {
		query.Set("lines", strconv.Itoa(lines))
	}
	var resp api.LogsResponse
	if _, err := c.do("GET", "/v1/boxes/"+id+"/logs?"+query.Encode(), nil, &resp); err != nil {
		return "", err
	}
	return resp.Log, nil
}

// RunJob runs a declared job or an ad-hoc command in a box, streaming its
// output to stdout, and returns the recorded outcome. The daemon keeps the
// run going even if this client goes away.
func (c *Client) RunJob(id string, run api.RunRequest, stdout io.Writer) (*state.Job, error) {
	body, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", c.base+"/v1/boxes/"+id+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
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
				return nil, errors.New("job stream ended without a result")
			}
			return nil, fmt.Errorf("decode job event: %w", err)
		}
		switch event.Type {
		case api.RunOutput:
			if _, err := stdout.Write(event.Data); err != nil {
				return nil, err
			}
		case api.RunExit:
			if event.Job == nil {
				return nil, errors.New("job stream ended without a result")
			}
			return event.Job, nil
		case api.RunError:
			return nil, errors.New(event.Error)
		default:
			return nil, fmt.Errorf("unknown job event %q", event.Type)
		}
	}
}

// DestroyBox removes a box and its disk.
func (c *Client) DestroyBox(id string) error {
	_, err := c.do("DELETE", "/v1/boxes/"+id, nil, nil)
	return err
}
