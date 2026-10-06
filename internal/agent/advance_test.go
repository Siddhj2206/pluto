package agent

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdvanceWorktreeBlocksDirtyGuestAndFastForwardsCleanGuest(t *testing.T) {
	source := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "-b", "main", source).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	gitRun(t, source, "config", "user.name", "Test")
	gitRun(t, source, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "add", ".")
	gitRun(t, source, "commit", "-m", "first")
	guest := filepath.Join(t.TempDir(), "guest")
	if out, err := exec.Command("git", "clone", "-q", source, guest).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, source, "add", ".")
	gitRun(t, source, "commit", "-m", "second")
	sha := gitRun(t, source, "rev-parse", "HEAD")
	bundle := filepath.Join(t.TempDir(), "advance.bundle")
	gitRun(t, source, "bundle", "create", bundle, "--all")
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := New(t.TempDir(), &fakeSystem{})
	if err != nil {
		t.Fatal(err)
	}
	ag.status.Synced = true
	ag.status.Worktree = guest
	if err := os.WriteFile(filepath.Join(guest, "agent-work.txt"), []byte("keep this local work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = ag.advanceWorktree(bufio.NewReader(bytes.NewReader(data)), Request{Worktree: guest, Ref: sha, Bytes: info.Size()})
	if err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty guest update err=%v", err)
	}
	if got := gitRun(t, guest, "rev-parse", "HEAD"); got == sha {
		t.Fatal("dirty worktree advanced unexpectedly")
	}
	if err := os.Remove(filepath.Join(guest, "agent-work.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := ag.advanceWorktree(bufio.NewReader(bytes.NewReader(data)), Request{Worktree: guest, Ref: sha, Bytes: info.Size()})
	if err != nil {
		t.Fatalf("clean advance: %v", err)
	}
	if got != sha || gitRun(t, guest, "rev-parse", "HEAD") != sha {
		t.Fatalf("advanced ref=%q head=%q want %q", got, gitRun(t, guest, "rev-parse", "HEAD"), sha)
	}
	if body, err := os.ReadFile(filepath.Join(guest, "tracked.txt")); err != nil || string(body) != "two\n" {
		t.Fatalf("advanced file=%q err=%v", body, err)
	}
}
