package state_test

import (
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestTrackedRemotePrefersOrigin(t *testing.T) {
	remotes := []state.Remote{
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
		{Name: "origin", Fetch: "https://example.com/me/app.git"},
		{Name: "fork", Fetch: "git@example.com:me/app.git"},
	}
	got, ok := state.TrackedRemote(remotes)
	if !ok || got.Name != "origin" {
		t.Fatalf("TrackedRemote = %q, %v; want origin, true", got.Name, ok)
	}
}

func TestTrackedRemoteUsesTheSoleRemote(t *testing.T) {
	got, ok := state.TrackedRemote([]state.Remote{{Name: "fork", Fetch: "https://example.com/me/app.git"}})
	if !ok || got.Name != "fork" {
		t.Fatalf("TrackedRemote = %q, %v; want fork, true", got.Name, ok)
	}
}

func TestTrackedRemoteAmbiguousWithoutOrigin(t *testing.T) {
	remotes := []state.Remote{
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
		{Name: "fork", Fetch: "https://example.com/me/app.git"},
	}
	if got, ok := state.TrackedRemote(remotes); ok {
		t.Fatalf("TrackedRemote = %q, true; want no tracked remote when several remotes lack an origin", got.Name)
	}
}

func TestTrackedRemoteNoRemotes(t *testing.T) {
	if got, ok := state.TrackedRemote(nil); ok {
		t.Fatalf("TrackedRemote = %q, true; want false when the box is local-only", got.Name)
	}
}

func TestRemoteSSHDetection(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"ssh://git@example.com/acme/app.git", true},
		{"git@example.com:acme/app.git", true},
		{"git@github.com:acme/app.git", true},
		{"https://example.com/acme/app.git", false},
		{"http://example.com/acme/app.git", false},
		{"git://example.com/acme/app.git", false},
		{"/srv/git/app.git", false},
		{"../relative/app.git", false},
	}
	for _, tc := range cases {
		if got := (state.Remote{Fetch: tc.url}).IsSSH(); got != tc.want {
			t.Errorf("IsSSH(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestSetRemotesPersistsTheMirroredList(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	box, _, err := st.CreateBox("alpha", "main", "/src/alpha")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}

	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "fork", Fetch: "git@example.com:me/app.git", Push: []string{"ssh://git@example.com/me/app.git"}},
	}
	updated, err := st.SetRemotes(box.ID, remotes)
	if err != nil {
		t.Fatalf("SetRemotes: %v", err)
	}
	if len(updated.Remotes) != 2 || updated.Remotes[1].Name != "fork" {
		t.Fatalf("Remotes = %+v, want the mirrored list", updated.Remotes)
	}
	read, err := st.Box(box.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if len(read.Remotes) != 2 || read.Remotes[0].Fetch != "https://example.com/acme/app.git" {
		t.Fatalf("persisted Remotes = %+v, want the list to survive a re-read", read.Remotes)
	}
}
