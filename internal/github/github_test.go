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

// limitServer answers the runs list, and counts requests that reached it and
// those answered with 304.
type limitServer struct {
	full, notModified int
	remaining         string
	status            int
	message           string
	retryAfter        string
}

func (s *limitServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.remaining != "" {
			w.Header().Set("X-RateLimit-Remaining", s.remaining)
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10))
		}
		if s.retryAfter != "" {
			w.Header().Set("Retry-After", s.retryAfter)
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": s.message})
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			s.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.full++
		w.Header().Set("ETag", `"v1"`)
		switch {
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]int64{{"id": 7}}})
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []Job{{ID: 1, Name: "check", Status: "queued"}}})
		case strings.HasSuffix(r.URL.Path, "/generate-jitconfig"):
			_ = json.NewEncoder(w).Encode(map[string]any{"runner": map[string]int64{"id": 3}, "encoded_jit_config": "x"})
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}
}

func TestRepeatPollsAreConditionalAndKeepTheirAnswer(t *testing.T) {
	s := &limitServer{}
	c := newTestClient(t, s.handler(t))
	c.Shared = NewShared()
	for range 2 {
		jobs, err := c.QueuedJobs(context.Background(), "o/r")
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 1 || jobs[0].Name != "check" { // Run 7 is under both statuses, and counted once.
			t.Fatalf("jobs = %+v", jobs)
		}
	}
	// The queued list, the in_progress list and run 7's jobs: three full answers,
	// then three free 304s.
	if s.full != 3 || s.notModified != 3 {
		t.Fatalf("full = %d, 304 = %d", s.full, s.notModified)
	}
}

func TestAnExhaustedTokenWaitsWithoutAsking(t *testing.T) {
	s := &limitServer{remaining: "0", status: http.StatusForbidden, message: "API rate limit exceeded"}
	c := newTestClient(t, s.handler(t))
	c.Shared = NewShared()
	_, err := c.QueuedJobs(context.Background(), "o/r")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Reserve || time.Until(rl.Until) < 25*time.Minute {
		t.Fatalf("err = %v", err)
	}
	s.status, s.remaining = 0, ""
	if _, _, err := c.CreateRunner(context.Background(), "o/r", "b", nil); !errors.As(err, &rl) {
		t.Fatalf("a blocked token reached GitHub: %v", err)
	}
	if s.full != 0 {
		t.Fatalf("%d requests reached GitHub", s.full)
	}
}

func TestPollingStopsAtTheReserveButRunnersStillRegister(t *testing.T) {
	s := &limitServer{remaining: "50"}
	c := newTestClient(t, s.handler(t))
	c.Shared = NewShared()
	if _, _, err := c.CreateRunner(context.Background(), "o/r", "b", nil); err != nil {
		t.Fatal(err)
	}
	_, err := c.QueuedJobs(context.Background(), "o/r")
	var rl *RateLimitError
	if !errors.As(err, &rl) || !rl.Reserve {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := c.CreateRunner(context.Background(), "o/r", "b", nil); err != nil {
		t.Fatalf("registering a runner within the reserve: %v", err)
	}
}

func TestSecondaryLimitsAndRetryAfterBlockTheToken(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		message    string
		retryAfter string
		atLeast    time.Duration
	}{
		{"secondary limit with no header waits a minute", http.StatusForbidden, "You have exceeded a secondary rate limit", "", 55 * time.Second},
		{"Retry-After is obeyed", http.StatusTooManyRequests, "slow down", "120", 115 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &limitServer{status: tt.status, message: tt.message, retryAfter: tt.retryAfter}
			c := newTestClient(t, s.handler(t))
			c.Shared = NewShared()
			_, err := c.QueuedJobs(context.Background(), "o/r")
			var rl *RateLimitError
			if !errors.As(err, &rl) || time.Until(rl.Until) < tt.atLeast {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAPermissionRefusalIsNotARateLimit(t *testing.T) {
	s := &limitServer{status: http.StatusForbidden, message: "Resource not accessible by personal access token"}
	c := newTestClient(t, s.handler(t))
	c.Shared = NewShared()
	_, err := c.QueuedJobs(context.Background(), "o/r")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if err := c.Shared.check("tok", true); err != nil {
		t.Fatalf("the token was blocked: %v", err)
	}
}

func TestCacheStaysBounded(t *testing.T) {
	s := NewShared()
	for i := range maxCached + 1 {
		s.store(fmt.Sprint(i), "e", nil)
	}
	if len(s.cache) > maxCached {
		t.Fatalf("cache has %d entries", len(s.cache))
	}
}

func TestAPollThatStartedFinishesBelowTheReserve(t *testing.T) {
	// Every answer says only 50 requests are left, but the first request found
	// the token above the reserve, so the poll must finish with its jobs.
	s := &limitServer{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		s.remaining = "50"
		s.handler(t)(w, r)
	})
	c.Shared = NewShared()
	jobs, err := c.QueuedJobs(context.Background(), "o/r")
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v, err = %v", jobs, err)
	}
	var rl *RateLimitError
	if _, err := c.QueuedJobs(context.Background(), "o/r"); !errors.As(err, &rl) || !rl.Reserve {
		t.Fatalf("the next poll should wait: %v", err)
	}
}

func TestAnAnswerThatDoesntDecodeIsntKept(t *testing.T) {
	bad := true
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("a bad answer was kept and replayed")
		}
		w.Header().Set("ETag", `"v1"`)
		if bad {
			_, _ = w.Write([]byte("{not json"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runners": []Runner{}})
	})
	c.Shared = NewShared()
	if _, err := c.Runners(context.Background(), "o/r"); err == nil {
		t.Fatal("bad JSON decoded")
	}
	bad = false
	if _, err := c.Runners(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
}
