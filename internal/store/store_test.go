package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestRunRoundTrip(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	in := Run{ID: "r_1", Repo: "o/r", Box: "b", RunnerID: 7, State: StateFailed, Reason: "x", GitHubRunID: 9,
		Workflow: "ci", Job: "j", Event: "push", Ref: "refs/heads/m", SHA: "s", Workspace: "/w", BoxKept: true,
		CreatedAt: now, StartedAt: now, FinishedAt: now, KeepUntil: now.Add(time.Hour)}
	if err := s.PutRun(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, "r_1")
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("got %+v\nwant %+v", got, in)
	}
	if _, err := s.Run(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLiveAndRunsFilter(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	for i, r := range []Run{
		{ID: "wait", State: StateWaiting}, {ID: "run", State: StateRunning},
		{ID: "kept", State: StateFailed, BoxKept: true}, {ID: "done", State: StatePassed},
		{ID: "expired", State: StateExpired},
	} {
		r.CreatedAt = at.Add(time.Duration(i) * time.Minute)
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	live, err := s.Live(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 3 {
		t.Fatalf("live = %+v", live)
	}
	runs, err := s.Runs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 || runs[0].ID != "done" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestPruneKeepsRecentActiveAndKeptRuns(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0)
	put := func(r Run, minute int) {
		r.CreatedAt = at.Add(time.Duration(minute) * time.Minute)
		if err := s.PutRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	put(Run{ID: "old-kept", State: StateFailed, BoxKept: true}, 0)
	put(Run{ID: "old-running", State: StateRunning}, 1)
	for i := range 5 {
		put(Run{ID: fmt.Sprintf("done-%d", i), State: StatePassed}, 2+i)
	}
	if err := s.Prune(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"old-kept": true, "old-running": true, "done-4": true, "done-3": true, "done-2": false, "done-0": false} {
		_, err := s.Run(ctx, id)
		if (err == nil) != want {
			t.Errorf("%s: exists = %v, want %v", id, err == nil, want)
		}
	}
}

func TestHostIDIsStable(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	calls := 0
	gen := func() string { calls++; return fmt.Sprintf("id%d", calls) }
	a, err := s.HostID(ctx, gen)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.HostID(ctx, gen)
	if err != nil || a != b || calls != 1 {
		t.Fatalf("%s %s %d %v", a, b, calls, err)
	}
}

func TestRepoLookupAndReplace(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.Repo(ctx, "o/r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	for _, tok := range []string{"one", "two"} {
		if err := s.PutRepo(ctx, Repo{Name: "o/r", Token: tok, ConnectedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Repo(ctx, "o/r")
	if err != nil || r.Token != "two" {
		t.Fatalf("%+v %v", r, err)
	}
}
