package daemon_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	plutoclient "github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/contract"
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
	featureHead, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"ref":"refs/heads/feature","after":%q,"deleted":false}`, strings.TrimSpace(string(featureHead)))
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

func TestWebhookHandlerDoesNotExposePlutoProviderOrControlAPI(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	for _, path := range []string{"/v1/providers", "/v1/health", "/v1/boxes"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s over webhook handler returned %d", path, rec.Code)
		}
	}
}

func TestGitHubPushProvidesContextAndUsesTriggeringContract(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	box, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	sender := filepath.Join(dir, "sender")
	git(t, "clone", remote, sender)
	if err := os.WriteFile(filepath.Join(sender, ".pluto.toml"), []byte("[jobs.test]\ncommand = ['echo', 'from-push']\n[events.push]\njob = 'test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", sender, "add", ".pluto.toml")
	git(t, "-C", sender, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "updated job")
	git(t, "-C", sender, "push", "origin", "main")
	after, err := exec.Command("git", "-C", sender, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	const repositoryURL = "https://github.com/example/project"
	payload := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q,"deleted":false,"repository":{"html_url":%q}}`, strings.TrimSpace(string(after)), repositoryURL)
	fired := make(chan contract.Exec, 1)
	srv := daemon.New(st, fakeRunner{st: st, fired: fired}, "test")
	if err = srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(payload))
	req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
	req.SetPathValue("source", "source")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "push-delivery-42")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("response %d: %s", rec.Code, rec.Body.String())
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	item := items[0]
	if item.Event.Kind != "push" || item.Event.Repo != remote || item.Event.Ref != strings.TrimSpace(string(after)) || item.Event.HeadRef != "refs/heads/main" || item.Event.ObjectID != "push-delivery-42" || item.Event.URL != repositoryURL {
		t.Fatalf("push event context=%+v", item.Event)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)
	select {
	case spec := <-fired:
		if got := strings.Join(spec.Command.Argv(), " "); got != "echo from-push" {
			t.Fatalf("job command=%q, want triggering-ref definition", got)
		}
		if spec.Env["PLUTO_EVENT_KIND"] != "push" || spec.Env["PLUTO_EVENT_REF"] != strings.TrimSpace(string(after)) || spec.Env["PLUTO_EVENT_HEAD_REF"] != "refs/heads/main" || spec.Env["PLUTO_EVENT_OBJECT_ID"] != "push-delivery-42" {
			t.Fatalf("push event environment=%+v", spec.Env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for push job")
	}
	current, err := st.Box(box.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The push must move the registered branch box to the pushed commit, not
	// run the job against stale content.
	if current.Branch != "main" || current.Ref != strings.TrimSpace(string(after)) {
		t.Fatalf("push did not advance the registered branch box: branch=%q ref=%q want %q", current.Branch, current.Ref, strings.TrimSpace(string(after)))
	}
	gotHead, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gotHead)); got != strings.TrimSpace(string(after)) {
		t.Fatalf("source worktree head=%s, want the pushed commit %s", got, strings.TrimSpace(string(after)))
	}
}

// A push whose source worktree has local changes blocks the advance and leaves
// the box ref untouched instead of running the job against stale content.
func TestGitHubPushBlocksOnDirtySourceWorktree(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	box, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	sender := filepath.Join(dir, "sender")
	git(t, "clone", remote, sender)
	if err := os.WriteFile(filepath.Join(sender, ".pluto.toml"), []byte("[jobs.test]\ncommand = ['echo', 'from-push']\n[events.push]\njob = 'test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", sender, "add", ".pluto.toml")
	git(t, "-C", sender, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "updated job")
	git(t, "-C", sender, "push", "origin", "main")
	after, err := exec.Command("git", "-C", sender, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	// The registered worktree has uncommitted local work that must be preserved.
	if err := os.WriteFile(filepath.Join(worktree, "local-work.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	fired := make(chan contract.Exec, 1)
	srv := daemon.New(st, fakeRunner{st: st, fired: fired}, "test")
	if err = srv.RegisterGitHubPush("source", box.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q,"deleted":false}`, strings.TrimSpace(string(after)))
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(payload))
	req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
	req.SetPathValue("source", "source")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "push-dirty")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("response %d: %s", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)
	waitFor(t, "the push update to be blocked", func() bool {
		items, err := st.Queue()
		return err == nil && len(items) == 1 && items[0].State == state.QueueBlocked
	})
	if len(fired) != 0 {
		t.Fatalf("job ran despite a dirty source worktree")
	}
	current, err := st.Box(box.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Ref != "" {
		t.Fatalf("box ref = %q, want unchanged after a blocked push", current.Ref)
	}
	if !strings.Contains(current.UpdateBlocked, "local changes") {
		t.Fatalf("blocked reason = %q, want a local-changes block", current.UpdateBlocked)
	}
}

func TestPostCommitEventUsesExistingBoxAndDurableQueue(t *testing.T) {
	dir := t.TempDir()
	remote, worktree := webhookGitRepo(t, dir)
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	socket := filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Shutdown(context.Background())
	commit, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	event := api.PostCommitEvent{Worktree: worktree, Commit: strings.TrimSpace(string(commit)), Branch: "main"}
	for i := 0; i < 2; i++ {
		if err := plutoclient.New(socket).PostCommit(event); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	if items[0].BoxID != box.ID || items[0].Job != "test" || items[0].Event.Kind != "post_commit" {
		t.Fatalf("queue item=%+v", items[0])
	}
	boxes, _, err := st.Boxes()
	if err != nil || len(boxes) != 1 {
		t.Fatalf("init/event created boxes: boxes=%+v err=%v", boxes, err)
	}
}

func TestGenericWebhookUsesTrustedPolicyAndRetainsEventContext(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	policy := "[jobs.test]\ncommand='true'\n[events.generic.build]\njob='test'\nactions=['completed']\n"
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "configure generic event policy")
	git(t, "-C", worktree, "push", "origin", "main")
	box, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	if err := srv.RegisterGenericWebhook("build-system", box.ID, "generic-secret"); err != nil {
		t.Fatal(err)
	}
	body := `{"build":"release-17","url":"https://ci.example/run/17"}`
	if err := srv.RegisterGenericWebhook("other-system", box.ID, "other-secret"); err != nil {
		t.Fatal(err)
	}
	post := func(source, event, action, id, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/generic/"+source, strings.NewReader(body))
		req.SetPathValue("source", source)
		req.Header.Set("X-Pluto-Event", event)
		req.Header.Set("X-Pluto-Action", action)
		if id != "" {
			req.Header.Set("X-Pluto-Event-ID", id)
		}
		timestamp := fmt.Sprint(time.Now().Unix())
		req.Header.Set("X-Pluto-Timestamp", timestamp)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "." + body))
		req.Header.Set("X-Pluto-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		return rec
	}
	if got := post("build-system", "build", "completed", "delivery-17", "wrong").Code; got != 401 {
		t.Fatalf("invalid secret status=%d", got)
	}
	if got := post("build-system", "build", "started", "delivery-17", "generic-secret").Code; got != 422 {
		t.Fatalf("filtered event status=%d", got)
	}
	if got := post("build-system", "build", "completed", "delivery-17", "generic-secret").Code; got != 422 {
		t.Fatalf("unapproved contract event status=%d, want 422", got)
	}
	parsed, err := contract.Parse(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveContract(box.Project, parsed.Hash(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := post("build-system", "build", "completed", "delivery-17", "generic-secret").Code; got != 202 {
			t.Fatalf("accepted event status=%d", got)
		}
	}
	if got := post("other-system", "build", "completed", "delivery-17", "other-secret").Code; got != 202 {
		t.Fatalf("same ID from another source status=%d", got)
	}
	for i := 0; i < 2; i++ {
		if got := post("build-system", "build", "completed", "", "generic-secret").Code; got != 202 {
			t.Fatalf("at-least-once status=%d", got)
		}
	}
	items, err := st.Queue()
	if err != nil || len(items) != 4 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	item := items[0]
	if item.Job != "test" || item.EventSource != "build-system" || item.EventID != "delivery-17" {
		t.Fatalf("item=%+v", item)
	}
	if item.ContractRevision != parsed.Hash() || item.TrustDecision != state.ContractTrustApproved {
		t.Fatalf("event admission metadata = revision %q decision %q", item.ContractRevision, item.TrustDecision)
	}
	var gotPayload, wantPayload map[string]any
	if err := json.Unmarshal(item.Event.Payload, &gotPayload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &wantPayload); err != nil {
		t.Fatal(err)
	}
	if item.Event.Kind != "build" || item.Event.Action != "completed" || item.Event.ObjectID != "delivery-17" || fmt.Sprint(gotPayload) != fmt.Sprint(wantPayload) {
		t.Fatalf("event context=%+v", item.Event)
	}
	srv.QueueCapacity = len(items)
	if got := post("build-system", "build", "completed", "capacity-hit", "generic-secret").Code; got != 503 {
		t.Fatalf("full queue status=%d", got)
	}
	items, err = st.Queue()
	if err != nil || items[len(items)-1].State != state.QueueRejected || items[len(items)-1].Reason != "queue is full" {
		t.Fatalf("full queue outcome=%+v err=%v", items[len(items)-1], err)
	}
	changedPolicy := policy + "\n[env]\nREVISION = 'two'\n"
	if err := os.WriteFile(filepath.Join(worktree, contract.FileName), []byte(changedPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "change generic policy revision")
	git(t, "-C", worktree, "push", "origin", "main")
	if got := post("build-system", "build", "completed", "revision-changed", "generic-secret").Code; got != 422 {
		t.Fatalf("changed contract event status=%d, want 422 until renewed approval", got)
	}
}

func TestGitHubPullRequestUsesTrustedActionPolicyAndReusableBox(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	policy := "[jobs.test]\ncommand='true'\n[events.pull_request]\njob='test'\nactions=['opened','synchronize']\ncredentials=['DEPLOY_TOKEN']\ntrusted_label='maintainer-approved'\n"
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "configure PR policy")
	git(t, "-C", worktree, "push", "origin", "main")
	sha, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	git(t, "--git-dir", remote, "update-ref", "refs/pull/7/head", strings.TrimSpace(string(sha)))
	root, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	advances := 0
	guestBlocked := false
	fired := make(chan contract.Exec, 4)
	t.Setenv("DEPLOY_TOKEN", "host-secret")
	runner := fakeRunner{st: st, record: true, fired: fired, advance: func(_ *state.Box, bundle, ref string) error {
		advances++
		if len(ref) != 40 {
			t.Errorf("advance ref=%q", ref)
		}
		heads, err := exec.Command("git", "bundle", "list-heads", bundle).Output()
		if err != nil || !strings.Contains(string(heads), ref) {
			t.Errorf("bundle does not contain target ref %s: heads=%q err=%v", ref, heads, err)
		}
		if guestBlocked {
			return errors.New("advance: worktree has local changes")
		}
		return nil
	}}
	srv := daemon.New(st, runner, "test")
	if err := srv.RegisterGitHubPush("source", root.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	for i, action := range []string{"closed", "opened", "synchronize"} {
		if action == "synchronize" {
			prTree := filepath.Join(st.Root(), "projects")
			entries, err := os.ReadDir(prTree)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("PR boxes on disk=%d, want one", len(entries))
			}
			if err := os.WriteFile(filepath.Join(prTree, entries[0].Name(), "user-work.txt"), []byte("keep me"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		payload := fmt.Sprintf(`{"action":%q,"number":7,"pull_request":{"html_url":"https://example.test/pr/7","head":{"ref":"feature","sha":%q}}}`, action, strings.TrimSpace(string(sha)))
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(payload))
		req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
		req.SetPathValue("source", "source")
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("pr-%d", i))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		if action == "closed" && rec.Code != 204 {
			t.Fatalf("closed response %d: %s", rec.Code, rec.Body.String())
		}
		if action != "closed" && rec.Code != 202 {
			t.Fatalf("%s response %d: %s", action, rec.Code, rec.Body.String())
		}
	}
	items, err := st.Queue()
	if err != nil || len(items) != 2 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	if items[0].BoxID != items[1].BoxID || items[0].Event.Kind != "pull_request" || items[1].Event.ObjectID != "7" {
		t.Fatalf("PR events did not share a contextual box: %+v", items)
	}
	if items[0].Event.HeadRef != "feature" || items[0].Event.Ref != strings.TrimSpace(string(sha)) {
		t.Fatalf("PR head context was not preserved: %+v", items[0].Event)
	}
	if items[1].State != state.QueueBlocked || !strings.Contains(items[1].Reason, "uncommitted changes") {
		t.Fatalf("dirty PR update was not surfaced as blocked: %+v", items[1])
	}
	if items[0].Event.Trusted || len(items[0].Event.CredentialNames) != 0 {
		t.Fatalf("untrusted PR was elevated: %+v", items[0].Event)
	}
	box, err := st.Box(items[0].BoxID)
	if err != nil {
		t.Fatal(err)
	}
	if box.WorkItemType != "pull_request" || box.WorkItemID != "7" || box.Ref != strings.TrimSpace(string(sha)) || box.Branch != "pluto/pr-7" {
		t.Fatalf("work-item box=%+v", box)
	}
	if box.TrustClass != state.TrustClassUntrusted {
		t.Fatalf("untrusted PR box trust class=%q, want untrusted", box.TrustClass)
	}
	if err := os.Remove(filepath.Join(box.Worktree, "user-work.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transition(box.ID, state.StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "update.txt"), []byte("new head\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", "update.txt")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "advance PR")
	git(t, "-C", worktree, "push", "origin", "main")
	newSHA, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	git(t, "--git-dir", remote, "update-ref", "refs/pull/7/head", strings.TrimSpace(string(newSHA)))
	newPayload := fmt.Sprintf(`{"action":"synchronize","number":7,"pull_request":{"html_url":"https://example.test/pr/7","labels":[{"name":"maintainer-approved"}],"head":{"ref":"feature","sha":%q}}}`, strings.TrimSpace(string(newSHA)))
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(newPayload))
	req := httptest.NewRequest("POST", "/github/source", strings.NewReader(newPayload))
	req.SetPathValue("source", "source")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "pr-advance")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != 202 || advances != 0 {
		t.Fatalf("clean VM advance response=%d calls=%d body=%s", rec.Code, advances, rec.Body.String())
	}
	items, err = st.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if !items[2].Event.Trusted || len(items[2].Event.CredentialNames) != 1 || items[2].Event.CredentialNames[0] != "DEPLOY_TOKEN" {
		t.Fatalf("maintainer trusted policy was not recorded: %+v", items[2].Event)
	}
	trustedBox, err := st.Box(items[2].BoxID)
	if err != nil {
		t.Fatal(err)
	}
	if trustedBox.TrustClass != state.TrustClassTrusted {
		t.Fatalf("labeled PR box trust class=%q, want trusted", trustedBox.TrustClass)
	}
	queueBytes, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(queueBytes), "host-secret") {
		t.Fatal("host secret value was persisted in queue metadata")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)
	deadline := time.After(3 * time.Second)
	for {
		items, err = st.Queue()
		if err != nil {
			t.Fatal(err)
		}
		if items[0].State == state.QueueDone && items[2].State == state.QueueDone {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("PR jobs did not finish: %+v", items)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if items[0].JobID == "" || items[2].JobID == "" {
		t.Fatalf("completed PR queue items are not linked to jobs: %+v", items)
	}
	if advances != 1 {
		t.Fatalf("guest ref updates=%d, want one after queued work serialized", advances)
	}
	box, err = st.Box(box.ID)
	if err != nil {
		t.Fatal(err)
	}
	if box.Ref != strings.TrimSpace(string(newSHA)) {
		t.Fatalf("recorded PR ref=%s, want %s", box.Ref, strings.TrimSpace(string(newSHA)))
	}
	gotHead, err := exec.Command("git", "-C", box.Worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gotHead)); got != strings.TrimSpace(string(newSHA)) {
		t.Fatalf("source box head=%s, want %s", got, strings.TrimSpace(string(newSHA)))
	}
	var untrustedExec, trustedExec contract.Exec
	for i := 0; i < 2; i++ {
		select {
		case spec := <-fired:
			if spec.Env["PLUTO_EVENT_KIND"] == "pull_request" && spec.Env["DEPLOY_TOKEN"] == "host-secret" {
				trustedExec = spec
			} else {
				untrustedExec = spec
			}
		case <-time.After(time.Second):
			t.Fatal("expected queued PR job execution")
		}
	}
	if untrustedExec.Env["PLUTO_EVENT_ACTION"] != "opened" {
		t.Fatalf("untrusted event context=%#v", untrustedExec.Env)
	}
	if untrustedExec.Env["PLUTO_EVENT_HEAD_REF"] != "feature" {
		t.Fatalf("PR source branch context=%#v", untrustedExec.Env)
	}
	if _, ok := untrustedExec.Env["DEPLOY_TOKEN"]; ok {
		t.Fatalf("untrusted PR got host secret: %#v", untrustedExec.Env)
	}
	if trustedExec.Env["DEPLOY_TOKEN"] != "host-secret" {
		t.Fatalf("trusted PR did not receive allowlisted host secret: %#v", trustedExec.Env)
	}
	oldHeadPayload := fmt.Sprintf(`{"action":"synchronize","number":7,"pull_request":{"html_url":"https://example.test/pr/7","head":{"ref":"feature","sha":%q}}}`, strings.TrimSpace(string(sha)))
	postPR := func(payload, delivery string) *httptest.ResponseRecorder {
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(payload))
		req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
		req.SetPathValue("source", "source")
		req.Header.Set("X-GitHub-Event", "pull_request")
		req.Header.Set("X-GitHub-Delivery", delivery)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := postPR(oldHeadPayload, "pr-advance"); rec.Code != 202 {
		t.Fatalf("duplicate delivery response=%d", rec.Code)
	}
	if rec := postPR(oldHeadPayload, "out-of-order"); rec.Code != 202 {
		t.Fatalf("out-of-order delivery response=%d", rec.Code)
	}
	items, err = st.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if items[3].State != state.QueueBlocked || !strings.Contains(items[3].Reason, "older than or diverged") {
		t.Fatalf("out-of-order PR update not blocked: %+v", items[3])
	}
	box, err = st.Box(items[0].BoxID)
	if err != nil {
		t.Fatal(err)
	}
	if box.Ref != strings.TrimSpace(string(newSHA)) {
		t.Fatalf("out-of-order event moved box ref backwards: %s", box.Ref)
	}
	if err := os.WriteFile(filepath.Join(worktree, "third.txt"), []byte("third head\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", "third.txt")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "third PR head")
	git(t, "-C", worktree, "push", "origin", "main")
	thirdSHA, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	git(t, "--git-dir", remote, "update-ref", "refs/pull/7/head", strings.TrimSpace(string(thirdSHA)))
	guestBlocked = true
	thirdPayload := fmt.Sprintf(`{"action":"synchronize","number":7,"pull_request":{"html_url":"https://example.test/pr/7","head":{"ref":"feature","sha":%q}}}`, strings.TrimSpace(string(thirdSHA)))
	if rec := postPR(thirdPayload, "guest-dirty"); rec.Code != 202 {
		t.Fatalf("guest dirty event response=%d %s", rec.Code, rec.Body.String())
	}
	deadline = time.After(3 * time.Second)
	for {
		items, err = st.Queue()
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 4 && items[4].State == state.QueueBlocked {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("dirty guest update did not become blocked: %+v", items)
		case <-time.After(10 * time.Millisecond):
		}
	}
	box, err = st.Box(box.ID)
	if err != nil {
		t.Fatal(err)
	}
	if box.Ref != strings.TrimSpace(string(newSHA)) || !strings.Contains(box.UpdateBlocked, "guest worktree") {
		t.Fatalf("dirty guest update advanced or was not surfaced: %+v", box)
	}
}

func TestGitHubIssueUsesTrustedActionsAndReusableDefaultBranchBox(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	policy := "[jobs.issue-work]\ncommand='echo issue'\n[events.issue]\njob='issue-work'\nactions=['opened','labeled']\ncredentials=['ISSUE_TOKEN']\n"
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "configure issue policy")
	git(t, "-C", worktree, "push", "origin", "main")
	defaultSHA, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	fired := make(chan contract.Exec, 2)
	t.Setenv("ISSUE_TOKEN", "host-secret")
	srv := daemon.New(st, fakeRunner{st: st, record: true, fired: fired}, "test")
	if err := srv.RegisterGitHubPush("source", root.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	postIssue := func(action, delivery string) *httptest.ResponseRecorder {
		payload := fmt.Sprintf(`{"action":%q,"issue":{"number":23,"html_url":"https://example.test/issues/23"}}`, action)
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(payload))
		req := httptest.NewRequest("POST", "/github/source", strings.NewReader(payload))
		req.SetPathValue("source", "source")
		req.Header.Set("X-GitHub-Event", "issues")
		req.Header.Set("X-GitHub-Delivery", delivery)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		srv.WebhookHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := postIssue("closed", "issue-closed"); rec.Code != http.StatusNoContent {
		t.Fatalf("filtered action response=%d %s", rec.Code, rec.Body.String())
	}
	if rec := postIssue("opened", "issue-opened"); rec.Code != http.StatusAccepted {
		t.Fatalf("opened response=%d %s", rec.Code, rec.Body.String())
	}
	if rec := postIssue("labeled", "issue-labeled"); rec.Code != http.StatusAccepted {
		t.Fatalf("labeled response=%d %s", rec.Code, rec.Body.String())
	}
	items, err := st.Queue()
	if err != nil || len(items) != 2 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
	if items[0].BoxID != items[1].BoxID || items[0].Event.Kind != "issue" || items[0].Event.ObjectID != "23" || items[0].Event.URL != "https://example.test/issues/23" {
		t.Fatalf("issue events did not retain shared identity/context: %+v", items)
	}
	if items[0].Event.Ref != strings.TrimSpace(string(defaultSHA)) || items[0].Event.HeadRef != "refs/heads/main" || !items[0].Event.Trusted {
		t.Fatalf("issue did not use trusted default branch context: %+v", items[0].Event)
	}
	box, err := st.Box(items[0].BoxID)
	if err != nil {
		t.Fatal(err)
	}
	if box.WorkItemType != "issue" || box.WorkItemID != "23" || box.Branch != "pluto/issue-23" || box.Ref != strings.TrimSpace(string(defaultSHA)) {
		t.Fatalf("issue box identity/ref=%+v", box)
	}
	head, err := exec.Command("git", "-C", box.Worktree, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != strings.TrimSpace(string(defaultSHA)) {
		t.Fatalf("issue box did not start from default branch: head=%q err=%v", head, err)
	}
	queueBytes, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(queueBytes), "host-secret") {
		t.Fatal("issue credential value was persisted in queue metadata")
	}
	if err := os.WriteFile(filepath.Join(box.Worktree, "local-work.txt"), []byte("preserve this issue work"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "default-update.txt"), []byte("new default branch commit"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", "default-update.txt")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "advance default branch")
	git(t, "-C", worktree, "push", "origin", "main")
	newDefault, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if rec := postIssue("labeled", "issue-labeled-dirty"); rec.Code != http.StatusAccepted {
		t.Fatalf("dirty issue update response=%d %s", rec.Code, rec.Body.String())
	}
	items, err = st.Queue()
	if err != nil || len(items) != 3 {
		t.Fatalf("queue after issue update=%+v err=%v", items, err)
	}
	if items[2].BoxID != box.ID || items[2].State != state.QueueBlocked || !strings.Contains(items[2].Reason, "uncommitted changes") {
		t.Fatalf("unsafe issue update was not surfaced against reusable box: %+v", items[2])
	}
	if box.Ref != strings.TrimSpace(string(defaultSHA)) {
		t.Fatalf("issue box ref advanced from %s to %s despite dirty work", defaultSHA, box.Ref)
	}
	localWork, err := os.ReadFile(filepath.Join(box.Worktree, "local-work.txt"))
	if err != nil || string(localWork) != "preserve this issue work" {
		t.Fatalf("issue work was overwritten: %q err=%v", localWork, err)
	}
	if strings.TrimSpace(string(newDefault)) == strings.TrimSpace(string(defaultSHA)) {
		t.Fatal("test did not advance the default branch")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)
	deadline := time.After(3 * time.Second)
	for {
		items, err = st.Queue()
		if err != nil {
			t.Fatal(err)
		}
		if items[0].State == state.QueueDone && items[1].State == state.QueueDone {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("issue jobs did not finish: %+v", items)
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, item := range items[:2] {
		if item.JobID == "" || item.BoxID != box.ID {
			t.Fatalf("queue item outcome is not linked to issue box/job: %+v", item)
		}
	}
	if items[2].State != state.QueueBlocked || items[2].JobID != "" {
		t.Fatalf("blocked issue update unexpectedly ran: %+v", items[2])
	}
	for i := 0; i < 2; i++ {
		select {
		case spec := <-fired:
			if spec.Command.String() != "echo issue" || spec.Env["PLUTO_EVENT_KIND"] != "issue" || spec.Env["PLUTO_EVENT_ACTION"] == "" || spec.Env["ISSUE_TOKEN"] != "host-secret" {
				t.Fatalf("issue job did not use default contract and allowlisted credentials: %+v", spec)
			}
		case <-time.After(time.Second):
			t.Fatal("expected queued issue job execution")
		}
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
	req.Header.Set("X-GitHub-Event", "projects_v2_item")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logged, `unsupported GitHub event type "projects_v2_item"`) {
		t.Fatalf("log=%q", logged)
	}
	items, err := st.Queue()
	if err != nil || len(items) != 0 {
		t.Fatalf("queue=%+v err=%v", items, err)
	}
}
