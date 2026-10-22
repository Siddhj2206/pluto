package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/hexid"
	"github.com/Siddhj2206/pluto/internal/state"
)

// approvedEventContract resolves the box's current contract revision and
// reports whether the project approved exactly that revision.
func (s *Server) approvedEventContract(box *state.Box, trusted *contract.Contract) (string, bool, error) {
	revision := trusted.Hash()
	approval, err := s.store.ContractApproval(box.Project)
	if err != nil {
		return revision, false, err
	}
	return revision, approval != nil && approval.Revision == revision, nil
}

// requireApprovedEventContract resolves the box's current contract revision
// and confirms it is approved for unattended work. On failure it writes the
// shared admission response and reports false, so every event handler admits
// revisions the same way.
func (s *Server) requireApprovedEventContract(w http.ResponseWriter, box *state.Box, trusted *contract.Contract) (string, bool) {
	revision, approved, err := s.approvedEventContract(box, trusted)
	if err != nil {
		http.Error(w, "contract trust state unavailable", http.StatusInternalServerError)
		return "", false
	}
	if !approved {
		http.Error(w, "current contract revision is not approved for unattended work", http.StatusUnprocessableEntity)
		return "", false
	}
	return revision, true
}

// githubTaskEvent bundles the facts of one GitHub delivery that task
// acceptance needs, so the call signature names the event rather than a long
// positional list.
type githubTaskEvent struct {
	source    string
	kind      string
	subject   string
	taskRef   string
	job       string
	eventKind string
	action    string
	delivery  string
	revision  string
	ref       string
	event     state.EventContext
	// traceID is the intake trace created by the webhook handler. When empty
	// (post-commit, direct callers) acceptance creates a fresh one.
	traceID string
}

func (s *Server) acceptGitHubTask(box *state.Box, e githubTaskEvent) (*state.QueueItem, error) {
	repo := box.PrimaryRepoURL
	repoDigest := sha256.Sum256([]byte(repo))
	identity := fmt.Sprintf("github:%x:%s:%s", repoDigest[:], e.kind, e.subject)
	prompt := "GitHub " + e.eventKind
	if e.action != "" {
		prompt += " " + e.action
	}
	prompt += " for " + e.kind + " " + e.subject
	if e.event.ObjectID == "" {
		e.event.ObjectID = e.subject
	}
	traceID := e.traceID
	if traceID == "" {
		traceID = events.NewTraceID()
	}
	actor := "github:" + e.source
	item := state.QueueItem{
		Source: state.QueueEvent, EventSource: e.source, EventID: e.delivery, Repo: repo,
		Ref: e.ref, BoxID: box.ID, Job: e.job, ContractRevision: e.revision,
		TrustDecision: state.ContractTrustApproved, Event: e.event,
		TraceID: traceID, Actor: actor,
	}
	task, err := s.store.AcceptTriggeredTask(
		state.Task{Source: "github", Project: box.Project, Ref: e.taskRef, BoxID: box.ID, IdempotencyKey: identity, TraceID: traceID, Actor: actor},
		state.TaskRun{Job: e.job, Prompt: prompt, IdempotencyKey: "github:" + e.source + ":" + e.delivery, ContractRevision: e.revision, TrustDecision: state.ContractTrustApproved, TraceID: traceID, Actor: actor},
		item, s.QueueCapacity, s.now(),
	)
	if task == nil {
		return nil, err
	}
	queue, queueErr := s.store.Queue()
	if queueErr != nil {
		return nil, queueErr
	}
	for _, queued := range queue {
		if queued.Source == state.QueueEvent && queued.EventSource == e.source && queued.EventID == e.delivery {
			if queued.TraceID == "" {
				queued.TraceID = traceID
			}
			s.recordQueued(box, queued, traceID, events.Actor{Type: "github", ID: e.delivery, Client: e.source})
			return &queued, err
		}
	}
	return nil, errors.New("event task run has no queue item")
}

func (s *Server) handlePostCommitEvent(w http.ResponseWriter, r *http.Request) {
	var event api.PostCommitEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&event); err != nil {
		http.Error(w, "invalid post-commit event", http.StatusBadRequest)
		return
	}
	root, err := filepath.Abs(event.Worktree)
	if err != nil || event.Worktree == "" || event.Commit == "" || event.Branch == "" {
		http.Error(w, "worktree, commit, and branch are required", http.StatusBadRequest)
		return
	}
	boxes, _, err := s.store.Boxes()
	if err != nil {
		http.Error(w, "box lookup failed", http.StatusInternalServerError)
		return
	}
	var box *state.Box
	for _, candidate := range boxes {
		if candidate.Worktree == root && candidate.Branch == event.Branch {
			box = candidate
			break
		}
	}
	if box == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ct, _, _, err := trustedDefaultBranch(r.Context(), box)
	if err != nil {
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	revision, ok := s.requireApprovedEventContract(w, box, ct)
	if !ok {
		return
	}
	if ct.Events.Push == nil || ct.Events.Push.Job == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delivery := "post-commit:" + box.ID + ":" + event.Commit
	identityDigest := sha256.Sum256([]byte(box.PrimaryRepoURL))
	identity := fmt.Sprintf("post-commit:%x:%s", identityDigest[:], box.Branch)
	traceID, actor := originFor(r, "", events.Actor{})
	_, err = s.store.AcceptTriggeredTask(
		state.Task{Source: "post_commit", Project: box.Project, Ref: box.Branch, BoxID: box.ID, IdempotencyKey: identity, TraceID: traceID, Actor: actorName(actor)},
		state.TaskRun{Job: ct.Events.Push.Job, Prompt: "Post-commit on " + event.Branch + " at " + event.Commit, IdempotencyKey: delivery, ContractRevision: revision, TrustDecision: state.ContractTrustApproved, TraceID: traceID, Actor: actorName(actor)},
		state.QueueItem{Source: state.QueueEvent, EventSource: "post-commit", Repo: box.PrimaryRepoURL, Ref: event.Commit, BoxID: box.ID, Job: ct.Events.Push.Job, EventID: delivery, ContractRevision: revision, TrustDecision: state.ContractTrustApproved, TraceID: traceID, Actor: actorName(actor), Event: state.EventContext{Kind: "post_commit", Repo: box.PrimaryRepoURL, Ref: event.Commit, HeadRef: event.Branch, ObjectID: event.Commit}},
		s.QueueCapacity, s.now(),
	)
	if errors.Is(err, state.ErrQueueFull) {
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "queue acceptance failed", http.StatusServiceUnavailable)
		return
	}
	s.record(events.Event{
		Kind:    events.KindQueueAccepted,
		TraceID: traceID,
		Actor:   actor,
		Summary: "queued post-commit job " + ct.Events.Push.Job,
		Where:   events.Where{BoxID: box.ID, EventSource: "post-commit", DeliveryID: delivery},
		Attrs:   map[string]string{"source": state.QueueEvent},
	}, box)
	w.WriteHeader(http.StatusAccepted)
}

// ListenWebhook starts the dedicated inbound HTTP surface. Put it behind a
// user-managed TLS proxy when GitHub must reach it over HTTPS.
func (s *Server) ListenWebhook(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	h := &http.Server{Handler: s.WebhookHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = h.Serve(ln) }()
	return h, nil
}

// Generic requests sign the exact body as HMAC-SHA256 over
// `unix_timestamp.body`; stale timestamps are rejected to limit replays.
func (s *Server) handleGenericWebhook(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	cfg, ok := s.webhookRegistrationFor(source, state.WebhookKindGeneric)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	// A stable ID lets a retried delivery resolve to the same run. Without
	// one there is nothing to deduplicate on, so the delivery is accepted
	// at-least-once under a unique internal identity (ADR 0003).
	deliveryID := strings.TrimSpace(r.Header.Get("X-Pluto-Event-ID"))
	if deliveryID == "" {
		deliveryID = newUnidentifiedDeliveryID(source)
	}
	d := newWebhookDelivery(source, state.WebhookKindGeneric, r.Header.Get("X-Pluto-Event"), r, body)
	d.deliveryID = deliveryID
	d.objectID = deliveryID
	var root *state.Box
	if box, err := s.store.Box(cfg.BoxID); err == nil {
		root = box
		d.repo = box.PrimaryRepoURL
	}
	s.webhookReceived(d, root)
	timestamp := strings.TrimSpace(r.Header.Get("X-Pluto-Timestamp"))
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	delta := s.now().Sub(time.Unix(seconds, 0))
	if err != nil || delta < -5*time.Minute || delta > 5*time.Minute {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "missing or stale webhook timestamp")
		http.Error(w, "missing or stale webhook timestamp", http.StatusUnauthorized)
		return
	}
	if !validGenericSignature(cfg.Secret, timestamp, body, r.Header.Get("X-Pluto-Signature-256")) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	s.webhookVerified(d, root)
	eventType := strings.TrimSpace(r.Header.Get("X-Pluto-Event"))
	action := strings.TrimSpace(r.Header.Get("X-Pluto-Action"))
	eventID := strings.TrimSpace(r.Header.Get("X-Pluto-Event-ID"))
	if eventType == "" || !json.Valid(body) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "event type and valid JSON payload are required")
		http.Error(w, "event type and valid JSON payload are required", http.StatusBadRequest)
		return
	}
	if root == nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "registered box unavailable")
		http.Error(w, "registered box unavailable", http.StatusUnprocessableEntity)
		return
	}
	if d.test {
		s.webhookDecision(d, root, events.KindWebhookIgnored, "ignored signed local test delivery", "signed local test delivery")
		w.WriteHeader(http.StatusOK)
		return
	}
	trusted, _, _, err := trustedDefaultBranch(r.Context(), root)
	if err != nil {
		s.logf("generic webhook %s: trusted default-branch event policy unavailable: %v", source, err)
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "trusted default-branch event policy unavailable")
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	policy, allowed := trusted.Events.Generic[eventType]
	if !allowed || !policy.Allows(action) {
		s.logf("generic webhook %s: rejected unsupported event %q action %q", source, eventType, action)
		s.webhookDecision(d, root, events.KindWebhookIgnored, "ignored unsupported webhook event", fmt.Sprintf("unsupported event type or action: %s %s", eventType, action))
		http.Error(w, "unsupported event type or action", http.StatusUnprocessableEntity)
		return
	}
	revision := trusted.Hash()
	approval, err := s.store.ContractApproval(root.Project)
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "contract trust state unavailable")
		http.Error(w, "contract trust state unavailable", http.StatusInternalServerError)
		return
	}
	if approval == nil || approval.Revision != revision {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "current contract revision is not approved for unattended work")
		http.Error(w, "current contract revision is not approved for unattended work", http.StatusUnprocessableEntity)
		return
	}
	s.workItemMu.Lock()
	defer s.workItemMu.Unlock()
	if existing, found := s.queuedEvent(source, deliveryID); found {
		if existing.State == state.QueueRejected {
			s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "queue is full")
			http.Error(w, "queue is full", http.StatusServiceUnavailable)
			return
		}
		s.webhookDecision(d, root, events.KindWebhookDeduped, "deduplicated webhook delivery", "delivery already admitted")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	prompt := "Webhook " + eventType
	if action != "" {
		prompt += " " + action
	}
	actor := events.Actor{Type: "webhook", ID: deliveryID, Client: source}
	_, err = s.store.AcceptTriggeredTask(
		state.Task{Source: "webhook", Project: root.Project, Ref: eventID, BoxID: root.ID, IdempotencyKey: "webhook:" + source + ":" + deliveryID, TraceID: d.traceID, Actor: actorName(actor)},
		state.TaskRun{Job: policy.Job, Prompt: prompt, IdempotencyKey: "webhook:" + source + ":" + deliveryID, ContractRevision: revision, TrustDecision: state.ContractTrustApproved, TraceID: d.traceID, Actor: actorName(actor)},
		state.QueueItem{Source: state.QueueEvent, EventSource: source, Repo: root.PrimaryRepoURL, BoxID: root.ID, Job: policy.Job, EventID: deliveryID, ContractRevision: revision, TrustDecision: state.ContractTrustApproved, TraceID: d.traceID, Actor: actorName(actor), Event: state.EventContext{Kind: eventType, Action: action, Repo: root.PrimaryRepoURL, ObjectID: eventID, Payload: append([]byte(nil), body...)}},
		s.QueueCapacity, s.now(),
	)
	if err != nil && !errors.Is(err, state.ErrQueueFull) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "queue acceptance failed")
		http.Error(w, fmt.Sprintf("queue acceptance failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, state.ErrQueueFull) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected webhook delivery", "queue is full")
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	s.webhookDecision(d, root, events.KindWebhookAdmitted, "admitted webhook delivery", "")
	w.WriteHeader(http.StatusAccepted)
}

// queuedEvent reports whether an event delivery already has a queue record.
// It is the read side of the delivery dedupe the store also enforces.
func (s *Server) queuedEvent(source, deliveryID string) (state.QueueItem, bool) {
	items, err := s.store.Queue()
	if err != nil {
		return state.QueueItem{}, false
	}
	for _, item := range items {
		if item.Source == state.QueueEvent && item.EventSource == source && item.EventID == deliveryID {
			return item, true
		}
	}
	return state.QueueItem{}, false
}

// newUnidentifiedDeliveryID gives a delivery with no stable X-Pluto-Event-ID
// a unique internal identity, so it is accepted at-least-once instead of being
// collapsed into one deduplicated run (ADR 0003).
func newUnidentifiedDeliveryID(source string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("unidentified:%s:%d:%x", source, time.Now().UTC().UnixNano(), b)
}

func validGenericSignature(secret, timestamp string, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(sig) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Action     string `json:"action"`
	Repository struct {
		HTMLURL string `json:"html_url"`
	} `json:"repository"`
}

type pullRequestPayload struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

type issueEventPayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	} `json:"issue"`
}

func (s *Server) handleGitHubPush(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	cfg, ok := s.webhookRegistrationFor(source, state.WebhookKindGitHub)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	d := newWebhookDelivery(source, state.WebhookKindGitHub, r.Header.Get("X-GitHub-Event"), r, body)
	box, boxErr := s.store.Box(cfg.BoxID)
	if box != nil {
		d.repo = box.PrimaryRepoURL
	}
	s.webhookReceived(d, box)
	if !validGitHubSignature(cfg.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	s.webhookVerified(d, box)
	if d.test {
		s.webhookDecision(d, box, events.KindWebhookIgnored, "ignored signed local test delivery", "signed local test delivery")
		w.WriteHeader(http.StatusOK)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event != "push" && event != "pull_request" && event != "issues" {
		s.logf("webhook %s: ignoring unsupported GitHub event type %q", source, event)
		s.webhookDecision(d, box, events.KindWebhookIgnored, "ignored unsupported GitHub event", fmt.Sprintf("unsupported GitHub event type %q", event))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event == "pull_request" {
		s.handleGitHubPullRequest(w, r, cfg, source, body, d, box)
		return
	}
	if event == "issues" {
		s.handleGitHubIssue(w, r, source, body, d, box)
		return
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "missing delivery ID")
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return
	}
	d.deliveryID = delivery
	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "invalid push payload")
		http.Error(w, "invalid push payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "" {
		s.logf("webhook %s: ignoring unsupported GitHub push action %q", source, payload.Action)
		s.webhookDecision(d, box, events.KindWebhookIgnored, "ignored unsupported GitHub push action", fmt.Sprintf("unsupported GitHub push action %q", payload.Action))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if boxErr != nil || box == nil {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "registered box unavailable")
		http.Error(w, "registered box unavailable", http.StatusUnprocessableEntity)
		return
	}
	ct, _, _, err := trustedDefaultBranch(r.Context(), box)
	if err != nil {
		s.logf("webhook %s: trusted default-branch push policy unavailable: %v", source, err)
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "trusted default-branch event policy unavailable")
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	revision, ok := s.requireApprovedEventContract(w, box, ct)
	if !ok {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "current contract revision is not approved for unattended work")
		return
	}
	if ct.Events.Push == nil || ct.Events.Push.Job == "" || payload.Ref != "refs/heads/"+box.Branch || payload.Deleted {
		s.logf("webhook %s: ignoring push ref %q (deleted=%t, registered branch=%q, trusted policy configured=%t)", source, payload.Ref, payload.Deleted, box.Branch, ct.Events.Push != nil && ct.Events.Push.Job != "")
		s.webhookDecision(d, box, events.KindWebhookIgnored, "ignored GitHub push", fmt.Sprintf("push ref %q is not the registered branch or policy is unset (deleted=%t)", payload.Ref, payload.Deleted))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !validGitCommitID(payload.After) {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "push payload has an invalid commit ID")
		http.Error(w, "push payload has an invalid commit ID", http.StatusBadRequest)
		return
	}
	fetch := exec.CommandContext(r.Context(), "git", "-C", box.Worktree, "fetch", "--no-tags", "--quiet", box.PrimaryRepoURL, payload.After)
	if output, err := fetch.CombinedOutput(); err != nil {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "could not fetch pushed commit")
		http.Error(w, fmt.Sprintf("could not fetch pushed commit: %v (%s)", err, strings.TrimSpace(string(output))), http.StatusUnprocessableEntity)
		return
	}
	s.workItemMu.Lock()
	defer s.workItemMu.Unlock()
	_, deduped := s.queuedEvent(source, delivery)
	item, err := s.acceptGitHubTask(box, githubTaskEvent{
		source: source, kind: "branch", subject: box.Branch, taskRef: box.Branch,
		job: ct.Events.Push.Job, eventKind: "push", delivery: delivery, revision: revision,
		ref: payload.After, traceID: d.traceID,
		event: state.EventContext{Kind: "push", Repo: box.PrimaryRepoURL, Ref: payload.After, HeadRef: payload.Ref, ObjectID: delivery, URL: payload.Repository.HTMLURL, Payload: append([]byte(nil), body...)},
	})
	if err != nil {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "queue acceptance failed")
		http.Error(w, fmt.Sprintf("queue acceptance failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	if item.State == state.QueueRejected {
		s.webhookDecision(d, box, events.KindWebhookRejected, "rejected GitHub delivery", "queue is full")
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	if deduped {
		s.webhookDecision(d, box, events.KindWebhookDeduped, "deduplicated GitHub delivery", "delivery already admitted")
	} else {
		s.webhookDecision(d, box, events.KindWebhookAdmitted, "admitted GitHub delivery", "")
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleGitHubIssue(w http.ResponseWriter, r *http.Request, source string, body []byte, d webhookDelivery, root *state.Box) {
	var p issueEventPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Issue.Number < 1 {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "invalid issue payload")
		http.Error(w, "invalid issue payload", http.StatusBadRequest)
		return
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "missing delivery ID")
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return
	}
	d.deliveryID = delivery
	s.workItemMu.Lock()
	defer s.workItemMu.Unlock()
	queued, err := s.store.Queue()
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "event deduplication lookup failed")
		http.Error(w, "event deduplication lookup failed", http.StatusInternalServerError)
		return
	}
	for _, item := range queued {
		if item.Source == state.QueueEvent && item.EventSource == source && item.EventID == delivery {
			if item.State == state.QueueRejected {
				s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "queue is full")
				http.Error(w, "queue is full", http.StatusServiceUnavailable)
				return
			}
			s.webhookDecision(d, root, events.KindWebhookDeduped, "deduplicated GitHub issue delivery", "delivery already admitted")
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	if root == nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "registered box unavailable")
		http.Error(w, "registered box unavailable", http.StatusUnprocessableEntity)
		return
	}
	trusted, defaultRef, defaultSHA, err := trustedDefaultBranch(r.Context(), root)
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "trusted default-branch event policy unavailable")
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	revision, ok := s.requireApprovedEventContract(w, root, trusted)
	if !ok {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "current contract revision is not approved for unattended work")
		return
	}
	policy := trusted.Events.Issue
	if policy == nil || !policy.Allows(p.Action) {
		s.logf("webhook %s: ignoring issue action %q (trusted policy configured=%t)", source, p.Action, policy != nil)
		s.webhookDecision(d, root, events.KindWebhookIgnored, "ignored GitHub issue delivery", fmt.Sprintf("unsupported issue action %q", p.Action))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d.ref = defaultSHA
	itemID := strconv.Itoa(p.Issue.Number)
	repoDigest := sha256.Sum256([]byte(root.PrimaryRepoURL))
	worktree := filepath.Join(s.store.Root(), "projects", fmt.Sprintf("%s-%x-issue-%s", safeRepoName(root.Project), repoDigest[:5], itemID))
	box, created, err := s.prepareIssueBox(root, worktree, itemID, defaultSHA)
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "prepare issue box failed")
		http.Error(w, fmt.Sprintf("prepare issue box: %v", err), http.StatusUnprocessableEntity)
		return
	}
	blocked := ""
	if !created && box.Ref != defaultSHA {
		fetch := exec.Command("git", "-C", worktree, "fetch", "--no-tags", "origin", defaultSHA)
		if out, fetchErr := fetch.CombinedOutput(); fetchErr != nil {
			blocked = fmt.Sprintf("issue ref update blocked: could not fetch default branch: %v (%s)", fetchErr, strings.TrimSpace(string(out)))
		}
		status, statusErr := exec.Command("git", "-C", worktree, "status", "--porcelain", "--untracked-files=all").Output()
		if blocked != "" {
			// Preserve the specific fetch failure.
		} else if statusErr != nil {
			blocked = fmt.Sprintf("issue ref update blocked: source worktree status is unknown: %v", statusErr)
		} else if len(status) != 0 {
			blocked = "issue ref update blocked: local worktree has uncommitted changes"
		} else {
			ancestor := exec.Command("git", "-C", worktree, "merge-base", "--is-ancestor", box.Ref, defaultSHA)
			if _, err := ancestor.CombinedOutput(); err != nil {
				blocked = "issue ref update blocked: default branch is older than or diverged from the issue box"
			}
		}
	}
	if blocked != "" {
		_ = s.store.UpdateWorkItemRef(box.ID, box.Ref, blocked)
	}
	q, err := s.acceptGitHubTask(box, githubTaskEvent{
		source: source, kind: "issue", subject: itemID, taskRef: "issue/" + itemID,
		job: policy.Job, eventKind: "issue", action: p.Action, delivery: delivery, revision: revision,
		ref: defaultSHA, traceID: d.traceID,
		event: state.EventContext{Kind: "issue", Action: p.Action, Repo: root.PrimaryRepoURL, Ref: defaultSHA, HeadRef: defaultRef, ObjectID: itemID, URL: p.Issue.HTMLURL, Trusted: true, CredentialNames: append([]string(nil), policy.CredentialNames...), Payload: append([]byte(nil), body...)},
	})
	if err != nil && !errors.Is(err, state.ErrQueueFull) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "queue acceptance failed")
		http.Error(w, fmt.Sprintf("queue acceptance failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	if q != nil && blocked != "" {
		_, _ = s.store.UpdateQueueItem(q.ID, state.QueueBlocked, "", blocked, s.now())
	}
	if q != nil && q.State == state.QueueRejected {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub issue delivery", "queue is full")
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	s.webhookDecision(d, root, events.KindWebhookAdmitted, "admitted GitHub issue delivery", "")
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) prepareIssueBox(root *state.Box, worktree, number, ref string) (*state.Box, bool, error) {
	if _, err := os.Stat(filepath.Join(worktree, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
			return nil, false, err
		}
		clone := exec.Command("git", "clone", "--no-checkout", "--", root.PrimaryRepoURL, worktree)
		if out, err := clone.CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("clone repository: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		fetch := exec.Command("git", "-C", worktree, "fetch", "--no-tags", "origin", ref)
		if out, err := fetch.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktree)
			return nil, false, fmt.Errorf("fetch default branch: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		checkout := exec.Command("git", "-C", worktree, "checkout", "-B", "pluto/issue-"+number, ref)
		if out, err := checkout.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktree)
			return nil, false, fmt.Errorf("checkout issue branch: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return nil, false, err
	}
	return s.store.CreateWorkItemBox(root.Project, root.PrimaryRepoURL, "issue", number, ref, worktree)
}

func (s *Server) handleGitHubPullRequest(w http.ResponseWriter, r *http.Request, cfg webhookRegistration, source string, body []byte, d webhookDelivery, root *state.Box) {
	var p pullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil || p.Number < 1 || p.PullRequest.Head.SHA == "" {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "invalid pull request payload")
		http.Error(w, "invalid pull request payload", http.StatusBadRequest)
		return
	}
	if root == nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "registered box unavailable")
		http.Error(w, "registered box unavailable", http.StatusUnprocessableEntity)
		return
	}
	trusted, _, _, err := trustedDefaultBranch(r.Context(), root)
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "trusted default-branch event policy unavailable")
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	revision, ok := s.requireApprovedEventContract(w, root, trusted)
	if !ok {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "current contract revision is not approved for unattended work")
		return
	}
	policy := trusted.Events.PullRequest
	if policy == nil || !policy.Allows(p.Action) {
		s.webhookDecision(d, root, events.KindWebhookIgnored, "ignored GitHub pull request delivery", fmt.Sprintf("unsupported pull request action %q", p.Action))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	trustedLabel := policy.TrustedLabel
	if trustedLabel == "" {
		trustedLabel = "pluto:trusted"
	}
	trustedPR := false
	for _, label := range p.PullRequest.Labels {
		if label.Name == trustedLabel {
			trustedPR = true
			break
		}
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "missing delivery ID")
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return
	}
	d.deliveryID = delivery
	d.ref = p.PullRequest.Head.SHA
	s.workItemMu.Lock()
	defer s.workItemMu.Unlock()
	queued, err := s.store.Queue()
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "event deduplication lookup failed")
		http.Error(w, "event deduplication lookup failed", http.StatusInternalServerError)
		return
	}
	for _, item := range queued {
		if item.Source == state.QueueEvent && item.EventSource == source && item.EventID == delivery {
			if item.State == state.QueueRejected {
				s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "queue is full")
				http.Error(w, "queue is full", http.StatusServiceUnavailable)
				return
			}
			s.webhookDecision(d, root, events.KindWebhookDeduped, "deduplicated GitHub pull request delivery", "delivery already admitted")
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}
	itemID := strconv.Itoa(p.Number)
	repoDigest := sha256.Sum256([]byte(root.PrimaryRepoURL))
	worktree := filepath.Join(s.store.Root(), "projects", fmt.Sprintf("%s-%x-pr-%s", safeRepoName(root.Project), repoDigest[:5], itemID))
	box, created, err := s.preparePullRequestBox(root, worktree, itemID, p.PullRequest.Head.SHA)
	if err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "prepare pull request box failed")
		http.Error(w, fmt.Sprintf("prepare pull request box: %v", err), http.StatusUnprocessableEntity)
		return
	}
	// Persist the trusted-policy decision so environment-layer sharing scopes
	// the box the same way credential injection does. An unlabeled PR stays
	// untrusted even if its own contract opts into layer caching.
	trustClass := state.TrustClassUntrusted
	if trustedPR {
		trustClass = state.TrustClassTrusted
	}
	if _, err := s.store.SetTrustClass(box.ID, trustClass); err != nil {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "record pull request trust class failed")
		http.Error(w, fmt.Sprintf("record pull request trust class: %v", err), http.StatusInternalServerError)
		return
	}
	blocked := ""
	if !created {
		status, statusErr := exec.Command("git", "-C", worktree, "status", "--porcelain", "--untracked-files=all").Output()
		if statusErr != nil {
			blocked = fmt.Sprintf("pull request ref update blocked: source worktree status is unknown: %v", statusErr)
		} else if strings.TrimSpace(string(status)) != "" {
			blocked = "pull request ref update blocked: local worktree has uncommitted changes"
		} else if box.Ref != p.PullRequest.Head.SHA {
			fetch := exec.Command("git", "-C", worktree, "fetch", "--no-tags", "origin", p.PullRequest.Head.SHA)
			if out, err := fetch.CombinedOutput(); err != nil {
				s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "could not fetch pull request head")
				http.Error(w, fmt.Sprintf("fetch pull request head: %v (%s)", err, strings.TrimSpace(string(out))), http.StatusUnprocessableEntity)
				return
			}
			ancestor := exec.Command("git", "-C", worktree, "merge-base", "--is-ancestor", box.Ref, p.PullRequest.Head.SHA)
			if _, err := ancestor.CombinedOutput(); err != nil {
				blocked = "pull request ref update blocked: event head is older than or diverged from the box ref"
			}
			if blocked == "" {
				keepRef := exec.Command("git", "-C", worktree, "update-ref", "refs/pluto/pull/"+itemID+"/target", p.PullRequest.Head.SHA)
				if out, err := keepRef.CombinedOutput(); err != nil {
					s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "could not retain pull request target")
					http.Error(w, fmt.Sprintf("retain pull request target: %v (%s)", err, strings.TrimSpace(string(out))), http.StatusUnprocessableEntity)
					return
				}
			}
		}
	}
	if blocked != "" {
		_ = s.store.UpdateWorkItemRef(box.ID, box.Ref, blocked)
	}
	credentialNames := []string(nil)
	if trustedPR {
		credentialNames = append(credentialNames, policy.CredentialNames...)
	}
	q, err := s.acceptGitHubTask(box, githubTaskEvent{
		source: source, kind: "pull_request", subject: itemID, taskRef: "pull_request/" + itemID,
		job: policy.Job, eventKind: "pull_request", action: p.Action, delivery: delivery, revision: revision,
		ref: p.PullRequest.Head.SHA, traceID: d.traceID,
		event: state.EventContext{Kind: "pull_request", Action: p.Action, Repo: root.PrimaryRepoURL, Ref: p.PullRequest.Head.SHA, HeadRef: p.PullRequest.Head.Ref, ObjectID: itemID, URL: p.PullRequest.HTMLURL, Trusted: trustedPR, CredentialNames: credentialNames, Payload: append([]byte(nil), body...)},
	})
	if err != nil && !errors.Is(err, state.ErrQueueFull) {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "queue acceptance failed")
		http.Error(w, fmt.Sprintf("queue acceptance failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	if q != nil && blocked != "" {
		_, _ = s.store.UpdateQueueItem(q.ID, state.QueueBlocked, "", blocked, s.now())
	}
	if q != nil && q.State == state.QueueRejected {
		s.webhookDecision(d, root, events.KindWebhookRejected, "rejected GitHub pull request delivery", "queue is full")
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	s.webhookDecision(d, root, events.KindWebhookAdmitted, "admitted GitHub pull request delivery", "")
	w.WriteHeader(http.StatusAccepted)
}

func safeRepoName(name string) string {
	name = filepath.Base(filepath.Clean(name))
	name = strings.ReplaceAll(name, " ", "-")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "repo"
	}
	return name
}

func (s *Server) preparePullRequestBox(root *state.Box, worktree, number, ref string) (*state.Box, bool, error) {
	if _, err := os.Stat(filepath.Join(worktree, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
			return nil, false, err
		}
		clone := exec.Command("git", "clone", "--no-checkout", "--", root.PrimaryRepoURL, worktree)
		if out, err := clone.CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("clone repository: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		fetch := exec.Command("git", "-C", worktree, "fetch", "--no-tags", "origin", "refs/pull/"+number+"/head")
		if out, err := fetch.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktree)
			return nil, false, fmt.Errorf("fetch pull request head: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		checkout := exec.Command("git", "-C", worktree, "checkout", "-B", "pluto/pr-"+number, "FETCH_HEAD")
		if out, err := checkout.CombinedOutput(); err != nil {
			_ = os.RemoveAll(worktree)
			return nil, false, fmt.Errorf("checkout pull request head: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	} else if err != nil {
		return nil, false, err
	}
	return s.store.CreateWorkItemBox(root.Project, root.PrimaryRepoURL, "pull_request", number, ref, worktree)
}

// validGitCommitID reports whether value is a full git object id.
func validGitCommitID(value string) bool {
	return hexid.Valid(value, 40, 64)
}

func trustedDefaultBranch(ctx context.Context, box *state.Box) (*contract.Contract, string, string, error) {
	if box.PrimaryRepoURL == "" {
		return nil, "", "", errors.New("registered box has no primary repository URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ls := exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--", box.PrimaryRepoURL, "HEAD")
	out, err := ls.Output()
	if err != nil {
		return nil, "", "", fmt.Errorf("resolve remote default branch: %w", err)
	}
	defaultRef, sha := "", ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			defaultRef = fields[1]
		}
		if len(fields) == 2 && fields[1] == "HEAD" {
			sha = fields[0]
		}
	}
	if !strings.HasPrefix(defaultRef, "refs/heads/") || sha == "" {
		return nil, "", "", errors.New("remote HEAD does not identify a default branch")
	}
	fetch := exec.CommandContext(ctx, "git", "-C", box.Worktree, "fetch", "--no-tags", "--quiet", box.PrimaryRepoURL, sha)
	if output, err := fetch.CombinedOutput(); err != nil {
		return nil, "", "", fmt.Errorf("fetch default-branch commit: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	show := exec.CommandContext(ctx, "git", "-C", box.Worktree, "show", sha+":"+contract.FileName)
	data, err := show.Output()
	if err != nil {
		return nil, "", "", fmt.Errorf("read default-branch contract: %w", err)
	}
	parsed, err := contract.Parse(string(data))
	if err != nil {
		return nil, "", "", fmt.Errorf("parse default-branch contract: %w", err)
	}
	return parsed, defaultRef, sha, nil
}

func validGitHubSignature(secret string, body []byte, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || len(sig) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	return subtle.ConstantTimeCompare(sig, expected) == 1
}
