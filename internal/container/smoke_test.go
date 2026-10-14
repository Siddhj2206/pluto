//go:build container

// The real-container-engine smoke test. It is excluded from the default test
// run, which fakes every seam; opt in explicitly:
//
//	go test -tags container ./internal/container -run TestSmokeCreatesContainerAndRunsCommand -v
//
// It uses PLUTO_CONTAINER_ENGINE (default: the first of podman/docker on PATH)
// and PLUTO_SMOKE_IMAGE (default ubuntu:24.04). It creates a box, runs a
// command inside it, and tears the box and its volume down.
package container_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
)

// TestSmokeBuildsALayerFromProvision exercises the real engine's built-layer
// path: a package layer is built on top of a base layer, tagged by its content
// address, resolved as a hit, and run in a container.
func TestSmokeBuildsALayerFromProvision(t *testing.T) {
	bin := os.Getenv("PLUTO_CONTAINER_ENGINE")
	if bin == "" {
		bin = container.DefaultBinary()
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("container engine %q not found: %v", bin, err)
	}
	image := os.Getenv("PLUTO_SMOKE_IMAGE")
	if image == "" {
		image = "ubuntu:24.04"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	eng := container.NewCLI(bin)

	base, err := eng.BuildLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: image})
	if err != nil {
		t.Fatalf("build base layer %q: %v", image, err)
	}

	digest := "pluto-smoke-layer"
	spec := container.LayerSpec{
		Kind:   container.LayerPackage,
		Parent: base.Key,
		Digest: digest,
		Build:  &container.LayerBuild{From: base.Key, Run: []string{"echo pluto-layer-smoke > /pluto-layer-smoke"}},
	}
	pkg, err := eng.BuildLayer(ctx, spec)
	if err != nil {
		t.Fatalf("build package layer: %v", err)
	}
	if pkg.Key != "pluto-layer:"+digest {
		t.Fatalf("package key = %q, want the digest tag", pkg.Key)
	}
	if hit, err := eng.ResolveLayer(ctx, spec); err != nil || hit != pkg {
		t.Fatalf("resolve built layer = %+v %v, want %+v", hit, err, pkg)
	}
	defer exec.Command(bin, "rmi", "-f", pkg.Key).Run()

	id, err := eng.Create(ctx, container.CreateSpec{BoxID: "smoke-layer", Image: pkg.Key})
	if err != nil {
		t.Fatalf("create container from built layer: %v", err)
	}
	defer eng.Destroy(ctx, id)
	if err := eng.Start(ctx, id); err != nil {
		t.Fatalf("start container: %v", err)
	}
	var out bytes.Buffer
	res, err := eng.Exec(ctx, id, container.ExecSpec{Argv: []string{"cat", "/pluto-layer-smoke"}}, &out)
	if err != nil {
		t.Fatalf("exec in container: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(out.String(), "pluto-layer-smoke") {
		t.Fatalf("exec = %+v, output %q, want the built layer's content", res, out.String())
	}
}

func TestSmokeCreatesContainerAndRunsCommand(t *testing.T) {
	bin := os.Getenv("PLUTO_CONTAINER_ENGINE")
	if bin == "" {
		bin = container.DefaultBinary()
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("container engine %q not found: %v", bin, err)
	}
	image := os.Getenv("PLUTO_SMOKE_IMAGE")
	if image == "" {
		image = "ubuntu:24.04"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	eng := container.NewCLI(bin)

	layer, err := eng.ResolveLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: image})
	if errors.Is(err, container.ErrNoLayer) {
		layer, err = eng.BuildLayer(ctx, container.LayerSpec{Kind: container.LayerBase, Source: image})
	}
	if err != nil {
		t.Fatalf("resolve/build base layer %q: %v", image, err)
	}

	volume, err := eng.EnsureVolume(ctx, container.VolumeSpec{Kind: container.VolumeWorkspace, BoxID: "smoke"})
	if err != nil {
		t.Fatalf("ensure volume: %v", err)
	}

	id, err := eng.Create(ctx, container.CreateSpec{
		BoxID:   "smoke",
		Image:   image,
		Layers:  []container.LayerRef{layer},
		Volumes: []container.Mount{{Volume: volume, Target: "/workspace"}},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer func() {
		if err := eng.Destroy(ctx, id); err != nil {
			t.Errorf("destroy container: %v", err)
		}
		if err := eng.RemoveVolume(ctx, volume); err != nil {
			t.Errorf("remove volume: %v", err)
		}
	}()

	if err := eng.Start(ctx, id); err != nil {
		t.Fatalf("start container: %v", err)
	}

	var out bytes.Buffer
	res, err := eng.Exec(ctx, id, container.ExecSpec{Argv: []string{"sh", "-c", "echo pluto-smoke && pwd"}}, &out)
	if err != nil {
		t.Fatalf("exec in container: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exec exit = %d, output: %s", res.ExitCode, out.String())
	}
	if !strings.Contains(out.String(), "pluto-smoke") {
		t.Fatalf("exec output = %q, want the command's stream", out.String())
	}

	status, err := eng.Status(ctx, id)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State != container.StateRunning {
		t.Fatalf("state = %q, want running", status.State)
	}
}
