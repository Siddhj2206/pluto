package daemon_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
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
	worktree := filepath.Join(dir, "repo")
	if err = os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte("[jobs.test]\ncommand = 'go test ./...'\n[events.push]\njob = 'test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	box, _, err := st.CreateBoxWithRepo("repo", "main", worktree, "https://github.com/acme/repo")
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	if err = srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	payload := `{"ref":"refs/heads/main","deleted":false}`
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
