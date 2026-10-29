package daemon_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Siddhj2206/pluto/internal/api"
	plutoclient "github.com/Siddhj2206/pluto/internal/client"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// TestWebhookSourceAPIAddsPersistsListsAndRestores drives the runtime source
// surface: a source is added without a daemon restart, persisted without the
// secret value, listed redacted, restored at start, and removed.
func TestWebhookSourceAPIAddsPersistsListsAndRestores(t *testing.T) {
	const secretEnv = "PLUTO_TEST_HOOK_SECRET"
	const secretValue = "super-secret-value"
	t.Setenv(secretEnv, secretValue)

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
	approveWebhookContract(t, st, remote, box.Project)

	srv := daemon.New(st, fakeRunner{st: st}, "test")
	srv.Events, err = events.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Events.Close()
	socket := filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Shutdown(context.Background())
	c := plutoclient.New(socket)

	status, err := c.AddWebhook(api.WebhookAddRequest{
		Kind:      state.WebhookKindGitHub,
		Source:    "production",
		BoxID:     box.ID,
		SecretEnv: secretEnv,
	})
	if err != nil {
		t.Fatalf("AddWebhook: %v", err)
	}
	if status.Source != "production" || status.Endpoint != "/github/production" || !status.SecretPresent || !status.Persisted {
		t.Fatalf("added source = %+v", status)
	}
	if status.SecretEnv != secretEnv {
		t.Fatalf("secret env name = %q, want %q", status.SecretEnv, secretEnv)
	}

	raw, err := os.ReadFile(filepath.Join(st.Root(), "webhooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), secretEnv) {
		t.Fatalf("webhooks.json does not name the secret env: %s", raw)
	}
	if strings.Contains(string(raw), secretValue) {
		t.Fatalf("webhooks.json stores the secret value: %s", raw)
	}

	list, err := c.Webhooks()
	if err != nil || len(list) != 1 {
		t.Fatalf("Webhooks = %+v, %v", list, err)
	}
	if list[0].Accepted != 0 || list[0].Rejected != 0 || list[0].LastDeliveryAt != nil {
		t.Fatalf("fresh source status = %+v", list[0])
	}

	// A second server on the same state restores the persisted registration,
	// so a daemon restart does not need the add command again.
	restored := daemon.New(st, fakeRunner{st: st}, "test")
	restored.Events = srv.Events
	if err := restored.RestoreWebhookSources(); err != nil {
		t.Fatalf("RestoreWebhookSources: %v", err)
	}
	restoredSocket := filepath.Join(dir, "restored.sock")
	if err := restored.Listen(restoredSocket); err != nil {
		t.Fatal(err)
	}
	go restored.Serve()
	defer restored.Shutdown(context.Background())
	restoredList, err := plutoclient.New(restoredSocket).Webhooks()
	if err != nil || len(restoredList) != 1 || restoredList[0].Source != "production" || !restoredList[0].SecretPresent {
		t.Fatalf("restored Webhooks = %+v, %v", restoredList, err)
	}
	rec := postSignedGitHub(t, restored, "production", secretValue, "delivery-restored", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("restored source response = %d: %s", rec.Code, rec.Body.String())
	}

	if err := c.RemoveWebhook("production"); err != nil {
		t.Fatalf("RemoveWebhook: %v", err)
	}
	if list, err := c.Webhooks(); err != nil || len(list) != 0 {
		t.Fatalf("Webhooks after removal = %+v, %v", list, err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), "webhooks.json")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("webhooks.json after removal: %v", err)
	}
	rec = postSignedGitHub(t, srv, "production", secretValue, "delivery-after-remove", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("removed source response = %d, want 404", rec.Code)
	}
}

// TestWebhookIntakeRecordsDurableDecisions proves every decision leaves a
// durable, redacted event and that the status counts and last rejection are
// readable through the API.
func TestWebhookIntakeRecordsDurableDecisions(t *testing.T) {
	const secretEnv = "PLUTO_TEST_HOOK_SECRET"
	const secretValue = "super-secret-value"
	t.Setenv(secretEnv, secretValue)

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
	approveWebhookContract(t, st, remote, box.Project)

	srv := daemon.New(st, fakeRunner{st: st}, "test")
	srv.Events, err = events.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Events.Close()
	socket := filepath.Join(dir, "pluto.sock")
	if err := srv.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Shutdown(context.Background())
	c := plutoclient.New(socket)
	if _, err := c.AddWebhook(api.WebhookAddRequest{Kind: state.WebhookKindGitHub, Source: "production", BoxID: box.ID, SecretEnv: secretEnv}); err != nil {
		t.Fatal(err)
	}

	after, err := gitOutput("-C", worktree, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q,"deleted":false,"repository":{"html_url":%q}}`, after, remote)

	// A bad signature is rejected with a durable reason.
	rec := postSignedGitHub(t, srv, "production", "wrong-secret", "delivery-bad", payload)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature response = %d: %s", rec.Code, rec.Body.String())
	}
	list, err := c.Webhooks()
	if err != nil || len(list) != 1 {
		t.Fatalf("Webhooks = %+v, %v", list, err)
	}
	if list[0].Rejected != 1 || list[0].LastRejectionReason != "invalid signature" || list[0].LastDeliveryID != "delivery-bad" || list[0].LastDeliveryAt == nil {
		t.Fatalf("rejected source status = %+v", list[0])
	}
	rejected, err := srv.Events.List(events.Filter{KindPrefix: events.KindWebhookRejected, Limit: 10})
	if err != nil || len(rejected) != 1 || rejected[0].Where.DeliveryID != "delivery-bad" || rejected[0].Reason != "invalid signature" {
		t.Fatalf("rejected event = %+v, %v", rejected, err)
	}

	// A valid delivery is received, verified, and admitted.
	rec = postSignedGitHub(t, srv, "production", secretValue, "delivery-good", payload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid delivery response = %d: %s", rec.Code, rec.Body.String())
	}
	assertWebhookEvent(t, srv, events.KindWebhookReceived, "delivery-good", box, after, len(payload), payload)
	assertWebhookEvent(t, srv, events.KindWebhookVerified, "delivery-good", box, after, len(payload), payload)
	assertWebhookEvent(t, srv, events.KindWebhookAdmitted, "delivery-good", box, after, len(payload), payload)

	// A retry is deduplicated and leaves its own event.
	rec = postSignedGitHub(t, srv, "production", secretValue, "delivery-good", payload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("duplicate response = %d: %s", rec.Code, rec.Body.String())
	}
	assertWebhookEvent(t, srv, events.KindWebhookDeduped, "delivery-good", box, after, len(payload), payload)

	// A signed but unsupported event is ignored, not admitted.
	rec = postSignedGitHubEvent(t, srv, "production", secretValue, "delivery-ping", "ping", `{"zen":"hello"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unsupported event response = %d: %s", rec.Code, rec.Body.String())
	}
	assertWebhookEvent(t, srv, events.KindWebhookIgnored, "delivery-ping", box, "", len(`{"zen":"hello"}`), `{"zen":"hello"}`)

	list, err = c.Webhooks()
	if err != nil || len(list) != 1 {
		t.Fatalf("Webhooks = %+v, %v", list, err)
	}
	if list[0].Accepted != 2 || list[0].Rejected != 1 || list[0].LastDeliveryID != "delivery-ping" || list[0].LastEvent != "ping" {
		t.Fatalf("source status = %+v", list[0])
	}

	// The ledger never stores the payload body, the secret, or an env value.
	all, err := srv.Events.List(events.Filter{KindPrefix: "webhook.", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		encoded, _ := json.Marshal(e)
		for _, forbidden := range []string{"refs/heads/main\",\"after", secretValue, `"deleted"`} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("event %s contains %q: %s", e.Kind, forbidden, encoded)
			}
		}
	}

	// The local test delivery is signed, labelled, and does not queue work.
	before, err := st.Queue()
	if err != nil {
		t.Fatal(err)
	}
	test, err := c.TestWebhook("production")
	if err != nil {
		t.Fatalf("TestWebhook: %v", err)
	}
	if !test.Verified || test.Outcome != "ignored" || !strings.Contains(test.Reason, "test") {
		t.Fatalf("test delivery = %+v", test)
	}
	afterQueue, err := st.Queue()
	if err != nil || len(afterQueue) != len(before) {
		t.Fatalf("test delivery queued work: before %d after %d, %v", len(before), len(afterQueue), err)
	}
	ignored, err := srv.Events.List(events.Filter{KindPrefix: "webhook.ignored", Limit: 10})
	if err != nil || len(ignored) == 0 {
		t.Fatalf("test ignored events = %+v, %v", ignored, err)
	}
	foundTest := false
	for _, e := range ignored {
		if e.Where.DeliveryID == test.DeliveryID && e.Attrs["test"] == "true" {
			foundTest = true
		}
	}
	if !foundTest {
		t.Fatalf("no labelled test event for %s: %+v", test.DeliveryID, ignored)
	}

	// The generic route has the same labelled test path.
	if _, err := c.AddWebhook(api.WebhookAddRequest{Kind: state.WebhookKindGeneric, Source: "ci", BoxID: box.ID, SecretEnv: secretEnv}); err != nil {
		t.Fatalf("add generic source: %v", err)
	}
	genericTest, err := c.TestWebhook("ci")
	if err != nil {
		t.Fatalf("generic TestWebhook: %v", err)
	}
	if !genericTest.Verified || genericTest.Outcome != "ignored" || genericTest.Endpoint != "/generic/ci" {
		t.Fatalf("generic test delivery = %+v", genericTest)
	}
}

func postSignedGitHub(t *testing.T, srv *daemon.Server, source, secret, delivery, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postSignedGitHubEvent(t, srv, source, secret, delivery, "push", body)
}

func postSignedGitHubEvent(t *testing.T, srv *daemon.Server, source, secret, delivery, event, body string) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	req := httptest.NewRequest("POST", "/github/"+source, strings.NewReader(body))
	req.SetPathValue("source", source)
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.WebhookHandler().ServeHTTP(rec, req)
	return rec
}

func assertWebhookEvent(t *testing.T, srv *daemon.Server, kind, delivery string, box *state.Box, objectID string, size int, payload string) {
	t.Helper()
	list, err := srv.Events.List(events.Filter{KindPrefix: kind, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		if e.Where.DeliveryID != delivery {
			continue
		}
		if e.Where.EventSource != "production" || e.Where.BoxID != box.ID {
			t.Fatalf("%s where = %+v", kind, e.Where)
		}
		if e.Attrs["repo"] != box.PrimaryRepoURL {
			t.Fatalf("%s repo = %q, want %q", kind, e.Attrs["repo"], box.PrimaryRepoURL)
		}
		if objectID != "" && e.Attrs["object_id"] != objectID {
			t.Fatalf("%s object_id = %q, want %q", kind, e.Attrs["object_id"], objectID)
		}
		if e.Attrs["size"] != fmt.Sprint(size) {
			t.Fatalf("%s size = %q, want %d", kind, e.Attrs["size"], size)
		}
		sum := sha256.Sum256([]byte(payload))
		if e.Attrs["sha256"] != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s sha256 = %q, want the payload hash", kind, e.Attrs["sha256"])
		}
		if strings.Contains(e.Summary, "deleted") || strings.Contains(e.Reason, "deleted") {
			t.Fatalf("%s stored payload prose: %+v", kind, e)
		}
		return
	}
	t.Fatalf("no %s event for delivery %q in %+v", kind, delivery, list)
}

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}
