package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

// TestWebhookSourcesPersistEnvNameNotSecret proves the source record is
// durable, listable, updatable with delivery outcomes, and free of the secret
// value: only the environment variable name is stored.
func TestWebhookSourcesPersistEnvNameNotSecret(t *testing.T) {
	root := t.TempDir()
	st, err := state.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	src := state.WebhookSource{
		Source:    "production",
		Kind:      state.WebhookKindGitHub,
		BoxID:     "box-1",
		SecretEnv: "PLUTO_TEST_HOOK_SECRET",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.SaveWebhookSource(src); err != nil {
		t.Fatal(err)
	}
	got, err := st.WebhookSource("production")
	if err != nil || got == nil {
		t.Fatalf("WebhookSource = %+v, %v", got, err)
	}
	if got.Kind != state.WebhookKindGitHub || got.BoxID != "box-1" || got.SecretEnv != "PLUTO_TEST_HOOK_SECRET" {
		t.Fatalf("persisted source = %+v", got)
	}

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	updated, found, err := st.RecordWebhookOutcome("production", state.WebhookOutcomeAccepted, "delivery-1", "push", "", at)
	if err != nil || !found {
		t.Fatalf("RecordWebhookOutcome = %+v, %v, %v", updated, found, err)
	}
	if updated.Accepted != 1 || updated.LastDeliveryID != "delivery-1" || updated.LastEvent != "push" {
		t.Fatalf("accepted outcome = %+v", updated)
	}
	updated, found, err = st.RecordWebhookOutcome("production", state.WebhookOutcomeRejected, "delivery-2", "push", "invalid signature", at.Add(time.Minute))
	if err != nil || !found {
		t.Fatalf("RecordWebhookOutcome rejected = %+v, %v, %v", updated, found, err)
	}
	if updated.Rejected != 1 || updated.LastRejectionReason != "invalid signature" || updated.LastRejectedAt == nil {
		t.Fatalf("rejected outcome = %+v", updated)
	}
	if updated.LastDeliveryID != "delivery-2" {
		t.Fatalf("last delivery = %q, want the rejected delivery", updated.LastDeliveryID)
	}

	list, err := st.WebhookSources()
	if err != nil || len(list) != 1 || list[0].Accepted != 1 || list[0].Rejected != 1 {
		t.Fatalf("WebhookSources = %+v, %v", list, err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "webhooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "PLUTO_TEST_HOOK_SECRET") {
		t.Fatalf("state does not name the secret environment variable: %s", raw)
	}
	for _, secret := range []string{"super-secret-value", "s3cret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("state contains a secret value: %s", raw)
		}
	}

	removed, err := st.DeleteWebhookSource("production")
	if err != nil || !removed {
		t.Fatalf("DeleteWebhookSource = %v, %v", removed, err)
	}
	if got, err := st.WebhookSource("production"); err != nil || got != nil {
		t.Fatalf("source survived removal: %+v, %v", got, err)
	}
	if _, found, err := st.RecordWebhookOutcome("production", state.WebhookOutcomeAccepted, "delivery-3", "push", "", at); err != nil || found {
		t.Fatalf("outcome for a removed source = found %v, err %v", found, err)
	}
}
