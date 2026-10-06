package daemon_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/state"
)

func TestGitHubPushIsVerifiedDeduplicatedAndQueued(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	// The checked-out branch attempts to select a different job. The default
	// branch policy must remain authoritative for event admission.
	git(t, "-C", worktree, "checkout", "-b", "feature")
	if err = os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[jobs.test]\ncommand = 'true'\n[jobs.attacker]\ncommand = 'false'\n[events.push]\njob = 'attacker'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "untrusted policy")
	git(t, "-C", worktree, "push", "origin", "feature")
	box, _, err := st.CreateBoxWithRepo("repo", "feature", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	if err = srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	payload := `{"ref":"refs/heads/feature","deleted":false}`
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
		req.SetPathValue("source", "source")
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "delivery-1")
		req.Header.Set("X-Hub-Signature-256", sig)
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		if rec.Code != 202 {
			t.Fatalf("response %d: %s", rec.Code, rec.Body.String())
		}
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	if items[0].BoxID != box.ID || items[0].Job != "test" || items[0].EventID != "delivery-1" {
		t.Fatalf("queued item=%+v", items[0])
	}
}

func webhookGitRepo(t *testing.T, dir string) (string, string) {
	t.Helper()
	remote := filepath.Join(dir, "remote.git")
	worktree := filepath.Join(dir, "repo")
	git(t, "init", "--bare", "--initial-branch=main", remote)
	git(t, "clone", remote, worktree)
	contract := "[jobs.test]\ncommand = 'true'\n[jobs.attacker]\ncommand = 'false'\n[events.push]\njob = 'test'\n"
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(contract), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "trusted policy")
	git(t, "-C", worktree, "push", "origin", "main")
	return remote, worktree
}

func git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestGitHubPushRejectsInvalidSignature(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _, err := st.CreateBox("repo", "main", dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	if err = srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/github/source", strings.NewReader(`{}`))
	req.SetPathValue("source", "source")
	req.Header.Set("X-Hub-Signature-256", "sha256=00")
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("status=%d", rec.Code)
	}
	items, _ := st.Queue()
	if len(items) != 0 {
		t.Fatalf("invalid signature queued work: %+v", items)
	}
}

func TestUnsupportedGitHubEventIsLoggedAndAcknowledged(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _, err := st.CreateBox("repo", "main", dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	if err := srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	var logged string
	srv.Logf = func(format string, args ...any) { logged = fmt.Sprintf(format, args...) }
	body := `{"action":"opened"}`
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/github/source", strings.NewReader(body))
	req.SetPathValue("source", "source")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logged, `unsupported GitHub event type "pull_request"`) {
		t.Fatalf("log=%q", logged)
	}
	items, err := st.Queue()
	if err != nil || len(items) != 0 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
}
