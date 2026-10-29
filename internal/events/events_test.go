package events_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/events"
)

func testEvent(kind, summary string) events.Event {
	return events.Event{
		Kind:    kind,
		Level:   events.LevelInfo,
		TraceID: "trc_0000000000000001",
		Actor:   events.Actor{Type: "cli", Client: "laptop"},
		Where:   events.Where{Host: "neptuno", BoxID: "box-1", Project: "cipher", Branch: "main"},
		Summary: summary,
	}
}

func open(t *testing.T, dir string, opts events.Options) *events.Store {
	t.Helper()
	st, err := events.OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAppendAssignsSequenceAndStableIdentity(t *testing.T) {
	st := open(t, t.TempDir(), events.Options{})

	first, err := st.Append(testEvent(events.KindJobQueued, "run requested"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	second, err := st.Append(testEvent(events.KindJobStarted, "job started"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seq = %d, %d, want 1, 2", first.Seq, second.Seq)
	}
	if !strings.HasPrefix(first.ID, "evt_") || first.ID == second.ID {
		t.Fatalf("ids = %q, %q, want distinct evt_ ids", first.ID, second.ID)
	}
	if first.Schema != events.Schema {
		t.Fatalf("schema = %d, want %d", first.Schema, events.Schema)
	}
	if first.At.Location() != time.UTC {
		t.Fatalf("at location = %v, want UTC", first.At.Location())
	}
}

func TestListNewestFirstAndFilters(t *testing.T) {
	st := open(t, t.TempDir(), events.Options{})
	for i, kind := range []string{
		events.KindJobQueued,
		events.KindJobStarted,
		events.KindQueueAccepted,
		events.KindJobDone,
	} {
		e := testEvent(kind, kind)
		if i == 2 {
			e.Where.BoxID = "box-2"
			e.TraceID = "trc_0000000000000002"
		}
		if _, err := st.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	all, err := st.List(events.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("len = %d, want 4", len(all))
	}
	if all[0].Kind != events.KindJobDone || all[3].Kind != events.KindJobQueued {
		t.Fatalf("order = %s..%s, want newest first", all[0].Kind, all[3].Kind)
	}

	byBox, err := st.List(events.Filter{BoxID: "box-2"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(byBox) != 1 || byBox[0].Kind != events.KindQueueAccepted {
		t.Fatalf("box filter = %+v, want the queue event", byBox)
	}

	byKind, err := st.List(events.Filter{KindPrefix: "job."})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(byKind) != 3 {
		t.Fatalf("kind filter len = %d, want 3", len(byKind))
	}

	byTrace, err := st.List(events.Filter{TraceID: "trc_0000000000000002"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(byTrace) != 1 {
		t.Fatalf("trace filter len = %d, want 1", len(byTrace))
	}

	limited, err := st.List(events.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(limited) != 2 || limited[0].Kind != events.KindJobDone {
		t.Fatalf("limit = %+v, want the two newest", limited)
	}

	after, err := st.List(events.Filter{AfterSeq: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(after) != 2 || after[0].Seq != 4 {
		t.Fatalf("after seq = %+v, want seq 4 and 3", after)
	}
}

func TestSequenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir, events.Options{})
	first, err := st.Append(testEvent(events.KindDaemonStarted, "daemon started"))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := open(t, dir, events.Options{})
	second, err := reopened.Append(testEvent(events.KindDaemonStopped, "daemon stopped"))
	if err != nil {
		t.Fatalf("Append after restart: %v", err)
	}
	if second.Seq != first.Seq+1 {
		t.Fatalf("seq after restart = %d, want %d", second.Seq, first.Seq+1)
	}
	all, err := reopened.List(events.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 || all[0].Seq != second.Seq || all[1].ID != first.ID {
		t.Fatalf("events after restart = %+v, want both in seq order", all)
	}
}

func TestRotationKeepsSequenceAndReadsEverySegment(t *testing.T) {
	dir := t.TempDir()
	// A tiny segment bound forces a rotation every few events.
	st := open(t, dir, events.Options{SegmentBytes: 1})
	for i := 0; i < 5; i++ {
		if _, err := st.Append(testEvent(events.KindJobQueued, "queued")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	segments := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			segments++
		}
	}
	if segments < 2 {
		t.Fatalf("segments = %d, want at least 2 after rotation", segments)
	}
	all, err := st.List(events.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("len = %d, want 5 across segments", len(all))
	}
	for i, e := range all {
		if e.Seq != uint64(5-i) {
			t.Fatalf("event %d seq = %d, want %d", i, e.Seq, 5-i)
		}
	}
}

func TestRetentionDropsOldestSegmentsByCount(t *testing.T) {
	dir := t.TempDir()
	// Each append rotates, so every event owns a segment and the count bound
	// is observable at segment granularity.
	st := open(t, dir, events.Options{SegmentBytes: 1, MaxEvents: 2})
	for i := 0; i < 6; i++ {
		if _, err := st.Append(testEvent(events.KindJobQueued, "queued")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	all, err := st.List(events.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) > 2 {
		t.Fatalf("retained %d events, want at most 2", len(all))
	}
	if len(all) == 0 {
		t.Fatal("retention dropped every event, want the newest kept")
	}
	if all[0].Seq != 6 {
		t.Fatalf("newest seq = %d, want 6", all[0].Seq)
	}
}

func TestRetentionDropsSegmentsByAge(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	clock := now
	dir := t.TempDir()
	st, err := events.OpenWithOptions(dir, events.Options{
		SegmentBytes: 1,
		MaxAge:       24 * time.Hour,
		Now:          func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("OpenWithOptions: %v", err)
	}
	defer st.Close()
	for i := 0; i < 3; i++ {
		if _, err := st.Append(testEvent(events.KindJobQueued, "queued")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	clock = now.Add(48 * time.Hour)
	if _, err := st.Append(testEvent(events.KindJobQueued, "later")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	all, err := st.List(events.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Summary != "later" {
		t.Fatalf("events = %+v, want only the fresh event after the age cutoff", all)
	}
}

func TestParseSinceAcceptsSeqDurationAndTimestamp(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	f, err := events.ParseSince("42", now)
	if err != nil {
		t.Fatalf("ParseSince seq: %v", err)
	}
	if f.AfterSeq != 42 {
		t.Fatalf("AfterSeq = %d, want 42", f.AfterSeq)
	}

	f, err = events.ParseSince("1h", now)
	if err != nil {
		t.Fatalf("ParseSince duration: %v", err)
	}
	if want := now.Add(-time.Hour); !f.Since.Equal(want) {
		t.Fatalf("Since = %v, want %v", f.Since, want)
	}

	f, err = events.ParseSince("2026-10-22T10:00:00Z", now)
	if err != nil {
		t.Fatalf("ParseSince timestamp: %v", err)
	}
	if want := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC); !f.Since.Equal(want) {
		t.Fatalf("Since = %v, want %v", f.Since, want)
	}

	if _, err := events.ParseSince("yesterday", now); err == nil {
		t.Fatal("ParseSince accepted nonsense")
	}
}

func TestSubscriptionSeesAppends(t *testing.T) {
	st := open(t, t.TempDir(), events.Options{})
	ch, cancel := st.Subscribe()
	defer cancel()

	if _, err := st.Append(testEvent(events.KindBoxCreated, "created box")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	select {
	case got := <-ch:
		if got.Kind != events.KindBoxCreated || got.Seq != 1 {
			t.Fatalf("subscribed event = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a subscribed event")
	}
}

func TestLedgerLinesAreOneJSONObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	st := open(t, dir, events.Options{})
	if _, err := st.Append(testEvent(events.KindJobQueued, "line one")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := st.Append(testEvent(events.KindJobDone, "line two")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var data []byte
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			data = append(data, b...)
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (%q)", len(lines), data)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Fatalf("line is not one JSON object: %q", line)
		}
	}
}

func TestOptionsFromEnvParsesRetention(t *testing.T) {
	opts, err := events.OptionsFromEnv(func(string) string { return "age=2h,count=50" })
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.MaxAge != 2*time.Hour || opts.MaxEvents != 50 {
		t.Fatalf("opts = %+v, want age 2h and count 50", opts)
	}

	opts, err = events.OptionsFromEnv(func(string) string { return "age=0,count=0" })
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.MaxAge >= 0 || opts.MaxEvents >= 0 {
		t.Fatalf("opts = %+v, want both bounds disabled", opts)
	}

	if _, err := events.OptionsFromEnv(func(string) string { return "size=1" }); err == nil {
		t.Fatal("OptionsFromEnv accepted an unknown key")
	}
	if _, err := events.OptionsFromEnv(func(string) string { return "age=soon" }); err == nil {
		t.Fatal("OptionsFromEnv accepted an invalid duration")
	}
}
