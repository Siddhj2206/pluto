package daemon

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/state"
)

// webhookRegistration is one live inbound source. Secret is the resolved
// value held only in memory; SecretEnv is the name persisted in host state.
type webhookRegistration struct {
	Source    string
	Kind      string
	BoxID     string
	Secret    string
	SecretEnv string
	Persisted bool

	LastDeliveryAt      *time.Time
	LastDeliveryID      string
	LastEvent           string
	LastOutcome         string
	Accepted            int
	Rejected            int
	LastRejectedAt      *time.Time
	LastRejectionReason string
}

func (r webhookRegistration) endpoint() string {
	return "/" + r.Kind + "/" + r.Source
}

// sourceNameRule keeps a source safe as one URL path segment and a state key.
var sourceNameRule = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// webhookRegistrationFor returns the live source for one route kind. A source
// registered for the other kind is absent, so the generic and GitHub routes
// stay separate.
func (s *Server) webhookRegistrationFor(source, kind string) (webhookRegistration, bool) {
	s.webhookMu.RLock()
	defer s.webhookMu.RUnlock()
	reg, ok := s.webhookRegs[source]
	if !ok || reg.Kind != kind {
		return webhookRegistration{}, false
	}
	return reg, true
}

// webhookRegistrationAny returns the live source whichever kind registered it,
// for the test-delivery surface.
func (s *Server) webhookRegistrationAny(source string) (webhookRegistration, bool) {
	s.webhookMu.RLock()
	defer s.webhookMu.RUnlock()
	reg, ok := s.webhookRegs[source]
	return reg, ok
}

// RegisterGitHubPush configures one source with an explicit secret. Call from
// trusted local setup code; the secret is used only by the receiver and never
// passed to a box job.
func (s *Server) RegisterGitHubPush(source, boxID, secret string) error {
	return s.registerRuntimeWebhook(state.WebhookKindGitHub, source, boxID, secret, "")
}

// RegisterGenericWebhook configures an authenticated non-GitHub event source.
// The secret is only used at ingress; it is never included in queued work.
func (s *Server) RegisterGenericWebhook(source, boxID, secret string) error {
	return s.registerRuntimeWebhook(state.WebhookKindGeneric, source, boxID, secret, "")
}

// RegisterWebhookRuntime registers a flag-configured source, resolving the
// secret from the daemon environment. It is the daemon-flag path; `pluto
// webhook add` goes through the persisted API instead.
func (s *Server) RegisterWebhookRuntime(kind, source, boxID, secretEnv string) error {
	if !state.ValidWebhookKind(kind) {
		return fmt.Errorf("unknown webhook kind %q", kind)
	}
	return s.registerRuntimeWebhook(kind, source, boxID, os.Getenv(secretEnv), secretEnv)
}

func (s *Server) registerRuntimeWebhook(kind, source, boxID, secret, secretEnv string) error {
	if source == "" || boxID == "" || secret == "" {
		return errors.New("source, box ID, and webhook secret are required")
	}
	if !state.ValidWebhookKind(kind) {
		return fmt.Errorf("unknown webhook kind %q", kind)
	}
	if !sourceNameRule.MatchString(source) {
		return errors.New("webhook source may contain only letters, digits, dot, underscore, and dash")
	}
	if _, err := s.store.Box(boxID); err != nil {
		return err
	}
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	reg := webhookRegistration{
		Source: source, Kind: kind, BoxID: boxID, Secret: secret, SecretEnv: secretEnv,
	}
	// A flag can re-register a persisted source. Keep its durable flag and
	// delivery history so later outcomes still reach webhooks.json.
	if old, ok := s.webhookRegs[source]; ok {
		reg.Persisted = old.Persisted
		reg.LastDeliveryAt = old.LastDeliveryAt
		reg.LastDeliveryID = old.LastDeliveryID
		reg.LastEvent = old.LastEvent
		reg.LastOutcome = old.LastOutcome
		reg.Accepted = old.Accepted
		reg.Rejected = old.Rejected
		reg.LastRejectedAt = old.LastRejectedAt
		reg.LastRejectionReason = old.LastRejectionReason
	}
	s.webhookRegs[source] = reg
	return nil
}

// RestoreWebhookSources loads persisted sources at daemon start. A source
// whose secret environment variable is absent stays listed but is not
// registered, so it can never verify an empty-key signature.
func (s *Server) RestoreWebhookSources() error {
	sources, err := s.store.WebhookSources()
	if err != nil {
		return err
	}
	for _, src := range sources {
		if !state.ValidWebhookKind(src.Kind) {
			s.logf("webhook source %s: unknown kind %q; source is listed but not receiving", src.Source, src.Kind)
			continue
		}
		secret := os.Getenv(src.SecretEnv)
		if secret == "" {
			s.logf("webhook source %s: environment variable %s is not set; source is listed but not receiving", src.Source, src.SecretEnv)
			continue
		}
		if _, err := s.store.Box(src.BoxID); err != nil {
			s.logf("webhook source %s: registered box %s is unavailable: %v", src.Source, src.BoxID, err)
			continue
		}
		s.webhookMu.Lock()
		s.webhookRegs[src.Source] = registrationFromSource(src, secret)
		s.webhookMu.Unlock()
	}
	return nil
}

func registrationFromSource(src state.WebhookSource, secret string) webhookRegistration {
	return webhookRegistration{
		Source: src.Source, Kind: src.Kind, BoxID: src.BoxID, Secret: secret,
		SecretEnv: src.SecretEnv, Persisted: true,
		LastDeliveryAt: src.LastDeliveryAt, LastDeliveryID: src.LastDeliveryID,
		LastEvent: src.LastEvent, LastOutcome: src.LastOutcome,
		Accepted: src.Accepted, Rejected: src.Rejected,
		LastRejectedAt: src.LastRejectedAt, LastRejectionReason: src.LastRejectionReason,
	}
}

// WebhookSources merges persisted and flag-registered sources into the
// redacted status list. It never returns a secret.
func (s *Server) WebhookSources() ([]api.WebhookSourceStatus, error) {
	persisted, err := s.store.WebhookSources()
	if err != nil {
		return nil, err
	}
	bySource := make(map[string]api.WebhookSourceStatus, len(persisted))
	for _, src := range persisted {
		status := statusFromSource(src)
		status.SecretPresent = os.Getenv(src.SecretEnv) != ""
		bySource[src.Source] = status
	}
	s.webhookMu.RLock()
	for source, reg := range s.webhookRegs {
		if existing, ok := bySource[source]; ok {
			existing.SecretPresent = reg.Secret != ""
			bySource[source] = existing
			continue
		}
		bySource[source] = statusFromRegistration(reg)
	}
	s.webhookMu.RUnlock()
	out := make([]api.WebhookSourceStatus, 0, len(bySource))
	for _, status := range bySource {
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

func statusFromSource(src state.WebhookSource) api.WebhookSourceStatus {
	return api.WebhookSourceStatus{
		Source: src.Source, Kind: src.Kind, BoxID: src.BoxID,
		Endpoint: "/" + src.Kind + "/" + src.Source, SecretEnv: src.SecretEnv,
		Persisted: true, LastDeliveryAt: src.LastDeliveryAt,
		LastDeliveryID: src.LastDeliveryID, LastEvent: src.LastEvent,
		LastOutcome: src.LastOutcome, Accepted: src.Accepted, Rejected: src.Rejected,
		LastRejectedAt: src.LastRejectedAt, LastRejectionReason: src.LastRejectionReason,
	}
}

func statusFromRegistration(reg webhookRegistration) api.WebhookSourceStatus {
	return api.WebhookSourceStatus{
		Source: reg.Source, Kind: reg.Kind, BoxID: reg.BoxID,
		Endpoint: reg.endpoint(), SecretEnv: reg.SecretEnv, SecretPresent: reg.Secret != "",
		Persisted: reg.Persisted, LastDeliveryAt: reg.LastDeliveryAt,
		LastDeliveryID: reg.LastDeliveryID, LastEvent: reg.LastEvent,
		LastOutcome: reg.LastOutcome, Accepted: reg.Accepted, Rejected: reg.Rejected,
		LastRejectedAt: reg.LastRejectedAt, LastRejectionReason: reg.LastRejectionReason,
	}
}

// noteWebhookOutcome updates the live counters and, for a persisted source,
// the durable record. outcome "" records the delivery without deciding it.
func (s *Server) noteWebhookOutcome(source, outcome, deliveryID, event, reason string) {
	now := s.now().UTC()
	persisted := false
	s.webhookMu.Lock()
	if reg, ok := s.webhookRegs[source]; ok {
		reg.LastDeliveryAt = &now
		reg.LastDeliveryID = deliveryID
		reg.LastEvent = event
		reg.LastOutcome = outcome
		switch outcome {
		case state.WebhookOutcomeAccepted, state.WebhookOutcomeDeduped:
			reg.Accepted++
		case state.WebhookOutcomeRejected:
			reg.Rejected++
			reg.LastRejectedAt = &now
			reg.LastRejectionReason = reason
		}
		persisted = reg.Persisted
		s.webhookRegs[source] = reg
	}
	s.webhookMu.Unlock()
	if !persisted {
		return
	}
	if _, _, err := s.store.RecordWebhookOutcome(source, outcome, deliveryID, event, reason, now); err != nil {
		s.logf("webhook source %s: persist delivery outcome: %v", source, err)
	}
}

// handleWebhookList serves the redacted source listing.
func (s *Server) handleWebhookList(w http.ResponseWriter, r *http.Request) {
	list, err := s.WebhookSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []api.WebhookSourceStatus{}
	}
	writeJSON(w, http.StatusOK, api.WebhookListResponse{SchemaVersion: 1, Webhooks: list})
}

// handleWebhookAdd persists a source and registers it in the running daemon.
// The secret is resolved from the daemon's environment by name.
func (s *Server) handleWebhookAdd(w http.ResponseWriter, r *http.Request) {
	var req api.WebhookAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if !state.ValidWebhookKind(req.Kind) {
		writeError(w, http.StatusBadRequest, errors.New("webhook kind must be github or generic"))
		return
	}
	if !sourceNameRule.MatchString(req.Source) {
		writeError(w, http.StatusBadRequest, errors.New("webhook source may contain only letters, digits, dot, underscore, and dash"))
		return
	}
	if req.BoxID == "" || req.SecretEnv == "" {
		writeError(w, http.StatusBadRequest, errors.New("box and secret environment variable are required"))
		return
	}
	if _, err := s.store.Box(req.BoxID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("registered box unavailable: %w", err))
		return
	}
	secret := os.Getenv(req.SecretEnv)
	if secret == "" {
		writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("environment variable %s is not set in the daemon environment", req.SecretEnv))
		return
	}
	existing, err := s.store.WebhookSource(req.Source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	src := state.WebhookSource{
		Source: req.Source, Kind: req.Kind, BoxID: req.BoxID, SecretEnv: req.SecretEnv,
		CreatedAt: s.now().UTC(),
	}
	if existing != nil {
		src = *existing
		src.Kind, src.BoxID, src.SecretEnv = req.Kind, req.BoxID, req.SecretEnv
	}
	if err := s.store.SaveWebhookSource(src); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.registerRuntimeWebhook(req.Kind, req.Source, req.BoxID, secret, req.SecretEnv); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	s.webhookMu.Lock()
	if reg, ok := s.webhookRegs[req.Source]; ok {
		reg.Persisted = true
		s.webhookRegs[req.Source] = reg
	}
	s.webhookMu.Unlock()
	status := statusFromSource(src)
	status.SecretPresent = true
	writeJSON(w, http.StatusCreated, status)
}

// handleWebhookRemove removes the source from host state and from the running
// daemon. A flag-registered source can be removed at runtime; the flag will
// restore it on the next daemon start.
func (s *Server) handleWebhookRemove(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	removed, err := s.store.DeleteWebhookSource(source)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.webhookMu.Lock()
	_, live := s.webhookRegs[source]
	delete(s.webhookRegs, source)
	s.webhookMu.Unlock()
	if !removed && !live {
		writeError(w, http.StatusNotFound, errors.New("webhook source not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWebhookTest sends one signed local delivery through the real handler.
// It is labelled as a test and can never admit work, so it proves the secret
// and route without pretending to be a GitHub delivery.
func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	reg, ok := s.webhookRegistrationAny(source)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("webhook source not found or its secret environment variable is unset"))
		return
	}
	delivery := newTestDeliveryID()
	body := []byte(`{"zen":"pluto signed local test delivery","test":true}`)
	var req *http.Request
	switch reg.Kind {
	case state.WebhookKindGitHub:
		req = httptest.NewRequest("POST", "/github/"+source, bytes.NewReader(body))
		req.SetPathValue("source", source)
		req.Header.Set("X-GitHub-Event", "ping")
		req.Header.Set("X-GitHub-Delivery", delivery)
		mac := hmac.New(sha256.New, []byte(reg.Secret))
		_, _ = mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	case state.WebhookKindGeneric:
		timestamp := strconv.FormatInt(s.now().Unix(), 10)
		req = httptest.NewRequest("POST", "/generic/"+source, bytes.NewReader(body))
		req.SetPathValue("source", source)
		req.Header.Set("X-Pluto-Event", "test")
		req.Header.Set("X-Pluto-Event-ID", delivery)
		req.Header.Set("X-Pluto-Timestamp", timestamp)
		mac := hmac.New(sha256.New, []byte(reg.Secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		req.Header.Set("X-Pluto-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	default:
		writeError(w, http.StatusInternalServerError, errors.New("unknown webhook kind"))
		return
	}
	req.Header.Set("X-Pluto-Webhook-Test", "true")
	rec := httptest.NewRecorder()
	s.WebhookHandler().ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code > 299 {
		writeError(w, http.StatusBadGateway, fmt.Errorf("test delivery rejected: %s", strings.TrimSpace(rec.Body.String())))
		return
	}
	writeJSON(w, http.StatusOK, api.WebhookTestResponse{
		Source: source, Kind: reg.Kind, Endpoint: reg.endpoint(), DeliveryID: delivery,
		Status: rec.Code, Verified: true, Outcome: state.WebhookOutcomeIgnored,
		Reason: "signed local test delivery",
	})
}

func newTestDeliveryID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("test-%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(b[:]))
}
