package cli

import (
	"bytes"
	"errors"
	"strings"
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

// A pending scheduled run renders as in flight: its state and trigger are
// shown, but it has no exit code until it finishes.
func TestPrintRunRendersAPendingRun(t *testing.T) {
	run := &state.Run{
		ID:            "abc",
		Task:          "build",
		BoxID:         "b1",
		WorkspacePath: "/src/app",
		Trigger:       state.TriggerSchedule,
		State:         state.RunPending,
		Steps:         []state.Step{{Name: "run", Command: "make test", State: state.RunPending}},
	}
	var out bytes.Buffer
	printRun(&out, run)
	got := out.String()
	for _, want := range []string{"state:     pending", "trigger:   schedule", "step run: pending"} {
		if !strings.Contains(got, want) {
			t.Fatalf("printRun = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "exit:") {
		t.Fatalf("printRun shows an exit code for a pending run: %q", got)
	}
	if runExit(run) != "-" {
		t.Fatalf("runExit(pending) = %q, want -", runExit(run))
	}
}
