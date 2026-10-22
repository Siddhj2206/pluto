package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Webhook source kinds. The kind selects the inbound route and the signature
// scheme; it is never inferred from the source name.
const (
	WebhookKindGitHub  = "github"
	WebhookKindGeneric = "generic"
)

// Webhook delivery outcomes. Accepted and deduped count as accepted because
// the delivery resolved to one admitted run; ignored deliveries were verified
// but intentionally produced no work.
const (
	WebhookOutcomeAccepted = "accepted"
	WebhookOutcomeRejected = "rejected"
	WebhookOutcomeDeduped  = "deduped"
	WebhookOutcomeIgnored  = "ignored"
)

// WebhookSource is one persisted inbound event source. SecretEnv names an
// environment variable in the daemon; the secret value is never stored.
// Delivery counters and the last rejection are part of the same record so
// `pluto webhook ls` survives a daemon restart.
type WebhookSource struct {
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	BoxID     string    `json:"box_id"`
	SecretEnv string    `json:"secret_env"`
	CreatedAt time.Time `json:"created_at"`

	LastDeliveryAt      *time.Time `json:"last_delivery_at,omitempty"`
	LastDeliveryID      string     `json:"last_delivery_id,omitempty"`
	LastEvent           string     `json:"last_event,omitempty"`
	LastOutcome         string     `json:"last_outcome,omitempty"`
	Accepted            int        `json:"accepted"`
	Rejected            int        `json:"rejected"`
	LastRejectedAt      *time.Time `json:"last_rejected_at,omitempty"`
	LastRejectionReason string     `json:"last_rejection_reason,omitempty"`
}

const webhookRecordSchema = 1

type webhookRecord struct {
	Schema  int             `json:"schema"`
	Sources []WebhookSource `json:"sources"`
}

// ValidWebhookKind reports whether kind names a supported inbound source.
func ValidWebhookKind(kind string) bool {
	return kind == WebhookKindGitHub || kind == WebhookKindGeneric
}

// WebhookSources returns the persisted sources in stable source order.
func (s *Store) WebhookSources() ([]WebhookSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readWebhooksLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(record.Sources, func(i, j int) bool { return record.Sources[i].Source < record.Sources[j].Source })
	return record.Sources, nil
}

// WebhookSource returns one persisted source, or nil when it is absent.
func (s *Store) WebhookSource(source string) (*WebhookSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readWebhooksLocked()
	if err != nil {
		return nil, err
	}
	for i := range record.Sources {
		if record.Sources[i].Source == source {
			copy := record.Sources[i]
			return &copy, nil
		}
	}
	return nil, nil
}

// SaveWebhookSource inserts or replaces one source. A caller that re-adds an
// existing source starts from WebhookSource so its delivery history is kept.
func (s *Store) SaveWebhookSource(source WebhookSource) error {
	if source.Source == "" || !ValidWebhookKind(source.Kind) || source.BoxID == "" || source.SecretEnv == "" {
		return errors.New("webhook source, kind, box, and secret environment variable are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readWebhooksLocked()
	if err != nil {
		return err
	}
	for i := range record.Sources {
		if record.Sources[i].Source != source.Source {
			continue
		}
		if source.CreatedAt.IsZero() {
			source.CreatedAt = record.Sources[i].CreatedAt
		}
		record.Sources[i] = source
		return s.writeWebhooksLocked(record)
	}
	record.Sources = append(record.Sources, source)
	return s.writeWebhooksLocked(record)
}

// DeleteWebhookSource removes one source. It reports whether a record existed.
func (s *Store) DeleteWebhookSource(source string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readWebhooksLocked()
	if err != nil {
		return false, err
	}
	for i := range record.Sources {
		if record.Sources[i].Source != source {
			continue
		}
		record.Sources = append(record.Sources[:i], record.Sources[i+1:]...)
		return true, s.writeWebhooksLocked(record)
	}
	return false, nil
}

// RecordWebhookOutcome folds one delivery decision into the persisted source.
// A source that is not persisted is left alone (found=false), so a
// flag-registered source keeps its counters in the daemon's memory instead.
func (s *Store) RecordWebhookOutcome(source, outcome, deliveryID, event, reason string, at time.Time) (WebhookSource, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.readWebhooksLocked()
	if err != nil {
		return WebhookSource{}, false, err
	}
	for i := range record.Sources {
		item := &record.Sources[i]
		if item.Source != source {
			continue
		}
		stamp := at.UTC()
		item.LastDeliveryAt = &stamp
		item.LastDeliveryID = deliveryID
		item.LastEvent = event
		item.LastOutcome = outcome
		switch outcome {
		case WebhookOutcomeAccepted, WebhookOutcomeDeduped:
			item.Accepted++
		case WebhookOutcomeRejected:
			item.Rejected++
			item.LastRejectedAt = &stamp
			item.LastRejectionReason = reason
		}
		copy := *item
		return copy, true, s.writeWebhooksLocked(record)
	}
	return WebhookSource{}, false, nil
}

func (s *Store) readWebhooksLocked() (webhookRecord, error) {
	path := filepath.Join(s.root, "webhooks.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return webhookRecord{Schema: webhookRecordSchema}, nil
	}
	if err != nil {
		return webhookRecord{}, err
	}
	var record webhookRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return webhookRecord{}, err
	}
	if record.Schema != webhookRecordSchema {
		return webhookRecord{}, errors.New("unsupported webhook source record schema")
	}
	return record, nil
}

func (s *Store) writeWebhooksLocked(record webhookRecord) error {
	record.Schema = webhookRecordSchema
	if record.Sources == nil {
		record.Sources = []WebhookSource{}
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.root, "webhooks.json"), data, 0o600)
}
