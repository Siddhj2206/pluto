package box_test

import (
	"context"
	"testing"

	"github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
)

// A project's `docker` node (#149) becomes the engine capability on the
// container the manager creates: absent means no engine, true means the box's
// own rootless engine, and {privileged: true} opts into the escalation.
func TestEnsureRequestsTheInBoxEngine(t *testing.T) {
	cases := []struct {
		name   string
		docker string
		want   container.EngineCapability
	}{
		{"unset has no engine", "", container.EngineCapability{}},
		{"docker true is rootless", "docker: true\n", container.EngineCapability{Enabled: true}},
		{"privileged escalation is opt-in", "docker: {privileged: true}\n", container.EngineCapability{Enabled: true, Privileged: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeTree(t, repo, map[string]string{"pluto.yml": "version: 1\n" + tc.docker})
			h := newWorkspaceHarness(t)
			if _, _, err := h.m.Ensure(context.Background(), box.CreateRequest{Workspace: repo}); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			spec, ok := h.engine.LastCreateSpec()
			if !ok {
				t.Fatal("engine never saw a create")
			}
			if spec.Engine != tc.want {
				t.Fatalf("engine capability = %+v, want %+v", spec.Engine, tc.want)
			}
		})
	}
}
