package state_test

import (
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

// A branch (or local) box is trusted: it is the user's own worktree. A
// pull-request work-item box defaults to untrusted until a trusted policy
// labels it; an issue box starts from the trusted default branch.
func TestBoxTrustClassDefaults(t *testing.T) {
	st := openStore(t, t.TempDir())
	branch, _, err := st.CreateBox("app", "main", "/src/app")
	if err != nil {
		t.Fatalf("CreateBox: %v", err)
	}
	if branch.TrustClass != state.TrustClassTrusted {
		t.Fatalf("branch trust class = %q, want %q", branch.TrustClass, state.TrustClassTrusted)
	}
	pr, _, err := st.CreateWorkItemBox("app", "https://example.test/app.git", "pull_request", "7", "sha7", "/src/app-pr-7")
	if err != nil {
		t.Fatalf("CreateWorkItemBox(pr): %v", err)
	}
	if pr.TrustClass != state.TrustClassUntrusted {
		t.Fatalf("pull request trust class = %q, want %q", pr.TrustClass, state.TrustClassUntrusted)
	}
	issue, _, err := st.CreateWorkItemBox("app", "https://example.test/app.git", "issue", "9", "sha9", "/src/app-issue-9")
	if err != nil {
		t.Fatalf("CreateWorkItemBox(issue): %v", err)
	}
	if issue.TrustClass != state.TrustClassTrusted {
		t.Fatalf("issue trust class = %q, want %q", issue.TrustClass, state.TrustClassTrusted)
	}
}

func TestSetTrustClassPersistsAndValidates(t *testing.T) {
	st := openStore(t, t.TempDir())
	pr, _, err := st.CreateWorkItemBox("app", "https://example.test/app.git", "pull_request", "7", "sha7", "/src/app-pr-7")
	if err != nil {
		t.Fatalf("CreateWorkItemBox: %v", err)
	}
	updated, err := st.SetTrustClass(pr.ID, state.TrustClassTrusted)
	if err != nil {
		t.Fatalf("SetTrustClass: %v", err)
	}
	if updated.TrustClass != state.TrustClassTrusted {
		t.Fatalf("trust class = %q, want trusted", updated.TrustClass)
	}
	got, err := st.Box(pr.ID)
	if err != nil {
		t.Fatalf("Box: %v", err)
	}
	if got.TrustClass != state.TrustClassTrusted {
		t.Fatalf("persisted trust class = %q, want trusted", got.TrustClass)
	}
	if _, err := st.SetTrustClass(pr.ID, "root"); err == nil {
		t.Fatal("SetTrustClass accepted an unknown class")
	}
}
