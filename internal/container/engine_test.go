package container_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/container"
)

// The in-box container engine (#149) is a create-time capability. With no
// engine the container gets nothing. The rootless engine — the default — gets
// only the device a userspace overlay needs, and never the host engine's
// socket. The privileged escalation is the explicit opt-in, and even it never
// mounts the host's socket.

func TestCLICreateMapsEngineCapability(t *testing.T) {
	cases := []struct {
		name   string
		engine container.EngineCapability
		want   []string
		absent []string
	}{
		{
			name:   "no engine",
			engine: container.EngineCapability{},
			absent: []string{"--privileged", "/dev/fuse", "docker.sock", "podman.sock", "docker.socket", "podman.socket"},
		},
		{
			name:   "rootless engine",
			engine: container.EngineCapability{Enabled: true},
			want:   []string{"--device /dev/fuse"},
			absent: []string{"--privileged", "docker.sock", "podman.sock", "docker.socket", "podman.socket"},
		},
		{
			name:   "privileged escalation still isolated from the host",
			engine: container.EngineCapability{Enabled: true, Privileged: true},
			want:   []string{"--privileged"},
			absent: []string{"docker.sock", "podman.sock", "docker.socket", "podman.socket"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			eng := &container.CLI{Bin: "docker", Run: rec.run}
			if _, err := eng.Create(context.Background(), container.CreateSpec{
				BoxID: "box-1", Image: "ubuntu:24.04", Engine: tc.engine,
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			cmd := rec.last()
			for _, want := range tc.want {
				if !strings.Contains(cmd, want) {
					t.Fatalf("create = %q, missing %q", cmd, want)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(cmd, bad) {
					t.Fatalf("create = %q, must not contain %q", cmd, bad)
				}
			}
		})
	}
}
