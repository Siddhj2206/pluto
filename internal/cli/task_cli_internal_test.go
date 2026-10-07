package cli

import (
	"errors"
	"testing"

	"github.com/Siddhj2206/pluto/internal/state"
)

func TestFindRunResolvesAUniquePrefix(t *testing.T) {
	task := &state.Task{ID: "abcdef0123456789", Runs: []state.TaskRun{{ID: "12345678aaaa"}}}
	run, err := findRun(task, "12345678")
	if err != nil || run == nil || run.ID != "12345678aaaa" {
		t.Fatalf("unique prefix: run=%v err=%v", run, err)
	}
}

func TestFindRunReportsAnAmbiguousPrefixAsAHint(t *testing.T) {
	task := &state.Task{ID: "abcdef0123456789", Runs: []state.TaskRun{
		{ID: "12345678aaaa"},
		{ID: "12345678bbbb"},
	}}
	run, err := findRun(task, "12345678")
	if run != nil {
		t.Fatalf("ambiguous prefix returned run %q", run.ID)
	}
	var hinted *hintError
	if !errors.As(err, &hinted) || len(hinted.next) == 0 {
		t.Fatalf("ambiguous prefix error = %v, want a hintError with a next step", err)
	}
}

func TestFindRunReportsAMissingRunAsAHint(t *testing.T) {
	task := &state.Task{ID: "abcdef0123456789", Runs: []state.TaskRun{{ID: "12345678aaaa"}}}
	run, err := findRun(task, "deadbeef")
	if run != nil {
		t.Fatalf("missing run returned %q", run.ID)
	}
	var hinted *hintError
	if !errors.As(err, &hinted) || len(hinted.next) == 0 {
		t.Fatalf("missing run error = %v, want a hintError with a next step", err)
	}
}
