package state

import (
	"errors"
	"testing"
	"time"
)

func TestQueuePersistsAndOrdersByPriorityFIFOAndAging(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	old, err := s.Enqueue(QueueItem{Source: QueueScheduled, BoxID: "box", Job: "nightly"}, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Enqueue(QueueItem{Source: QueueEvent, BoxID: "box", Job: "test"}, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Enqueue(QueueItem{Source: QueueExplicit, BoxID: "box", Job: "build"}, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.NextQueueItem(now, time.Hour)
	if err != nil || got.ID != second.ID {
		t.Fatalf("first priority: got %#v, err %v", got, err)
	}
	got, err = s.NextQueueItem(now.Add(2*time.Hour), time.Hour)
	if err != nil || got.ID != old.ID {
		t.Fatalf("aged scheduled item: got %#v, err %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	items, err := s.Queue()
	if err != nil || len(items) != 3 {
		t.Fatalf("queue after reopen: %d items, err %v", len(items), err)
	}
	if items[0].ID != old.ID || items[1].ID != first.ID {
		t.Fatalf("FIFO order not retained: %#v", items)
	}
}

func TestEnqueueRejectsWhenQueueFullWithVisibleError(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.Enqueue(QueueItem{Source: QueueEvent, BoxID: "box"}, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Enqueue(QueueItem{Source: QueueEvent, BoxID: "box"}, 1, time.Now())
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestRecoverQueueRequeuesStartingAndFailsUnknownRunning(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	a, _ := s.Enqueue(QueueItem{Source: QueueExplicit, BoxID: "a", Job: "x"}, 10, now)
	b, _ := s.Enqueue(QueueItem{Source: QueueExplicit, BoxID: "b", Job: "y"}, 10, now)
	_, _ = s.NextQueueItem(now, time.Minute)
	_, _ = s.NextQueueItem(now, time.Minute)
	_, _ = s.UpdateQueueItem(b.ID, QueueRunning, "", "", now)
	if err := s.RecoverQueue(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	items, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if items[0].State != QueuePending || items[1].State != QueueFailed || items[1].Reason == "" {
		t.Fatalf("recovered queue=%+v (first id %s)", items, a.ID)
	}
}
