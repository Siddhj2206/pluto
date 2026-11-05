package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Token: "tok", HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func TestQueuedJobsFollowsPagesAndKeepsOnlyQueued(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("missing token")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			var ids []map[string]int64
			if r.URL.Query().Get("status") == "queued" {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if page == 1 {
					for i := range 100 {
						ids = append(ids, map[string]int64{"id": int64(i + 1)})
					}
				} else if page == 2 {
					ids = append(ids, map[string]int64{"id": 1000}) // The oldest run.
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": ids})
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			status := "completed"
			if strings.Contains(r.URL.Path, "/1000/") {
				status = "queued"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []Job{{ID: 5, Name: "old", Status: status, Labels: []string{"pluto"}}}})
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	})
	jobs, err := c.QueuedJobs(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Name != "old" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestJobLogFindsEarlierAttempts(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			var jobs []Job
			if r.URL.Query().Get("filter") == "all" { // A re-run hides the first attempt otherwise.
				jobs = append(jobs, Job{ID: 7, Runner: "pluto-a"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs})
		case strings.HasSuffix(r.URL.Path, "/jobs/7/logs"):
			fmt.Fprint(w, "the log")
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	})
	data, err := c.JobLog(context.Background(), "o/r", 9, "pluto-a")
	if err != nil || string(data) != "the log" {
		t.Fatalf("%q %v", data, err)
	}
}

func TestCreateRunnerSendsCompleteLabels(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if got := fmt.Sprint(body["labels"]); !strings.Contains(got, "self-hosted") {
			t.Errorf("labels = %s", got)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"runner": map[string]any{"id": 3}, "encoded_jit_config": "cfg"})
	})
	id, cfg, err := c.CreateRunner(context.Background(), "o/r", "n", []string{"self-hosted", "pluto"})
	if err != nil || id != 3 || cfg != "cfg" {
		t.Fatalf("%d %q %v", id, cfg, err)
	}
}

func TestErrorsAreTypedAndDontLeakTheToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	if err := c.CheckAdmin(context.Background(), "o/r"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	c = newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"nope"}`)
	})
	err := c.CheckAdmin(context.Background(), "o/r")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 403 || strings.Contains(err.Error(), "tok") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckAdminRejectsATokenWithoutAdmin(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"permissions":{"admin":false}}`)
	})
	if err := c.CheckAdmin(context.Background(), "o/r"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("err = %v", err)
	}
}

func TestAGoneRunnerIsNotAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	if err := c.DeleteRunner(context.Background(), "o/r", 1); err != nil {
		t.Fatal(err)
	}
	if busy, err := c.RunnerBusy(context.Background(), "o/r", 1); err != nil || busy {
		t.Fatalf("%v %v", busy, err)
	}
}
