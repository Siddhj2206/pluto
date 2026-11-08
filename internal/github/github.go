// Package github is the small part of GitHub's REST API that Pluto needs.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is GitHub's public API.
const DefaultBaseURL = "https://api.github.com"

// ErrUnauthorized means GitHub rejected the token.
var ErrUnauthorized = errors.New("GitHub rejected the token")

// Job is a CI job waiting for a runner.
type Job struct {
	ID     int64    `json:"id"`
	RunID  int64    `json:"run_id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
	URL    string   `json:"html_url"`
	Runner string   `json:"runner_name"`
}

// Runner is a registered runner.
type Runner struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Busy bool   `json:"busy"`
}

// maxPages bounds listing calls. A repo with more queued work than this is
// already beyond what one host can serve.
const maxPages = 5

// API is what the host needs from GitHub.
type API interface {
	CheckAdmin(ctx context.Context, repo string) error
	QueuedJobs(ctx context.Context, repo string) ([]Job, error)
	CreateRunner(ctx context.Context, repo, name string, labels []string) (id int64, jitConfig string, err error)
	DeleteRunner(ctx context.Context, repo string, id int64) error
	Runners(ctx context.Context, repo string) ([]Runner, error)
	RunnerBusy(ctx context.Context, repo string, id int64) (bool, error)
	JobLog(ctx context.Context, repo string, runID int64, runner string) ([]byte, error)
}

// Client talks to GitHub with one token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// Shared, if set, makes repeat requests conditional and tracks the token's
	// rate limit across clients.
	Shared *Shared
}

// New returns a client for the public API.
func New(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.request(ctx, method, path, body, out, false)
}

// request is do, and polling marks a request that can wait when the token is
// nearly out of requests.
func (c *Client) request(ctx context.Context, method, path string, body, out any, polling bool) error {
	if c.Shared != nil {
		if err := c.Shared.check(c.Token, polling); err != nil {
			return err
		}
	}
	// Raw downloads such as logs are large and asked for once, so only decoded
	// GET answers are kept for conditional requests.
	_, raw := out.(*[]byte)
	key, etag := "", ""
	var stored []byte
	if c.Shared != nil && method == http.MethodGet && !raw {
		key = c.Token + " " + path
		etag, stored = c.Shared.lookup(key)
	}
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "pluto")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if c.Shared != nil {
		if err := c.Shared.observe(c.Token, resp, apiMessage(data)); err != nil {
			return err
		}
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && stored != nil:
		data = stored // Free: a 304 doesn't count against the limit.
	case resp.StatusCode >= 300:
		return &StatusError{Code: resp.StatusCode, Path: path, Message: apiMessage(data)}
	case key != "" && resp.Header.Get("ETag") != "":
		c.Shared.store(key, resp.Header.Get("ETag"), data)
	}
	if b, ok := out.(*[]byte); ok {
		*b = data
		return nil
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// StatusError is an unexpected HTTP status from GitHub.
type StatusError struct {
	Code    int
	Path    string
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub returned %d for %s: %s", e.Code, e.Path, e.Message)
}

func apiMessage(data []byte) string {
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &v) == nil && v.Message != "" {
		return v.Message
	}
	return strings.TrimSpace(string(data))
}

// CheckAdmin confirms the token can manage runners on the repo.
func (c *Client) CheckAdmin(ctx context.Context, repo string) error {
	var v struct {
		Permissions struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repo, nil, &v); err != nil {
		return err
	}
	if !v.Permissions.Admin {
		return fmt.Errorf("this token doesn't have admin access to %s, which GitHub requires to register runners", repo)
	}
	return nil
}

// QueuedJobs lists jobs waiting for a runner. A run that's already in progress
// can still have jobs waiting, so both run states are checked.
func (c *Client) QueuedJobs(ctx context.Context, repo string) ([]Job, error) {
	var jobs []Job
	seen := map[int64]bool{}
	for _, status := range []string{"queued", "in_progress"} {
		for page := 1; page <= maxPages; page++ {
			var runs struct {
				Runs []struct {
					ID int64 `json:"id"`
				} `json:"workflow_runs"`
			}
			q := url.Values{"status": {status}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
			if err := c.request(ctx, http.MethodGet, "/repos/"+repo+"/actions/runs?"+q.Encode(), nil, &runs, true); err != nil {
				return nil, err
			}
			for _, run := range runs.Runs {
				if seen[run.ID] {
					continue
				}
				seen[run.ID] = true
				js, err := c.runJobs(ctx, repo, run.ID, "latest", true)
				if err != nil {
					return nil, err
				}
				for _, j := range js {
					if j.Status == "queued" {
						jobs = append(jobs, j)
					}
				}
			}
			if len(runs.Runs) < 100 {
				break
			}
		}
	}
	return jobs, nil
}

// runJobs lists a run's jobs. The filter is latest for the current attempt, or
// all to include earlier attempts of a re-run workflow.
func (c *Client) runJobs(ctx context.Context, repo string, runID int64, filter string, polling bool) ([]Job, error) {
	var jobs []Job
	for page := 1; page <= maxPages; page++ {
		var v struct {
			Jobs []Job `json:"jobs"`
		}
		path := fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?filter=%s&per_page=100&page=%d", repo, runID, filter, page)
		if err := c.request(ctx, http.MethodGet, path, nil, &v, polling); err != nil {
			return nil, err
		}
		jobs = append(jobs, v.Jobs...)
		if len(v.Jobs) < 100 {
			break
		}
	}
	return jobs, nil
}

// CreateRunner registers a one-time runner. Labels must be complete: GitHub
// adds no default labels to these runners, not even self-hosted.
func (c *Client) CreateRunner(ctx context.Context, repo, name string, labels []string) (int64, string, error) {
	body := map[string]any{"name": name, "runner_group_id": 1, "labels": labels, "work_folder": "_work"}
	var v struct {
		Runner struct {
			ID int64 `json:"id"`
		} `json:"runner"`
		Config string `json:"encoded_jit_config"`
	}
	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/actions/runners/generate-jitconfig", body, &v); err != nil {
		return 0, "", err
	}
	return v.Runner.ID, v.Config, nil
}

// DeleteRunner removes a registration. One that's already gone is fine, because
// GitHub removes one-time runners by itself after their job.
func (c *Client) DeleteRunner(ctx context.Context, repo string, id int64) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/actions/runners/%d", repo, id), nil, nil)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return nil
	}
	return err
}

// Runners lists the repo's registered runners.
func (c *Client) Runners(ctx context.Context, repo string) ([]Runner, error) {
	var out []Runner
	for page := 1; page <= maxPages; page++ {
		var v struct {
			Runners []Runner `json:"runners"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runners?per_page=100&page=%d", repo, page), nil, &v); err != nil {
			return nil, err
		}
		out = append(out, v.Runners...)
		if len(v.Runners) < 100 {
			break
		}
	}
	return out, nil
}

// RunnerBusy reports whether GitHub has given the runner a job. A box learns
// this seconds later, when its pre-job hook runs.
func (c *Client) RunnerBusy(ctx context.Context, repo string, id int64) (bool, error) {
	var r Runner
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runners/%d", repo, id), nil, &r)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return false, nil
	}
	return r.Busy, err
}

// JobLog fetches the log of the job that ran on the named runner.
func (c *Client) JobLog(ctx context.Context, repo string, runID int64, runner string) ([]byte, error) {
	jobs, err := c.runJobs(ctx, repo, runID, "all", false)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Runner == runner {
			var data []byte
			err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, j.ID), nil, &data)
			return data, err
		}
	}
	return nil, fmt.Errorf("GitHub has no job for runner %s in run %d", runner, runID)
}
