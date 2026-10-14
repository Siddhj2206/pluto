package terminal_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/mask"
	"github.com/Siddhj2206/pluto/internal/terminal"
)

// frameTimeout bounds a wait for a terminal frame. It is a failure guard, not
// a synchronisation delay: every test is ordered by a channel of its own.
const frameTimeout = 2 * time.Second

// readFrame waits for one output frame, failing the test if none arrives.
func readFrame(t *testing.T, sub *terminal.Subscription) string {
	t.Helper()
	select {
	case frame, ok := <-sub.Output():
		if !ok {
			t.Fatal("terminal stream ended before a frame arrived")
		}
		return string(frame)
	case <-time.After(frameTimeout):
		t.Fatal("timed out waiting for a terminal frame")
		return ""
	}
}

// drain reads every frame until the stream ends and returns the joined output.
func drain(t *testing.T, sub *terminal.Subscription) string {
	t.Helper()
	var b strings.Builder
	for frame := range sub.Output() {
		b.Write(frame)
	}
	return b.String()
}

// TestAttachReplaysScrollbackToALaterJoiner proves the daemon's bounded
// scrollback is replayed to a client that attaches after output was produced.
func TestAttachReplaysScrollbackToALaterJoiner(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	release := make(chan struct{})
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		_, _ = io.WriteString(slave, "early output\n")
		<-release
		return 0, nil
	}

	_, first, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer first.Close()
	if got := readFrame(t, first); got != "early output\n" {
		t.Fatalf("first frame = %q, want the shell's output", got)
	}

	replay, second, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	defer second.Close()
	if string(replay) != "early output\n" {
		t.Fatalf("replay = %q, want the retained scrollback", replay)
	}
	close(release)
}

// TestAttachFansOutputToEveryClient proves several clients attached to one box
// all see the same output.
func TestAttachFansOutputToEveryClient(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	lines := make(chan string)
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		for line := range lines {
			_, _ = io.WriteString(slave, line)
		}
		return 0, nil
	}

	_, first, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_, second, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer first.Close()
	defer second.Close()

	lines <- "shared output\n"
	for i, sub := range []*terminal.Subscription{first, second} {
		if got := readFrame(t, sub); got != "shared output\n" {
			t.Fatalf("client %d frame = %q, want the shared output", i+1, got)
		}
	}
	close(lines)
}

// TestAttachAnyClientMayType proves input from any attached client reaches the
// box's terminal, not just the first one.
func TestAttachAnyClientMayType(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	typed := make(chan string, 1)
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		buf := make([]byte, 64)
		n, _ := slave.Read(buf)
		typed <- string(buf[:n])
		return 0, nil
	}

	_, first, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	_, second, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer first.Close()
	defer second.Close()

	if _, err := second.Write([]byte("ls -la\n")); err != nil {
		t.Fatalf("second client write: %v", err)
	}
	select {
	case got := <-typed:
		if got != "ls -la\n" {
			t.Fatalf("terminal read = %q, want the second client's keystrokes", got)
		}
	case <-time.After(frameTimeout):
		t.Fatal("the terminal never saw the second client's input")
	}
}

// TestWatchCannotType proves a watch subscription is read-only.
func TestWatchCannotType(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	release := make(chan struct{})
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		<-release
		return 0, nil
	}

	_, sub, err := hub.Attach("box-1", start, nil, true)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer sub.Close()
	if _, err := sub.Write([]byte("nope")); err == nil {
		t.Fatal("a watch subscription accepted input")
	}
	close(release)
}

// TestAttachMasksOutput proves the session's stream is masked before it reaches
// a subscriber, even when a secret straddles two output chunks.
func TestAttachMasksOutput(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	masker := mask.New("s3cr3t-value")
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		_, _ = io.WriteString(slave, "token s3cr3t-")
		_, _ = io.WriteString(slave, "value done\n")
		return 0, nil
	}

	_, sub, err := hub.Attach("box-1", start, masker, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer sub.Close()
	got := drain(t, sub)
	if strings.Contains(got, "s3cr3t-value") {
		t.Fatalf("stream leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("stream = %q, want a redaction", got)
	}
}

// TestScrollbackIsBounded proves the scrollback keeps only the most recent
// bytes when more than its bound is produced.
func TestScrollbackIsBounded(t *testing.T) {
	hub := terminal.NewHubWithScrollback(terminal.PipeOpener, 8)
	release := make(chan struct{})
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		_, _ = io.WriteString(slave, "0123456789AB")
		<-release
		return 0, nil
	}

	_, first, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer first.Close()
	if got := readFrame(t, first); got != "0123456789AB" {
		t.Fatalf("first frame = %q", got)
	}

	replay, second, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	defer second.Close()
	if string(replay) != "456789AB" {
		t.Fatalf("replay = %q, want only the last 8 bytes", replay)
	}
	close(release)
}

// TestAttachReportsExitCode proves a stream ends when the terminal process
// exits and reports that process's code to the client.
func TestAttachReportsExitCode(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		_, _ = io.WriteString(slave, "bye\n")
		return 7, nil
	}

	_, sub, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer sub.Close()
	if got := drain(t, sub); got != "bye\n" {
		t.Fatalf("stream = %q, want the final output", got)
	}
	if code := sub.ExitCode(); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// awaitStart waits for the terminal process to start, failing if it never does.
func awaitStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(frameTimeout):
		t.Fatal("the terminal process never started")
	}
}

// TestCloseEndsTheTerminal proves stopping a box ends its terminal, and a later
// attach starts a fresh one.
func TestCloseEndsTheTerminal(t *testing.T) {
	hub := terminal.NewHub(terminal.PipeOpener)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	start := func(_ context.Context, slave io.ReadWriteCloser) (int, error) {
		started <- struct{}{}
		<-release
		return 0, nil
	}

	_, first, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	awaitStart(t, started)

	hub.Close("box-1")
	select {
	case _, ok := <-first.Output():
		if ok {
			t.Fatal("the client stream delivered a frame after Close")
		}
	case <-time.After(frameTimeout):
		t.Fatal("closing the terminal did not end the client stream")
	}

	_, second, err := hub.Attach("box-1", start, nil, false)
	if err != nil {
		t.Fatalf("re-Attach: %v", err)
	}
	defer second.Close()
	awaitStart(t, started)
	close(release)
}
