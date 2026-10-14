//go:build container

// Opt-in smoke tests for the in-box container engine (#149). They are
// excluded from the default run, which fakes the engine:
//
//	go test -tags container ./internal/container -run TestSmokeEngine -v
//
// They use PLUTO_CONTAINER_ENGINE (default: the first of podman/docker on
// PATH) and PLUTO_SMOKE_IMAGE (default ubuntu:24.04). The nested-engine test
// is a second opt-in via PLUTO_SMOKE_ENGINE_IMAGE, because the image must
// carry a working rootless engine and the host must permit nested user
// namespaces.
package container_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
)

// TestSmokeEngineCreateFlags asserts the create-time isolation this ticket
// owns: a rootless engine box is not privileged and never mounts the host
// engine's socket; the escalation is privileged but still isolated from the
// host engine.
func TestSmokeEngineCreateFlags(t *testing.T) {
	bin := smokeBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	eng := container.NewCLI(bin)

	cases := []struct {
		name       string
		engine     container.EngineCapability
		privileged string
	}{
		{"rootless", container.EngineCapability{Enabled: true}, "false"},
		{"privileged", container.EngineCapability{Enabled: true, Privileged: true}, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := eng.Create(ctx, container.CreateSpec{
				BoxID: "smoke-engine-" + tc.name, Image: smokeImage(), Engine: tc.engine,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			defer eng.Destroy(ctx, id)

			out, err := exec.CommandContext(ctx, bin, "inspect", "--format", "{{.HostConfig.Privileged}}", id).CombinedOutput()
			if err != nil {
				t.Fatalf("inspect: %v (%s)", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.privileged {
				t.Fatalf("privileged = %q, want %q", got, tc.privileged)
			}
			full, err := exec.CommandContext(ctx, bin, "inspect", id).CombinedOutput()
			if err != nil {
				t.Fatalf("inspect full: %v", err)
			}
			for _, sock := range []string{"docker.sock", "podman.sock", "docker.socket", "podman.socket"} {
				if strings.Contains(string(full), sock) {
					t.Fatalf("container mounts the host engine socket %q", sock)
				}
			}
		})
	}
}

// TestSmokeRootlessEngineBuilds proves a rootless engine inside the box can
// build an image. It is a second opt-in because it depends on the image
// carrying a working rootless engine and on the host allowing nested user
// namespaces.
func TestSmokeRootlessEngineBuilds(t *testing.T) {
	image := os.Getenv("PLUTO_SMOKE_ENGINE_IMAGE")
	if image == "" {
		t.Skip("set PLUTO_SMOKE_ENGINE_IMAGE to a rootless-engine image to run")
	}
	bin := smokeBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	eng := container.NewCLI(bin)

	id, err := eng.Create(ctx, container.CreateSpec{
		BoxID: "smoke-nested", Image: image, Engine: container.EngineCapability{Enabled: true},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer eng.Destroy(ctx, id)
	if err := eng.Start(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}

	var out strings.Builder
	res, err := eng.Exec(ctx, id, container.ExecSpec{Argv: []string{
		"sh", "-c", "if command -v podman >/dev/null 2>&1; then podman info; else docker info; fi",
	}}, &out)
	if err != nil {
		t.Fatalf("exec nested engine: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("nested engine exited %d: %s", res.ExitCode, out.String())
	}
	if !strings.Contains(out.String(), "rootless") {
		t.Fatalf("nested engine output does not report rootless: %s", out.String())
	}
}

func smokeBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("PLUTO_CONTAINER_ENGINE")
	if bin == "" {
		bin = container.DefaultBinary()
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("container engine %q not found: %v", bin, err)
	}
	return bin
}

func smokeImage() string {
	if img := os.Getenv("PLUTO_SMOKE_IMAGE"); img != "" {
		return img
	}
	return "ubuntu:24.04"
}
