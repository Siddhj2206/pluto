package container_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
)

// layerRecorder is a scriptable Run that records argv and captures the
// generated Dockerfile a build was pointed at.
type layerRecorder struct {
	calls      [][]string
	stdout     string
	inspectErr error
	dockerfile string
}

func (r *layerRecorder) run(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "image" {
		if r.inspectErr != nil {
			return nil, r.inspectErr
		}
	}
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			if data, err := os.ReadFile(args[i+1]); err == nil {
				r.dockerfile = string(data)
			}
		}
	}
	return []byte(r.stdout), nil
}

func (r *layerRecorder) last() string { return strings.Join(r.calls[len(r.calls)-1], " ") }

// TestCLIBuildBaseLayerFromDockerfile proves a Dockerfile-based base builds an
// image tagged by the layer's content address.
func TestCLIBuildBaseLayerFromDockerfile(t *testing.T) {
	rec := &layerRecorder{}
	eng := &container.CLI{Bin: "podman", Run: rec.run}

	ref, err := eng.BuildLayer(context.Background(), container.LayerSpec{
		Kind:   container.LayerBase,
		Digest: "abc123",
		Build:  &container.LayerBuild{Dockerfile: "/ctx/Dockerfile", Context: "/ctx"},
	})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if ref.Kind != container.LayerBase || ref.Key != "pluto-layer:abc123" {
		t.Fatalf("layer = %+v, want the digest tag", ref)
	}
	if got := rec.last(); got != "build -t pluto-layer:abc123 -f /ctx/Dockerfile /ctx" {
		t.Fatalf("build command = %q", got)
	}
}

// TestCLIBuildBaseLayerFromInlineRun proves inline steps build a generated
// Dockerfile FROM a default image.
func TestCLIBuildBaseLayerFromInlineRun(t *testing.T) {
	rec := &layerRecorder{}
	eng := &container.CLI{Bin: "podman", Run: rec.run}

	ref, err := eng.BuildLayer(context.Background(), container.LayerSpec{
		Kind:   container.LayerBase,
		Digest: "def456",
		Build:  &container.LayerBuild{From: "ubuntu:24.04", Run: []string{"apt-get update", "apt-get install -y curl"}},
	})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if ref.Key != "pluto-layer:def456" {
		t.Fatalf("layer = %+v", ref)
	}
	want := "FROM ubuntu:24.04\nRUN apt-get update\nRUN apt-get install -y curl\n"
	if rec.dockerfile != want {
		t.Fatalf("generated Dockerfile = %q, want %q", rec.dockerfile, want)
	}
	if !strings.HasPrefix(rec.last(), "build -t pluto-layer:def456 -f ") {
		t.Fatalf("build command = %q", rec.last())
	}
}

// TestCLIBuildPackageLayer proves a package layer builds FROM the base layer's
// key and is tagged by its own content address.
func TestCLIBuildPackageLayer(t *testing.T) {
	rec := &layerRecorder{}
	eng := &container.CLI{Bin: "docker", Run: rec.run}

	ref, err := eng.BuildLayer(context.Background(), container.LayerSpec{
		Kind:   container.LayerPackage,
		Parent: "pluto-layer:abc123",
		Digest: "pkg789",
		Build:  &container.LayerBuild{From: "pluto-layer:abc123", Run: []string{"apt-get update && apt-get install -y nodejs"}},
	})
	if err != nil {
		t.Fatalf("BuildLayer: %v", err)
	}
	if ref.Kind != container.LayerPackage || ref.Key != "pluto-layer:pkg789" {
		t.Fatalf("layer = %+v", ref)
	}
	want := "FROM pluto-layer:abc123\nRUN apt-get update && apt-get install -y nodejs\n"
	if rec.dockerfile != want {
		t.Fatalf("generated Dockerfile = %q, want %q", rec.dockerfile, want)
	}
}

// TestCLIResolveBuiltLayer proves a built layer resolves as a hit while its
// tagged image exists and as a miss when the engine does not have it.
func TestCLIResolveBuiltLayer(t *testing.T) {
	rec := &layerRecorder{stdout: "sha256:ignored\n"}
	eng := &container.CLI{Bin: "podman", Run: rec.run}

	ref, err := eng.ResolveLayer(context.Background(), container.LayerSpec{Kind: container.LayerPackage, Digest: "pkg789"})
	if err != nil {
		t.Fatalf("ResolveLayer hit: %v", err)
	}
	if ref.Key != "pluto-layer:pkg789" {
		t.Fatalf("resolved layer = %+v, want the digest tag", ref)
	}
	if got := rec.last(); got != "image inspect --format {{.Id}} pluto-layer:pkg789" {
		t.Fatalf("inspect command = %q", got)
	}

	miss := &layerRecorder{inspectErr: errors.New("no such image")}
	eng = &container.CLI{Bin: "podman", Run: miss.run}
	if _, err := eng.ResolveLayer(context.Background(), container.LayerSpec{Kind: container.LayerPackage, Digest: "pkg789"}); !errors.Is(err, container.ErrNoLayer) {
		t.Fatalf("ResolveLayer miss = %v, want ErrNoLayer", err)
	}
}

// TestCLIBuildLayerRequiresARecipe proves a built layer with no recipe is a
// clean unsupported-layer error rather than an empty build.
func TestCLIBuildLayerRequiresARecipe(t *testing.T) {
	rec := &layerRecorder{}
	eng := &container.CLI{Bin: "docker", Run: rec.run}
	if _, err := eng.BuildLayer(context.Background(), container.LayerSpec{Kind: container.LayerPackage, Digest: "x"}); !errors.Is(err, container.ErrUnsupportedLayer) {
		t.Fatalf("BuildLayer = %v, want ErrUnsupportedLayer", err)
	}
}
