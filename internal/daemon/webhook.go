package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

type githubWebhook struct{ BoxID, Secret string }

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

// RegisterGitHubPush configures one source. Call from trusted local setup code;
// the secret is used only by the receiver and never passed to a box job.
func (s *Server) RegisterGitHubPush(source, boxID, secret string) error {
	if source == "" || boxID == "" || secret == "" {
		return errors.New("source, box ID, and webhook secret are required")
	}
	if _, err := s.store.Box(boxID); err != nil {
		return err
	}
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	s.webhooks[source] = githubWebhook{BoxID: boxID, Secret: secret}
	return nil
}

type pushPayload struct {
	Ref     string `json:"ref"`
	Deleted bool   `json:"deleted"`
	Action  string `json:"action"`
}

func (s *Server) handleGitHubPush(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	s.webhookMu.RLock()
	cfg, ok := s.webhooks[source]
	s.webhookMu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if !validGitHubSignature(cfg.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if event := r.Header.Get("X-GitHub-Event"); event != "push" {
		s.logf("webhook %s: ignoring unsupported GitHub event type %q", source, event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delivery := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if delivery == "" {
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return
	}
	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid push payload", http.StatusBadRequest)
		return
	}
	if payload.Action != "" {
		s.logf("webhook %s: ignoring unsupported GitHub push action %q", source, payload.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	box, err := s.store.Box(cfg.BoxID)
	if err != nil {
		http.Error(w, "registered box unavailable", http.StatusUnprocessableEntity)
		return
	}
	ct, err := trustedPushContract(r.Context(), box)
	if err != nil {
		s.logf("webhook %s: trusted default-branch push policy unavailable: %v", source, err)
		http.Error(w, "trusted default-branch event policy unavailable", http.StatusUnprocessableEntity)
		return
	}
	if ct.Events.Push == nil || ct.Events.Push.Job == "" || payload.Ref != "refs/heads/"+box.Branch || payload.Deleted {
		s.logf("webhook %s: ignoring push ref %q (deleted=%t, registered branch=%q, trusted policy configured=%t)", source, payload.Ref, payload.Deleted, box.Branch, ct.Events.Push != nil && ct.Events.Push.Job != "")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	item, err := s.store.Enqueue(state.QueueItem{Source: state.QueueEvent, Repo: box.PrimaryRepoURL, Ref: payload.Ref, BoxID: box.ID, Job: ct.Events.Push.Job, EventID: delivery}, s.QueueCapacity, s.now())
	if err != nil {
		http.Error(w, fmt.Sprintf("queue acceptance failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	if item.State == state.QueueRejected {
		http.Error(w, "queue is full", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// trustedPushContract always reads the policy from the remote's advertised
// default branch. The current checkout may be the triggering branch, so it is
// never used as an authority for event admission.
func trustedPushContract(ctx context.Context, box *state.Box) (*contract.Contract, error) {
	if box.PrimaryRepoURL == "" {
		return nil, errors.New("registered box has no primary repository URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ls := exec.CommandContext(ctx, "git", "ls-remote", "--symref", "--", box.PrimaryRepoURL, "HEAD")
	out, err := ls.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve remote default branch: %w", err)
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
		return nil, errors.New("remote HEAD does not identify a default branch")
	}
	fetch := exec.CommandContext(ctx, "git", "-C", box.Worktree, "fetch", "--no-tags", "--quiet", box.PrimaryRepoURL, sha)
	if output, err := fetch.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fetch default-branch commit: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	show := exec.CommandContext(ctx, "git", "-C", box.Worktree, "show", sha+":"+contract.FileName)
	data, err := show.Output()
	if err != nil {
		return nil, fmt.Errorf("read default-branch contract: %w", err)
	}
	parsed, err := contract.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse default-branch contract: %w", err)
	}
	return parsed, nil
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
