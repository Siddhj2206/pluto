package cli_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
)

// TestEventsReadOneTracedManualRun drives the real CLI against a daemon with a
// fake runner: one `pluto run` must leave a request -> start -> done chain that
// `pluto events --trace` can read.
func TestEventsReadOneTracedManualRun(t *testing.T) {
	socket, _ := startDaemon(t)
	repo := gitRepo(t)
	writeJobContract(t, repo)

	if code, _, errOut := runCLI(t, "--socket", socket, "up", "--worktree", repo); code != 0 {
		t.Fatalf("up exit = %d, stderr: %s", code, errOut)
	}
	if code, _, errOut := runCLI(t, "--socket", socket, "run", repo, "test"); code != 0 {
		t.Fatalf("run exit = %d, stderr: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, "--socket", socket, "events", "--kind", "job.", "--json")
	if code != 0 {
		t.Fatalf("events exit = %d, stderr: %s", code, errOut)
	}
	var list api.EventListResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode events: %v (%s)", err, out)
	}
	if list.SchemaVersion != events.Schema {
		t.Fatalf("schema_version = %d, want %d", list.SchemaVersion, events.Schema)
	}
	if len(list.Events) != 3 {
		t.Fatalf("job events = %d (%s), want request, start, done", len(list.Events), out)
	}
	trace := list.Events[0].TraceID
	if trace == "" {
		t.Fatalf("events have no trace id: %s", out)
	}
	for _, e := range list.Events {
		if e.TraceID != trace {
			t.Fatalf("event %s trace = %q, want %q", e.Kind, e.TraceID, trace)
		}
	}

	// The human view is chronological and answers what, where, and when.
	code, out, errOut = runCLI(t, "--socket", socket, "events", "--trace", trace)
	if code != 0 {
		t.Fatalf("events --trace exit = %d, stderr: %s", code, errOut)
	}
	queued := strings.Index(out, events.KindJobQueued)
	started := strings.Index(out, events.KindJobStarted)
	done := strings.Index(out, events.KindJobDone)
	if queued < 0 || started < 0 || done < 0 {
		t.Fatalf("trace output is missing a hop:\n%s", out)
	}
	if !(queued < started && started < done) {
		t.Fatalf("trace output is not chronological:\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Fatalf("trace output has no where:\n%s", out)
	}

	// A worktree resolves to its box, and a duration bounds the read.
	code, out, _ = runCLI(t, "--socket", socket, "events", "--box", repo, "--since", "1h")
	if code != 0 {
		t.Fatalf("events --box exit = %d", code)
	}
	if !strings.Contains(out, events.KindBoxCreated) || !strings.Contains(out, events.KindJobDone) {
		t.Fatalf("events --box output = %q, want this worktree's events", out)
	}

	// Box and kind filters agree with the trace view.
	code, out, _ = runCLI(t, "--socket", socket, "events", "--kind", "job.", "--limit", "1", "--json")
	if code != 0 {
		t.Fatalf("events --limit exit = %d", code)
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode limited events: %v (%s)", err, out)
	}
	if len(list.Events) != 1 || list.Events[0].Kind != events.KindJobDone {
		t.Fatalf("limit = %+v, want only the newest event", list.Events)
	}
}

func TestEventsWithoutDaemonIsLegible(t *testing.T) {
	code, _, errOut := runCLI(t, "--socket", t.TempDir()+"/missing.sock", "events")
	if code == 0 {
		t.Fatal("events should fail without a daemon")
	}
	if !strings.Contains(errOut, "daemon") {
		t.Fatalf("stderr = %q, want a daemon hint", errOut)
	}
}

func TestEventsRejectsABadSince(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "events", "--since", "yesterday")
	if code != 2 {
		t.Fatalf("exit = %d, want a usage error", code)
	}
	if !strings.Contains(errOut, "since") {
		t.Fatalf("stderr = %q, want the since usage", errOut)
	}
}

// The help promises at most 1000 events; a larger --limit is a usage error,
// not a silently ignored bound.
func TestEventsEnforcesTheDocumentedLimit(t *testing.T) {
	socket, _ := startDaemon(t)
	code, _, errOut := runCLI(t, "--socket", socket, "events", "--limit", "1001")
	if code != 2 {
		t.Fatalf("exit = %d, want a usage error", code)
	}
	if !strings.Contains(errOut, "pluto:") || !strings.Contains(errOut, "at most 1000") {
		t.Fatalf("stderr = %q, want the documented maximum", errOut)
	}
}

// TestEventsFollowReadsTheStream drives --follow against a fake SSE daemon
// that sends one frame and closes: the CLI must print it and return cleanly.
func TestEventsFollowReadsTheStream(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "pluto.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"schema_version":1,"events":[]}`)
	})
	mux.HandleFunc("GET /v1/events/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"schema\":1,\"seq\":7,\"id\":\"evt_0000000000000007\",\"at\":\"2026-10-22T12:00:00Z\",\"kind\":\"job.done\",\"level\":\"info\",\"summary\":\"job 12ab done\",\"where\":{\"project\":\"cipher\",\"branch\":\"main\"}}\n\n")
		w.(http.Flusher).Flush()
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	code, out, errOut := runCLI(t, "--socket", socket, "events", "--follow")
	if code != 0 {
		t.Fatalf("follow exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "job.done") || !strings.Contains(out, "cipher/main") {
		t.Fatalf("follow output = %q, want the streamed event and its where", out)
	}

	code, out, errOut = runCLI(t, "--socket", socket, "events", "--follow", "--json")
	if code != 0 {
		t.Fatalf("follow --json exit = %d, stderr: %s", code, errOut)
	}
	var e events.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &e); err != nil {
		t.Fatalf("follow --json output is not one object: %v (%q)", err, out)
	}
	if e.Seq != 7 || e.Kind != events.KindJobDone {
		t.Fatalf("streamed JSON = %+v", e)
	}
}
