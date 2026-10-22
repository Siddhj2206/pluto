package daemon_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/daemon"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// TestGitHubIssueLabelGateAdmitsOnlyPlutoFix proves the declarative
// [events.issue] labels gate at admission: an issue without the label is
// ignored with a durable reason naming the missing label and creates no box,
// task, or queue item; a delivery carrying the label is admitted.
func TestGitHubIssueLabelGateAdmitsOnlyPlutoFix(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	policy := "[jobs.issue-work]\ncommand='echo issue'\n[events.issue]\njob='issue-work'\nactions=['opened','labeled']\nlabels=['pluto:fix']\ncredentials=['ISSUE_TOKEN']\n"
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "configure issue label gate")
	git(t, "-C", worktree, "push", "origin", "main")
	root, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	approveWebhookContract(t, st, remote, root.Project)
	t.Setenv("ISSUE_TOKEN", "host-secret")
	srv := daemon.New(st, fakeRunner{st: st}, "test")
	ledger, err := events.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	srv.Events = ledger
	if err := srv.RegisterGitHubPush("source", root.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	post := func(delivery, action string, labels []string) *httptest.ResponseRecorder {
		t.Helper()
		payload := fmt.Sprintf(`{"action":%q,"issue":{"number":23,"html_url":"https://example.test/issues/23","labels":%s}}`, action, issueLabelsJSON(labels))
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

	if rec := post("issue-unlabelled", "opened", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unlabelled issue response = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post("issue-wrong-label", "opened", []string{"bug"}); rec.Code != http.StatusNoContent {
		t.Fatalf("wrong-label issue response = %d: %s", rec.Code, rec.Body.String())
	}
	if boxes, _, err := st.Boxes(); err != nil || len(boxes) != 1 {
		t.Fatalf("ignored issues created work-item boxes: boxes=%+v err=%v", boxes, err)
	}
	if items, err := st.Queue(); err != nil || len(items) != 0 {
		t.Fatalf("ignored issues queued work: items=%+v err=%v", items, err)
	}
	if tasks, err := st.Tasks(); err != nil || len(tasks) != 0 {
		t.Fatalf("ignored issues created tasks: tasks=%+v err=%v", tasks, err)
	}
	ignored, err := ledger.List(events.Filter{KindPrefix: events.KindWebhookIgnored})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 2 {
		t.Fatalf("webhook.ignored events = %d, want one per unlabelled delivery", len(ignored))
	}
	for _, event := range ignored {
		if !strings.Contains(event.Reason, "pluto:fix") {
			t.Fatalf("ignored reason %q does not name the missing label", event.Reason)
		}
	}

	if rec := post("issue-labelled", "labeled", []string{"pluto:fix"}); rec.Code != http.StatusAccepted {
		t.Fatalf("labelled issue response = %d: %s", rec.Code, rec.Body.String())
	}
	items, err := st.Queue()
	if err != nil || len(items) != 1 || items[0].Job != "issue-work" || items[0].Event.ObjectID != "23" {
		t.Fatalf("labelled issue was not admitted: items=%+v err=%v", items, err)
	}
	if len(items[0].Event.Labels) != 1 || items[0].Event.Labels[0] != "pluto:fix" {
		t.Fatalf("issue labels were not carried into the job context: %+v", items[0].Event)
	}
	admitted, err := ledger.List(events.Filter{KindPrefix: events.KindWebhookAdmitted})
	if err != nil || len(admitted) != 1 {
		t.Fatalf("webhook.admitted events = %+v, %v", admitted, err)
	}
}

// issueLabelsJSON renders the labels array of an issues payload.
func issueLabelsJSON(labels []string) string {
	if len(labels) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, fmt.Sprintf(`{"name":%q}`, label))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TestGitHubLoopKeepsOneTraceFromIntakeToJob proves the review-3 chain is one
// trace: a signed issue delivery, its task, run, queue item, and job share the
// intake trace and carry the payload, labels, trust decision, and Pluto ids the
// job scripts need. A trusted PR delivery runs on its own work-item box with
// the comment token; the same head delivered without the trusted label runs
// without host credentials.
func TestGitHubLoopKeepsOneTraceFromIntakeToJob(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	remote, worktree := webhookGitRepo(t, dir)
	policy := `[jobs.issue-work]
command = 'echo issue'
[jobs.pr-test]
command = 'echo pr'
[events.issue]
job = 'issue-work'
actions = ['opened', 'labeled']
labels = ['pluto:fix']
credentials = ['ISSUE_TOKEN']
[events.pull_request]
job = 'pr-test'
actions = ['opened', 'synchronize']
trusted_label = 'pluto:trusted'
credentials = ['PR_TOKEN']
`
	if err := os.WriteFile(filepath.Join(worktree, ".pluto.toml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, "-C", worktree, "add", ".pluto.toml")
	git(t, "-C", worktree, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "configure the GitHub loop")
	git(t, "-C", worktree, "push", "origin", "main")
	head, err := exec.Command("git", "-C", worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	defaultSHA := strings.TrimSpace(string(head))
	git(t, "--git-dir", remote, "update-ref", "refs/pull/7/head", defaultSHA)
	root, _, err := st.CreateBoxWithRepo("repo", "main", worktree, remote)
	if err != nil {
		t.Fatal(err)
	}
	approveWebhookContract(t, st, remote, root.Project)
	t.Setenv("ISSUE_TOKEN", "issue-secret")
	t.Setenv("PR_TOKEN", "pr-secret")
	fired := make(chan contract.Exec, 4)
	runner := fakeRunner{st: st, record: true, fired: fired}
	runner.obs = &fakeObservers{}
	srv := daemon.New(st, runner, "test")
	ledger, err := events.Open(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	srv.Events = ledger
	if err := srv.RegisterGitHubPush("source", root.ID, "secret"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.SchedulerLoop(ctx, 10*time.Millisecond)

	issueBody := fmt.Sprintf(`{"action":"opened","issue":{"number":23,"html_url":"https://example.test/issues/23","labels":%s},"repository":{"html_url":%q}}`, issueLabelsJSON([]string{"pluto:fix"}), remote)
	if rec := postSignedGitHubEvent(t, srv, "source", "secret", "issue-1", "issues", issueBody); rec.Code != http.StatusAccepted {
		t.Fatalf("issue delivery response = %d: %s", rec.Code, rec.Body.String())
	}
	issueSpec := nextFired(t, fired)
	if issueSpec.Env["PLUTO_EVENT_KIND"] != "issue" || issueSpec.Env["PLUTO_EVENT_OBJECT_ID"] != "23" {
		t.Fatalf("issue job context = %#v", issueSpec.Env)
	}
	if payload := issueSpec.Env["PLUTO_EVENT_PAYLOAD"]; !strings.Contains(payload, "pluto:fix") || !strings.Contains(payload, `"number"`) {
		t.Fatalf("issue job did not receive the event payload: %q", payload)
	}
	if issueSpec.Env["PLUTO_EVENT_LABELS"] != "pluto:fix" || issueSpec.Env["PLUTO_EVENT_TRUSTED"] != "true" {
		t.Fatalf("issue job gate context = %#v", issueSpec.Env)
	}
	if issueSpec.Env["ISSUE_TOKEN"] != "issue-secret" {
		t.Fatalf("issue job did not receive its allowlisted credential: %#v", issueSpec.Env)
	}
	issueTrace := issueSpec.Env["PLUTO_TRACE_ID"]
	if issueTrace == "" || issueSpec.Env["PLUTO_TASK_ID"] == "" || issueSpec.Env["PLUTO_RUN_ID"] == "" {
		t.Fatalf("issue job lacks trace/task/run identity: %#v", issueSpec.Env)
	}
	waitForTraceKind(t, ledger, issueTrace, events.KindJobDone)
	assertTraceChain(t, ledger, issueTrace, issueSpec.Env["PLUTO_TASK_ID"], issueSpec.Env["PLUTO_RUN_ID"])

	// The same head delivered twice: once with the trusted label and once
	// without. The trusted run may comment; the untrusted run gets no token.
	trustedBody := fmt.Sprintf(`{"action":"opened","number":7,"pull_request":{"html_url":"https://example.test/pull/7","labels":[{"name":"pluto:trusted"}],"head":{"ref":"feature","sha":%q}},"repository":{"html_url":%q}}`, defaultSHA, remote)
	if rec := postSignedGitHubEvent(t, srv, "source", "secret", "pr-1", "pull_request", trustedBody); rec.Code != http.StatusAccepted {
		t.Fatalf("trusted PR delivery response = %d: %s", rec.Code, rec.Body.String())
	}
	trustedSpec := nextFired(t, fired)
	if trustedSpec.Env["PLUTO_EVENT_TRUSTED"] != "true" || trustedSpec.Env["PR_TOKEN"] != "pr-secret" || trustedSpec.Env["PLUTO_EVENT_LABELS"] != "pluto:trusted" {
		t.Fatalf("trusted PR job context = %#v", trustedSpec.Env)
	}
	if trustedSpec.Env["PLUTO_EVENT_OBJECT_ID"] != "7" || trustedSpec.Env["PLUTO_EVENT_REF"] != defaultSHA {
		t.Fatalf("trusted PR job identity = %#v", trustedSpec.Env)
	}
	waitForTraceKind(t, ledger, trustedSpec.Env["PLUTO_TRACE_ID"], events.KindJobDone)
	assertTraceChain(t, ledger, trustedSpec.Env["PLUTO_TRACE_ID"], trustedSpec.Env["PLUTO_TASK_ID"], trustedSpec.Env["PLUTO_RUN_ID"])

	untrustedBody := fmt.Sprintf(`{"action":"synchronize","number":7,"pull_request":{"html_url":"https://example.test/pull/7","labels":[{"name":"needs-review"}],"head":{"ref":"feature","sha":%q}},"repository":{"html_url":%q}}`, defaultSHA, remote)
	if rec := postSignedGitHubEvent(t, srv, "source", "secret", "pr-2", "pull_request", untrustedBody); rec.Code != http.StatusAccepted {
		t.Fatalf("untrusted PR delivery response = %d: %s", rec.Code, rec.Body.String())
	}
	untrustedSpec := nextFired(t, fired)
	if untrustedSpec.Env["PLUTO_EVENT_TRUSTED"] != "false" {
		t.Fatalf("untrusted PR trust context = %#v", untrustedSpec.Env)
	}
	if _, ok := untrustedSpec.Env["PR_TOKEN"]; ok {
		t.Fatalf("untrusted PR received host credentials: %#v", untrustedSpec.Env)
	}
	if untrustedSpec.Env["PLUTO_TASK_ID"] != trustedSpec.Env["PLUTO_TASK_ID"] {
		t.Fatalf("PR deliveries did not share the durable PR task: %q vs %q", untrustedSpec.Env["PLUTO_TASK_ID"], trustedSpec.Env["PLUTO_TASK_ID"])
	}

	// A fork PR carrying the trusted label is still untrusted: a maintainer
	// can label it, but its head lives in another repository, so it gets no
	// host credentials and cannot comment.
	forkBody := fmt.Sprintf(`{"action":"opened","number":7,"pull_request":{"html_url":"https://example.test/pull/7","labels":[{"name":"pluto:trusted"}],"head":{"ref":"feature","sha":%q,"repo":{"full_name":"attacker/project"}},"base":{"repo":{"full_name":"example/project"}}},"repository":{"html_url":%q}}`, defaultSHA, remote)
	if rec := postSignedGitHubEvent(t, srv, "source", "secret", "pr-fork", "pull_request", forkBody); rec.Code != http.StatusAccepted {
		t.Fatalf("fork PR delivery response = %d: %s", rec.Code, rec.Body.String())
	}
	forkSpec := nextFired(t, fired)
	if forkSpec.Env["PLUTO_EVENT_TRUSTED"] != "false" {
		t.Fatalf("labelled fork PR was trusted: %#v", forkSpec.Env)
	}
	if _, ok := forkSpec.Env["PR_TOKEN"]; ok {
		t.Fatalf("fork PR received host credentials: %#v", forkSpec.Env)
	}
}

// waitForTraceKind waits until one traced job has recorded its terminal event,
// so assertions see the whole chain rather than the instant the job starts.
func waitForTraceKind(t *testing.T, ledger *events.Store, trace, kind string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, err := ledger.List(events.Filter{TraceID: trace, KindPrefix: kind, Limit: 10})
		if err == nil && len(list) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("trace %s never recorded %s", trace, kind)
}

func nextFired(t *testing.T, fired <-chan contract.Exec) contract.Exec {
	t.Helper()
	select {
	case spec := <-fired:
		return spec
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for the event job")
		return contract.Exec{}
	}
}

// assertTraceChain proves the intake decision, queue acceptance, and job
// lifecycle events all belong to one trace and one task/run.
func assertTraceChain(t *testing.T, ledger *events.Store, trace, taskID, runID string) {
	t.Helper()
	if trace == "" || taskID == "" || runID == "" {
		t.Fatalf("trace chain needs trace/task/run ids, got %q/%q/%q", trace, taskID, runID)
	}
	list, err := ledger.List(events.Filter{TraceID: trace, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, event := range list {
		if event.TraceID != trace {
			t.Fatalf("event %s leaked another trace: %+v", event.Kind, event)
		}
		kinds[event.Kind] = true
	}
	for _, want := range []string{events.KindWebhookReceived, events.KindWebhookVerified, events.KindWebhookAdmitted, events.KindQueueAccepted, events.KindJobStarted, events.KindJobDone} {
		if !kinds[want] {
			all, _ := ledger.List(events.Filter{Limit: 1000})
			var seen []string
			for _, event := range all {
				seen = append(seen, event.Kind+":"+event.TraceID)
			}
			t.Fatalf("trace %s is missing %s; got %v; ledger %v", trace, want, kinds, seen)
		}
	}
	tasks, err := ledger.List(events.Filter{TraceID: trace, KindPrefix: "task.", Limit: 1000})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("trace %s has no task events: %+v, %v", trace, tasks, err)
	}
	found := false
	for _, event := range tasks {
		if event.Where.TaskID == taskID || event.Attrs["task_id"] == taskID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("trace %s task events do not name task %s: %+v", trace, taskID, tasks)
	}
	found = false
	for _, event := range list {
		if event.Where.RunID == runID || event.Attrs["run_id"] == runID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("trace %s events do not name run %s: %+v", trace, runID, list)
	}
}
