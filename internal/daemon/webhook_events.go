package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// webhookDelivery is the small, non-secret fact set every webhook decision
// event carries. The payload body is hashed and measured, never stored.
type webhookDelivery struct {
	source     string
	kind       string
	event      string
	action     string
	deliveryID string
	repo       string
	ref        string
	objectID   string
	size       int
	hash       string
	test       bool
	traceID    string
}

func newWebhookDelivery(source, kind, event string, r *http.Request, body []byte) webhookDelivery {
	d := webhookDelivery{
		source:     source,
		kind:       kind,
		event:      strings.TrimSpace(event),
		action:     strings.TrimSpace(r.Header.Get("X-Pluto-Action")),
		deliveryID: firstHeader(r, "X-GitHub-Delivery", "X-Pluto-Event-ID"),
		size:       len(body),
		test:       r.Header.Get("X-Pluto-Webhook-Test") == "true",
		traceID:    events.NewTraceID(),
	}
	sum := sha256.Sum256(body)
	d.hash = hex.EncodeToString(sum[:])
	var payloadAction string
	d.repo, d.ref, d.objectID, payloadAction = webhookPayloadFacts(d.event, body)
	if d.action == "" {
		d.action = payloadAction
	}
	return d
}

func firstHeader(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// webhookPayloadFacts reads only the identifiers the ledger needs. It is
// deliberately separate from the typed admission parsing so a malformed
// payload still leaves an explainable rejection event.
func webhookPayloadFacts(event string, body []byte) (repo, ref, objectID, action string) {
	var p struct {
		Action     string `json:"action"`
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Number     int    `json:"number"`
		Repository struct {
			HTMLURL  string `json:"html_url"`
			FullName string `json:"full_name"`
		} `json:"repository"`
		Issue struct {
			Number int `json:"number"`
		} `json:"issue"`
		PullRequest struct {
			Number int `json:"number"`
			Head   struct {
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", "", ""
	}
	repo = p.Repository.HTMLURL
	if repo == "" {
		repo = p.Repository.FullName
	}
	action = p.Action
	switch event {
	case "push":
		ref, objectID = p.Ref, p.After
	case "issues":
		if p.Issue.Number > 0 {
			objectID = strconv.Itoa(p.Issue.Number)
		} else if p.Number > 0 {
			objectID = strconv.Itoa(p.Number)
		}
	case "pull_request":
		if p.PullRequest.Number > 0 {
			objectID = strconv.Itoa(p.PullRequest.Number)
		}
		ref = p.PullRequest.Head.SHA
	}
	return repo, ref, objectID, action
}

// recordWebhookEvent appends one delivery decision. A daemon without a ledger
// is silent, like every other record path.
func (s *Server) recordWebhookEvent(d webhookDelivery, box *state.Box, kind, level, summary, reason string) {
	if s.Events == nil {
		return
	}
	attrs := map[string]string{
		"source":      d.source,
		"delivery_id": d.deliveryID,
		"size":        strconv.Itoa(d.size),
		"sha256":      d.hash,
	}
	if d.event != "" {
		attrs["event"] = d.event
	}
	if d.action != "" {
		attrs["action"] = d.action
	}
	if d.repo != "" {
		attrs["repo"] = d.repo
	}
	if d.ref != "" {
		attrs["ref"] = d.ref
	}
	if d.objectID != "" {
		attrs["object_id"] = d.objectID
	}
	if d.test {
		attrs["test"] = "true"
	}
	s.record(events.Event{
		Kind:    kind,
		Level:   level,
		TraceID: d.traceID,
		Actor:   events.Actor{Type: d.kind, ID: d.deliveryID, Client: d.source},
		Summary: summary,
		Reason:  reason,
		Where:   events.Where{EventSource: d.source, DeliveryID: d.deliveryID},
		Attrs:   attrs,
	}, box)
}

// webhookDecision records a final admission decision and folds it into the
// source status counters.
func (s *Server) webhookDecision(d webhookDelivery, box *state.Box, kind, summary, reason string) {
	level := events.LevelInfo
	outcome := ""
	switch kind {
	case events.KindWebhookRejected:
		level, outcome = events.LevelWarn, state.WebhookOutcomeRejected
	case events.KindWebhookAdmitted:
		outcome = state.WebhookOutcomeAccepted
	case events.KindWebhookDeduped:
		outcome = state.WebhookOutcomeDeduped
	case events.KindWebhookIgnored:
		outcome = state.WebhookOutcomeIgnored
	}
	s.recordWebhookEvent(d, box, kind, level, summary, reason)
	if outcome != "" {
		s.noteWebhookOutcome(d.source, outcome, d.deliveryID, d.event, reason)
	}
}

// webhookReceived records the arrival before verification and stamps the
// source's last-delivery fields.
func (s *Server) webhookReceived(d webhookDelivery, box *state.Box) {
	name := d.event
	if name == "" {
		name = "webhook"
	}
	s.recordWebhookEvent(d, box, events.KindWebhookReceived, events.LevelInfo, "received "+name+" delivery", "")
	s.noteWebhookOutcome(d.source, "", d.deliveryID, d.event, "")
}

// webhookVerified records a valid signature.
func (s *Server) webhookVerified(d webhookDelivery, box *state.Box) {
	name := d.event
	if name == "" {
		name = "webhook"
	}
	s.recordWebhookEvent(d, box, events.KindWebhookVerified, events.LevelInfo, "verified "+name+" delivery", "")
}
