package cli_test

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/state"
	"github.com/Siddhj2206/pluto/internal/vcs"
)

// attachFrameTimeout bounds a wait for a terminal frame. It guards against a
// hang; ordering is carried by the fake engine's own channels.
const attachFrameTimeout = 3 * time.Second

// frameWriter forwards each attach output chunk to a channel so a test can read
// the stream while it is still live.
type frameWriter struct{ frames chan []byte }

func (w *frameWriter) Write(p []byte) (int, error) {
	w.frames <- append([]byte(nil), p...)
	return len(p), nil
}

// startAttach runs one client's attach in the background and returns the output
// frames and a channel carrying the stream's final error.
func startAttach(t *testing.T, c *client.Client, id string, watch bool, in io.Reader) (<-chan []byte, <-chan error) {
	t.Helper()
	frames := make(chan []byte, 16)
	done := make(chan error, 1)
	go func() {
		_, err := c.AttachTerminal(id, watch, in, &frameWriter{frames: frames})
		done <- err
	}()
	return frames, done
}

// awaitFrame waits for one attach frame and asserts its content.
func awaitFrame(t *testing.T, frames <-chan []byte, want string) {
	t.Helper()
	select {
	case got := <-frames:
		if string(got) != want {
			t.Fatalf("attach frame = %q, want %q", got, want)
		}
	case <-time.After(attachFrameTimeout):
		t.Fatalf("timed out waiting for attach frame %q", want)
	}
}

// awaitAttachDone waits for an attach stream to end without error.
func awaitAttachDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach stream failed: %v", err)
		}
	case <-time.After(attachFrameTimeout):
		t.Fatal("timed out waiting for the attach stream to end")
	}
}

// createTerminalBox creates a workspace's box through the real CLI and returns
// the socket, store, and box record.
func createTerminalBox(t *testing.T, engine container.Engine, repo string) (string, *state.Store, *state.Box) {
	t.Helper()
	adapter := &vcs.Fake{Workspace: vcs.Workspace{Path: repo}, Revision: vcs.Revision{Commit: "abc"}}
	socket, st := startBoxDaemon(t, engine, adapter)
	t.Chdir(repo)
	if code, _, errOut := runCLI(t, "--socket", socket, "box", "create"); code != 0 {
		t.Fatalf("box create exit = %d, stderr: %s", code, errOut)
	}
	return socket, st, boxRecord(t, st, repo)
}

// TestWatchStreamsAndMasksBoxTerminal proves `pluto attach --watch` streams a
// box terminal's output read-only, with declared secrets masked.
func TestWatchStreamsAndMasksBoxTerminal(t *testing.T) {
	const secret = "host-secret-value"
	t.Setenv("REGISTRY_TOKEN", secret)

	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\nsecrets:\n  REGISTRY_TOKEN: { env: REGISTRY_TOKEN }\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, _ container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = io.WriteString(sink, "token "+secret+"\n")
		return container.ExecResult{ExitCode: 0}, nil
	}
	socket, _, box := createTerminalBox(t, engine, repo)

	code, out, errOut := runCLI(t, "--socket", socket, "attach", "--watch", box.ID)
	if code != 0 {
		t.Fatalf("attach --watch exit = %d, stderr: %s", code, errOut)
	}
	assertMasked(t, "watch stream", out, secret)
}

// TestAttachStreamsBoxTerminal proves the interactive form streams the box
// terminal's output through the real CLI.
func TestAttachStreamsBoxTerminal(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, _ container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = io.WriteString(sink, "hello from the box\n")
		return container.ExecResult{ExitCode: 0}, nil
	}
	socket, _, box := createTerminalBox(t, engine, repo)

	code, out, errOut := runCLI(t, "--socket", socket, "attach", box.ID)
	if code != 0 {
		t.Fatalf("attach exit = %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "hello from the box") {
		t.Fatalf("attach output = %q, want the terminal's output", out)
	}
}

// TestAttachFansOutAndReplaysScrollback proves several clients see the same
// output and a later client is replayed the scrollback it missed.
func TestAttachFansOutAndReplaysScrollback(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	release := make(chan struct{})
	engine.ExecFunc = func(_ string, _ container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		_, _ = io.WriteString(sink, "early output\n")
		<-release
		return container.ExecResult{ExitCode: 0}, nil
	}
	socket, _, box := createTerminalBox(t, engine, repo)
	c := client.New(socket)

	firstFrames, firstDone := startAttach(t, c, box.ID, false, strings.NewReader(""))
	awaitFrame(t, firstFrames, "early output\n")

	secondFrames, secondDone := startAttach(t, c, box.ID, true, nil)
	awaitFrame(t, secondFrames, "early output\n")

	close(release)
	awaitAttachDone(t, firstDone)
	awaitAttachDone(t, secondDone)
}

// TestAttachKeystrokesReachTheBox proves typed input from a client reaches the
// box's terminal.
func TestAttachKeystrokesReachTheBox(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  dev: { run: \"echo hi\" }\n")
	engine := container.NewFake()
	engine.ExecFunc = func(_ string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		buf := make([]byte, 64)
		n, _ := spec.Stdin.Read(buf)
		_, _ = io.WriteString(sink, "echo:"+string(buf[:n]))
		return container.ExecResult{ExitCode: 0}, nil
	}
	socket, _, box := createTerminalBox(t, engine, repo)

	frames, done := startAttach(t, client.New(socket), box.ID, false, strings.NewReader("ping\n"))
	awaitFrame(t, frames, "echo:ping\n")
	awaitAttachDone(t, done)
}

// TestAttachHoldsTheBoxSharedUntilItEnds proves an attached client is a shared
// holder: an exclusive run waits until the attach ends.
func TestAttachHoldsTheBoxSharedUntilItEnds(t *testing.T) {
	repo := gitRepo(t)
	writePlutoYML(t, repo, "version: 1\ntasks:\n  batch: { run: [\"batch\"], lock: exclusive }\n")
	engine := container.NewFake()
	terminalRelease := make(chan struct{})
	batchEntered := make(chan struct{})
	var once sync.Once
	engine.ExecFunc = func(_ string, spec container.ExecSpec, sink io.Writer) (container.ExecResult, error) {
		if len(spec.Argv) > 0 && spec.Argv[0] == "batch" {
			once.Do(func() { close(batchEntered) })
			return container.ExecResult{ExitCode: 0}, nil
		}
		_, _ = io.WriteString(sink, "attached\n")
		<-terminalRelease
		return container.ExecResult{ExitCode: 0}, nil
	}
	socket, st, box := createTerminalBox(t, engine, repo)

	frames, attachDone := startAttach(t, client.New(socket), box.ID, false, strings.NewReader(""))
	awaitFrame(t, frames, "attached\n")

	exclusive := runCLIAsync(t, "--socket", socket, "run", "batch")
	awaitRun(t, st, "batch")
	requireNotEntered(t, batchEntered, "the exclusive run while an attach held the box")

	close(terminalRelease)
	awaitEngine(t, batchEntered, "the exclusive run after the attach ended")
	if res := <-exclusive; res.code != 0 {
		t.Fatalf("exclusive run exit = %d, stderr: %s", res.code, res.err)
	}
	awaitAttachDone(t, attachDone)
}
