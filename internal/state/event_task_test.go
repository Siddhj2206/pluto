package state_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestEventDeliveryCreatesStableTaskAndAppendsRuns(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "state")
	st, err := state.Open(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	box, _, err := st.CreateBox("repo", "main", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	accept := func(delivery, action string) *state.Task {
		t.Helper()
		task, err := st.AcceptTriggeredTask(
			state.Task{Source: "github", Project: "repo", Ref: "refs/heads/main", BoxID: box.ID, IdempotencyKey: "github:repo:branch:main"},
			state.TaskRun{Job: "test", Prompt: "GitHub push: " + action, IdempotencyKey: "github:source:" + delivery},
			state.QueueItem{Source: state.QueueEvent, EventSource: "source", EventID: delivery, Repo: "repo", Job: "test", Event: state.EventContext{Kind: "push", Action: action}},
			10,
			now,
		)
		if err != nil {
			t.Fatalf("AcceptTriggeredTask(%q): %v", delivery, err)
		}
		return task
	}

	first := accept("delivery-1", "first")
	duplicate := accept("delivery-1", "first")
	if duplicate.ID != first.ID || len(duplicate.Runs) != 1 || duplicate.Runs[0].ID != first.Runs[0].ID {
		t.Fatalf("duplicate delivery created work: first=%+v duplicate=%+v", first, duplicate)
	}
	second := accept("delivery-2", "second")
	if second.ID != first.ID || len(second.Runs) != 2 || second.Runs[1].IdempotencyKey != "github:source:delivery-2" || second.Runs[1].EventSource != "source" || second.Runs[1].Event.Kind != "push" {
		t.Fatalf("later delivery did not append to branch task: %+v", second)
	}
	items, err := st.Queue()
	if err != nil || len(items) != 2 {
		t.Fatalf("queue=%+v err=%v, want one item per unique delivery", items, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = state.Open(storeDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	recovered := accept("delivery-2", "second")
	if recovered.ID != first.ID || len(recovered.Runs) != 2 || recovered.Runs[1].Event == nil || recovered.Runs[1].Event.Kind != "push" {
		t.Fatalf("reopened event task=%+v, want stable identity and trigger context", recovered)
	}
}
