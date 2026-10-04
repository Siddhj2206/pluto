package agent

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

func startServer(t *testing.T, ag *Agent) *Client {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go ag.Serve(ln)
	return &Client{dial: func() (net.Conn, error) { return net.Dial("unix", socket) }}
}

func TestServerClientRoundTrip(t *testing.T) {
	ag, err := New(t.TempDir(), newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := startServer(t, ag)

	if err := c.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := c.Apply(testContract(t), "/home/dev/work/x"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	waitFor(t, "provision done", func() bool {
		st, err := c.Status()
		return err == nil && st.Provision.State == state.PhaseDone
	})
	log, err := c.Logs("provision", "", 10)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !strings.Contains(log, "=== provision") {
		t.Fatalf("provision log = %q, want the agent's header", log)
	}
}

func TestSyncStreamsBundle(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := startServer(t, ag)

	bundle := filepath.Join(t.TempDir(), "repo.bundle")
	if err := os.WriteFile(bundle, []byte("bundle-bytes"), 0o644); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if err := c.Sync(bundle, "/home/dev/work/x", "master"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sys.clones) != 1 || sys.cloneData == nil || string(sys.cloneData) != "bundle-bytes" {
		t.Fatalf("clone = %v, data = %q", sys.clones, sys.cloneData)
	}
	if !ag.Status().Synced {
		t.Fatal("agent should report synced")
	}
}

func TestServerRejectsBadRequests(t *testing.T) {
	ag, err := New(t.TempDir(), newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := startServer(t, ag)

	if _, err := c.call(Request{Op: "apply"}, nil, time.Second); err == nil {
		t.Fatal("apply without a contract should fail")
	}
	if _, err := c.call(Request{Op: "bogus"}, nil, time.Second); err == nil {
		t.Fatal("unknown op should fail")
	}
}

func TestServerClientRunStreams(t *testing.T) {
	sys := newFakeSystem()
	sys.jobChunks = []string{"hello\n", "world\n"}
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := startServer(t, ag)

	var got []byte
	job, err := c.Run(state.NewID(), []string{"echo", "hi"}, "/home/dev/work/x", func(data []byte) {
		got = append(got, data...)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(got) != "hello\nworld\n" {
		t.Fatalf("streamed = %q, want both chunks", got)
	}
	if job == nil || job.State != state.JobDone || job.Command != "echo hi" {
		t.Fatalf("job = %+v, want done/echo hi", job)
	}

	reported, err := c.JobStatus()
	if err != nil {
		t.Fatalf("JobStatus: %v", err)
	}
	if reported == nil || reported.ID != job.ID || reported.State != state.JobDone {
		t.Fatalf("agent job = %+v, want %s done", reported, job.ID)
	}

	log, err := c.JobLog(job.ID, 10)
	if err != nil {
		t.Fatalf("JobLog: %v", err)
	}
	if !strings.Contains(log, "hello") || !strings.Contains(log, "world") {
		t.Fatalf("job log = %q, want the job's output", log)
	}
}

func TestServerClientRunRejectsBadRequest(t *testing.T) {
	ag, err := New(t.TempDir(), newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := startServer(t, ag)
	if _, err := c.Run(state.NewID(), nil, "/home/dev/work/x", func([]byte) {}); err == nil {
		t.Fatal("Run without argv should fail")
	}
}

func TestClientCallIsBoundedWhenAgentStalls(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "stall.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn // accepted but never answered
	}()
	t.Cleanup(func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	})

	c := &Client{
		dial:        func() (net.Conn, error) { return net.Dial("unix", socket) },
		callTimeout: 200 * time.Millisecond,
	}
	start := time.Now()
	if _, err := c.Status(); err == nil {
		t.Fatal("Status should time out against a stalled agent")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("client call took %s, want it bounded by the call timeout", elapsed)
	}
}
