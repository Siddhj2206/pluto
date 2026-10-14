package container_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
)

// recorder is a scriptable Run: it records argv and replies per command.
type recorder struct {
	calls  [][]string
	stdout string
	err    error
}

func (r *recorder) run(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return []byte(r.stdout), r.err
}

func (r *recorder) last() string { return strings.Join(r.calls[len(r.calls)-1], " ") }

// TestCLICreateBuildsContainerCommand proves the real engine spells a box's
// create with its name, mounts, and a long-lived command.
func TestCLICreateBuildsContainerCommand(t *testing.T) {
	rec := &recorder{stdout: "sha256:abc\n"}
	eng := &container.CLI{Bin: "docker", Run: rec.run}

	id, err := eng.Create(context.Background(), container.CreateSpec{
		BoxID: "box-1",
		Image: "ubuntu:24.04",
		Volumes: []container.Mount{
			{Volume: container.VolumeRef{Kind: container.VolumeWorkspace, Name: "pluto-box-1-workspace"}, Target: "/workspace"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "sha256:abc" {
		t.Fatalf("id = %q, want the engine's printed id", id)
	}
	want := "create --name pluto-box-1 --mount type=volume,src=pluto-box-1-workspace,dst=/workspace ubuntu:24.04 sleep infinity"
	if got := rec.last(); got != want {
		t.Fatalf("create command = %q, want %q", got, want)
	}
}

// TestCLILifecycleCommands proves each lifecycle verb maps to its engine verb.
func TestCLILifecycleCommands(t *testing.T) {
	cases := []struct {
		verb string
		call func(eng *container.CLI) error
		want string
	}{
		{"start", func(e *container.CLI) error { return e.Start(context.Background(), "c1") }, "start c1"},
		{"stop", func(e *container.CLI) error { return e.Stop(context.Background(), "c1") }, "stop c1"},
		{"freeze", func(e *container.CLI) error { return e.Freeze(context.Background(), "c1") }, "pause c1"},
		{"thaw", func(e *container.CLI) error { return e.Thaw(context.Background(), "c1") }, "unpause c1"},
		{"destroy", func(e *container.CLI) error { return e.Destroy(context.Background(), "c1") }, "rm -f c1"},
	}
	for _, tc := range cases {
		rec := &recorder{}
		eng := &container.CLI{Bin: "docker", Run: rec.run}
		if err := tc.call(eng); err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
		if got := rec.last(); got != tc.want {
			t.Fatalf("%s command = %q, want %q", tc.verb, got, tc.want)
		}
	}
}

// TestCLIExecStreamsBytes proves exec passes the argv and streams the command's
// output to the caller's sink.
func TestCLIExecStreamsBytes(t *testing.T) {
	rec := &recorder{stdout: "hello from the box\n"}
	eng := &container.CLI{Bin: "docker", Run: rec.run}

	var sink bytes.Buffer
	res, err := eng.Exec(context.Background(), "c1", container.ExecSpec{Argv: []string{"echo", "hi"}, Dir: "/workspace", TTY: true, Env: map[string]string{"A": "1"}}, &sink)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if got := rec.last(); got != "exec -i -t -w /workspace -e A=1 c1 echo hi" {
		t.Fatalf("exec command = %q", got)
	}
	if sink.String() != "hello from the box\n" {
		t.Fatalf("sink = %q, want the streamed output", sink.String())
	}
}

// TestCLIStatusMapsEngineStates proves the observed engine state maps to the
// seam's vocabulary.
func TestCLIStatusMapsEngineStates(t *testing.T) {
	for _, tc := range []struct {
		engine string
		want   container.State
	}{
		{"running", container.StateRunning},
		{"paused", container.StateFrozen},
		{"created", container.StateCreated},
		{"exited", container.StateStopped},
	} {
		rec := &recorder{stdout: tc.engine + "\n"}
		eng := &container.CLI{Bin: "docker", Run: rec.run}
		st, err := eng.Status(context.Background(), "c1")
		if err != nil {
			t.Fatalf("Status(%s): %v", tc.engine, err)
		}
		if st.State != tc.want {
			t.Fatalf("Status(%s) = %s, want %s", tc.engine, st.State, tc.want)
		}
	}
}

// TestCLIBuildBaseLayerPulls proves a missing base layer is pulled and resolves
// to the image's content address.
func TestCLIBuildBaseLayerPulls(t *testing.T) {
	rec := &recorder{stdout: "sha256:deadbeef\n"}
	eng := &container.CLI{Bin: "podman", Run: rec.run}

	ref, err := eng.BuildLayer(context.Background(), container.LayerSpec{Kind: container.LayerBase, Source: "ubuntu:24.04"})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if ref.Kind != container.LayerBase || ref.Key != "sha256:deadbeef" {
		t.Fatalf("layer = %+v, want the pulled image id", ref)
	}
	if !strings.HasPrefix(rec.calls[0][0], "pull") {
		t.Fatalf("first call = %v, want a pull", rec.calls[0])
	}
}

// TestCLIVolumes proves volume ensure/remove map to engine volume verbs.
func TestCLIVolumes(t *testing.T) {
	rec := &recorder{}
	eng := &container.CLI{Bin: "docker", Run: rec.run}
	ref, err := eng.EnsureVolume(context.Background(), container.VolumeSpec{Kind: container.VolumeWorkspace, BoxID: "box-1"})
	if err != nil {
		t.Fatalf("EnsureVolume: %v", err)
	}
	if ref.Name != "pluto-box-1-workspace" {
		t.Fatalf("volume name = %q", ref.Name)
	}
	if err := eng.RemoveVolume(context.Background(), ref); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
	if got := strings.Join(rec.calls[0], " "); got != "volume create pluto-box-1-workspace" {
		t.Fatalf("ensure command = %q", got)
	}
	if got := strings.Join(rec.calls[1], " "); got != "volume rm pluto-box-1-workspace" {
		t.Fatalf("remove command = %q", got)
	}
}

// TestFakeExercisesSeam proves the controllable fake covers the whole seam:
// lifecycle, resolve-then-build, volumes, and a scripted byte stream.
func TestFakeExercisesSeam(t *testing.T) {
	ctx := context.Background()
	f := container.NewFake()

	// A missing layer resolves to ErrNoLayer, then builds to a stable key,
	// then resolves as a cache hit.
	if _, err := f.ResolveLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: "ubuntu:24.04"}); !errors.Is(err, container.ErrNoLayer) {
		t.Fatalf("ResolveLayer miss = %v, want ErrNoLayer", err)
	}
	ref, err := f.BuildLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: "ubuntu:24.04"})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if hit, err := f.ResolveLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: "ubuntu:24.04"}); err != nil || hit != ref {
		t.Fatalf("ResolveLayer hit = %+v %v, want %+v", hit, err, ref)
	}

	vol, err := f.EnsureVolume(ctx, container.VolumeSpec{Kind: container.VolumeHome, BoxID: "box-1"})
	if err != nil {
		t.Fatalf("EnsureVolume: %v", err)
	}
	id, err := f.Create(ctx, container.CreateSpec{BoxID: "box-1", Image: "ubuntu:24.04", Layers: []container.LayerRef{ref}, Volumes: []container.Mount{{Volume: vol, Target: "/home/pluto"}}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"start", func() error { return f.Start(ctx, id) }},
		{"freeze", func() error { return f.Freeze(ctx, id) }},
		{"thaw", func() error { return f.Thaw(ctx, id) }},
		{"stop", func() error { return f.Stop(ctx, id) }},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	if st, _ := f.State(id); st != container.StateStopped {
		t.Fatalf("state = %s, want stopped", st)
	}

	var sink bytes.Buffer
	res, err := f.Exec(ctx, id, container.ExecSpec{Argv: []string{"echo", "hi"}, TTY: true}, &sink)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 || sink.Len() != 0 {
		t.Fatalf("default exec = %+v %q", res, sink.String())
	}
	f.ExecFunc = func(_ string, _ container.ExecSpec, s io.Writer) (container.ExecResult, error) {
		_, _ = s.Write([]byte("streamed\n"))
		return container.ExecResult{ExitCode: 3}, nil
	}
	if res, err = f.Exec(ctx, id, container.ExecSpec{Argv: []string{"false"}}, &sink); err != nil || res.ExitCode != 3 {
		t.Fatalf("scripted exec = %+v %v", res, err)
	}
	if sink.String() != "streamed\n" {
		t.Fatalf("sink = %q", sink.String())
	}

	if err := f.Destroy(ctx, id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok := f.State(id); ok {
		t.Fatal("container still present after Destroy")
	}
	// Every seam verb left a recorded call.
	joined := strings.Join(f.Calls(), " ")
	for _, op := range []string{"create", "start", "freeze", "thaw", "stop", "exec", "resolve-layer", "build-layer", "ensure-volume", "destroy"} {
		if !strings.Contains(joined, op) {
			t.Fatalf("calls %q missing %q", joined, op)
		}
	}
}
