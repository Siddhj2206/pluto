package daemon_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

func listEvents(t *testing.T, c *http.Client, query string) []events.Event {
	t.Helper()
	resp, data := do(t, c, "GET", "/v1/events"+query, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/events%s status = %d, body %s", query, resp.StatusCode, data)
	}
	var out api.EventListResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode events: %v (%s)", err, data)
	}
	if out.SchemaVersion != events.Schema {
		t.Fatalf("schema_version = %d, want %d", out.SchemaVersion, events.Schema)
	}
	return out.Events
}

func runWithTrace(t *testing.T, c *http.Client, boxID, trace, job string) {
	t.Helper()
	body, _ := json.Marshal(api.RunRequest{Job: job, TraceID: trace})
	req, err := http.NewRequest("POST", "http://pluto/v1/boxes/"+boxID+"/run", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluto-Trace", trace)
	req.Header.Set("X-Pluto-Client", "laptop")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain run: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
}

func TestEventLedgerRecordsOneTracedManualRun(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{record: true})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	trace := "trc_0123456789abcdef"
	runWithTrace(t, c, box.ID, trace, "test")

	list := listEvents(t, c, "?trace="+trace)
	if len(list) != 3 {
		t.Fatalf("trace events = %d (%+v), want request, start, done", len(list), list)
	}
	// Newest first: done, started, queued.
	want := []string{events.KindJobDone, events.KindJobStarted, events.KindJobQueued}
	for i, kind := range want {
		if list[i].Kind != kind {
			t.Fatalf("event %d kind = %s, want %s", i, list[i].Kind, kind)
		}
		if list[i].TraceID != trace {
			t.Fatalf("event %d trace = %q, want %q", i, list[i].TraceID, trace)
		}
		if list[i].Where.Project == "" || list[i].Where.Branch != "main" {
			t.Fatalf("event %d where = %+v, want project and branch", i, list[i].Where)
		}
		if list[i].Actor.Client != "laptop" {
			t.Fatalf("event %d actor = %+v, want the CLI client", i, list[i].Actor)
		}
		if list[i].Where.Host == "" {
			t.Fatalf("event %d has no host", i)
		}
	}
	if list[0].ParentID != list[1].ID || list[1].ParentID != list[2].ID {
		t.Fatalf("parent chain = %q -> %q -> %q, want request -> start -> done",
			list[2].ID, list[1].ID, list[0].ID)
	}
	if list[0].DurationMS < 0 || list[1].Where.JobID == "" {
		t.Fatalf("job events lack job identity or duration: %+v", list)
	}

	// The job record carries the same correlation and trigger source.
	resp, data := do(t, c, "GET", "/v1/boxes/"+box.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var stored struct {
		Jobs []state.Job `json:"jobs"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode box: %v", err)
	}
	if len(stored.Jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(stored.Jobs))
	}
	job := stored.Jobs[0]
	if job.TraceID != trace || job.Source != "direct" || job.JobOrigin != "manual" {
		t.Fatalf("job correlation = trace %q source %q origin %q, want %q/direct/manual",
			job.TraceID, job.Source, job.JobOrigin, trace)
	}
	if job.ID != list[1].Where.JobID {
		t.Fatalf("job id %q does not match the event's where.job_id %q", job.ID, list[1].Where.JobID)
	}
}

func TestEventLedgerFiltersByBoxKindAndSince(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{record: true})
	c := client(socket)
	worktree := writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n")
	first := createBoxAt(t, c, worktree)
	second := createBoxAt(t, c, writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n"))

	runWithTrace(t, c, first.ID, "trc_0000000000000001", "test")

	all := listEvents(t, c, "")
	if len(all) < 3 {
		t.Fatalf("events = %d, want daemon start, box creates, and the run", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq >= all[i-1].Seq {
			t.Fatalf("sequence not strictly descending: %d then %d", all[i-1].Seq, all[i].Seq)
		}
	}

	byBox := listEvents(t, c, "?box="+second.ID)
	if len(byBox) != 1 || byBox[0].Kind != events.KindBoxCreated || byBox[0].Where.BoxID != second.ID {
		t.Fatalf("box filter = %+v, want the second box's create event", byBox)
	}

	byKind := listEvents(t, c, "?kind=job.")
	if len(byKind) != 3 {
		t.Fatalf("kind filter = %d events, want 3 job events", len(byKind))
	}
	for _, e := range byKind {
		if !strings.HasPrefix(e.Kind, "job.") {
			t.Fatalf("kind filter returned %q", e.Kind)
		}
	}

	limited := listEvents(t, c, "?limit=2")
	if len(limited) != 2 || limited[0].Seq != all[0].Seq {
		t.Fatalf("limit = %+v, want the two newest", limited)
	}

	after := listEvents(t, c, "?since="+strconv.FormatUint(all[0].Seq-1, 10))
	if len(after) != 1 || after[0].Seq != all[0].Seq {
		t.Fatalf("since = %+v, want only the newest event", after)
	}

	bad, data := do(t, c, "GET", "/v1/events?since=yesterday", nil)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad since status = %d, body %s", bad.StatusCode, data)
	}
}

func TestEventLedgerSurvivesRestartAndOrdersBySequence(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	socket := filepath.Join(dir, "pluto.sock")
	openServer := func() (*state.Store, *events.Store, *daemon.Server) {
		t.Helper()
		st, err := state.Open(stateDir)
		if err != nil {
			t.Fatalf("state.Open: %v", err)
		}
		ledger, err := events.OpenWithOptions(filepath.Join(dir, "events"), events.Options{})
		if err != nil {
			t.Fatalf("events.Open: %v", err)
		}
		fr := fakeRunner{st: st}
		srv := daemon.New(st, fr, "test")
		srv.Events = ledger
		if err := srv.Listen(socket); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		go srv.Serve()
		return st, ledger, srv
	}

	st, ledger, srv := openServer()
	c := client(socket)
	createBoxAt(t, c, writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n"))
	srv.Shutdown(context.Background())
	ledger.Close()
	st.Close()

	st2, ledger2, srv2 := openServer()
	t.Cleanup(func() {
		srv2.Shutdown(context.Background())
		ledger2.Close()
		st2.Close()
	})
	c = client(socket)
	createBoxAt(t, c, writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n"))

	list := listEvents(t, c, "?limit=100")
	if len(list) < 4 {
		t.Fatalf("events after restart = %d, want both runs and daemon boundaries", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Seq >= list[i-1].Seq {
			t.Fatalf("sequence after restart is not monotonic: %d then %d", list[i-1].Seq, list[i].Seq)
		}
	}
	if list[0].Kind != events.KindBoxCreated {
		t.Fatalf("newest event = %s, want the second box create", list[0].Kind)
	}
	// The pre-restart history is still readable, not overwritten.
	found := false
	for _, e := range list {
		if e.Kind == events.KindDaemonStopped {
			found = true
		}
	}
	if !found {
		t.Fatalf("pre-restart daemon.stopped event is missing from %+v", list)
	}
}

func TestEventLedgerStreamDeliversNewEvents(t *testing.T) {
	socket, _ := start(t)
	c := client(socket)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://pluto/v1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	got := make(chan events.Event, 4)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err == nil {
				got <- e
			}
		}
	}()

	// An empty since starts at the current head, so this create is the first
	// event the stream should deliver.
	createBoxAt(t, client(socket), writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n"))
	select {
	case e := <-got:
		if e.Kind != events.KindBoxCreated {
			t.Fatalf("streamed event = %+v, want box.created", e)
		}
		if e.Seq == 0 || e.ID == "" {
			t.Fatalf("streamed event lacks identity: %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for a streamed event")
	}
}

func TestEventLedgerRetentionBoundsTheDaemonRead(t *testing.T) {
	socket, _, srv := startServerWithLedger(t, fakeRunner{}, events.Options{SegmentBytes: 1, MaxEvents: 1})
	if srv.Events == nil {
		t.Fatal("server has no ledger")
	}
	c := client(socket)
	for i := 0; i < 5; i++ {
		createBoxAt(t, c, writeContract(t, t.TempDir(), "[jobs.test]\ncommand = \"true\"\n"))
	}
	list := listEvents(t, c, "?limit=100")
	if len(list) != 1 {
		t.Fatalf("retained %d events, want 1 (newest)", len(list))
	}
	if list[0].Kind != events.KindBoxCreated {
		t.Fatalf("retained event = %s, want the newest box.created", list[0].Kind)
	}
}

func TestEventLedgerNeverRecordsSecretsOrArgv(t *testing.T) {
	socket, _ := startWith(t, fakeRunner{record: true})
	c := client(socket)
	const secret = "s3cr3t-contract-value"
	worktree := writeContract(t, t.TempDir(), "[env]\nAPI_TOKEN = \""+secret+"\"\n\n[jobs.test]\ncommand = \"true\"\n")
	box := createBoxAt(t, c, worktree)

	// An ad-hoc argv can contain a user's token; the ledger names the request
	// but never copies the argv.
	const argvSecret = "ghp_argv_secret_value"
	body, _ := json.Marshal(api.RunRequest{Argv: []string{"curl", "-H", "Authorization: Bearer " + argvSecret}})
	resp, data := do(t, c, "POST", "/v1/boxes/"+box.ID+"/run", json.RawMessage(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status = %d, body %s", resp.StatusCode, data)
	}

	eventsDir := filepath.Join(filepath.Dir(socket), "events")
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var ledgerText strings.Builder
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(eventsDir, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		ledgerText.Write(b)
	}
	if strings.Contains(ledgerText.String(), secret) {
		t.Fatalf("ledger contains a contract environment value: %s", ledgerText.String())
	}
	if strings.Contains(ledgerText.String(), argvSecret) {
		t.Fatalf("ledger contains an ad-hoc argv value: %s", ledgerText.String())
	}
	if ledgerText.Len() == 0 {
		t.Fatal("ledger is empty; the redaction check proved nothing")
	}
}
