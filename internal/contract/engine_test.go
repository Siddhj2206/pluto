package contract_test

import (
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// The `docker` node is the box's in-box container-engine capability (#149):
// absent or false means no engine; true means the box runs its own rootless
// engine, isolated from the host's; {privileged: true} opts into the elevated
// privileges a full in-box build needs.

func TestParseProjectDockerCapability(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		enabled    bool
		privileged bool
	}{
		{"absent", "version: 1\n", false, false},
		{"false", "version: 1\ndocker: false\n", false, false},
		{"true", "version: 1\ndocker: true\n", true, false},
		{"rootless", "version: 1\ndocker: {privileged: false}\n", true, false},
		{"privileged", "version: 1\ndocker: {privileged: true}\n", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := contract.ParseProject(tc.body)
			if err != nil {
				t.Fatalf("ParseProject: %v", err)
			}
			if p.Docker.Enabled != tc.enabled || p.Docker.Privileged != tc.privileged {
				t.Fatalf("docker = %+v, want enabled=%v privileged=%v", p.Docker, tc.enabled, tc.privileged)
			}
		})
	}
}

func TestParseProjectDockerNamesAnUnknownKey(t *testing.T) {
	_, err := contract.ParseProject("version: 1\ndocker: {priv: true}\n")
	if err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("err = %v, want the docker key named", err)
	}
}

func TestParseProjectDockerRejectsANonBool(t *testing.T) {
	if _, err := contract.ParseProject("version: 1\ndocker: \"yes\"\n"); err == nil {
		t.Fatal("docker: \"yes\" parsed, want an error")
	}
}
