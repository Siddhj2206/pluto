package host

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/github"
	"github.com/Siddhj2206/pluto/internal/store"
)

// Reporting push runs to the commit they ran for, as GitHub commit statuses
// (ADR 0011). It never holds up a run: it runs on its own, and a status that
// can't be sent is only logged and tried again later.

const (
	// reportWindow is how far back a run's result is still worth telling GitHub.
	reportWindow = 7 * 24 * time.Hour
	// reportRetry is the least time between tries for a run GitHub didn't take.
	reportRetry = time.Minute
	// needsStatusWrite explains a status GitHub refused to take.
	needsStatusWrite = "couldn't report to GitHub: the token needs Commit statuses: write"
	// cantSeeCommit explains a status for a commit GitHub says isn't there.
	cantSeeCommit = "couldn't report to GitHub: the token can't see this repo or commit"
)

// commitStatus maps a run to the state and description of its commit status.
func commitStatus(r store.Run) (state, description string) {
	reason := strings.Join(strings.Fields(r.Reason), " ")
	withReason := func(what string) string {
		if reason == "" {
			return what
		}
		return what + ": " + reason
	}
	switch r.State {
	case store.StateQueued:
		return "pending", "Queued"
	case store.StateWaiting, store.StateRunning:
		return "pending", "Running"
	case store.StatePassed:
		if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
			return "success", "Passed"
		}
		return "success", "Passed in " + shortDuration(r.FinishedAt.Sub(r.StartedAt))
	case store.StateFailed:
		return "failure", withReason("Failed")
	case store.StateCancelled:
		return "error", withReason("Cancelled")
	case store.StateLost:
		return "error", withReason("Lost")
	case store.StateSkipped:
		return "error", withReason("Skipped")
	case store.StateRefused:
		if reason == "" {
			return "error", "Refused"
		}
		return "error", reason
	}
	return "error", withReason(r.State)
}

// shortDuration writes a run's time the way the log's step lines do: 41s,
// 2m05s, 1h02m.
func shortDuration(d time.Duration) string {
	n := int(d.Round(time.Second) / time.Second)
	switch {
	case n < 60:
		return fmt.Sprintf("%ds", n)
	case n < 3600:
		return fmt.Sprintf("%dm%02ds", n/60, n%60)
	}
	return fmt.Sprintf("%dh%02dm", n/3600, n%3600/60)
}

// startReporting sends pending commit statuses in the background. Finding them
// is a quick local query, so most passes find none and start nothing.
func (h *Host) startReporting(ctx context.Context) {
	h.mu.Lock()
	busy := h.reporting
	h.mu.Unlock()
	if busy {
		return
	}
	runs, err := h.runsToReport(ctx)
	if err != nil {
		log.Printf("finding runs to report to GitHub: %v", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	h.mu.Lock()
	h.reporting = true
	h.mu.Unlock()
	h.prepares.Add(1)
	go func() {
		defer h.prepares.Done()
		if err := h.reportRuns(ctx, runs); err != nil && ctx.Err() == nil {
			log.Printf("reporting to GitHub: %v", err)
		}
		h.mu.Lock()
		h.reporting = false
		h.mu.Unlock()
	}()
}

// runsToReport lists the recent push runs GitHub needs to hear about now.
func (h *Host) runsToReport(ctx context.Context) ([]store.Run, error) {
	runs, err := h.Store.PushRunsToReport(ctx, h.now().Add(-reportWindow))
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(runs, func(r store.Run) bool { return !h.reportDue(r.ID) }), nil
}

// reportPushRuns tells GitHub about every recent push run whose state has moved
// since it last did.
func (h *Host) reportPushRuns(ctx context.Context) error {
	runs, err := h.runsToReport(ctx)
	if err != nil {
		return err
	}
	return h.reportRuns(ctx, runs)
}

func (h *Host) reportRuns(ctx context.Context, runs []store.Run) error {
	var errs []error
	for _, r := range runs {
		if err := h.reportRun(ctx, r); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (h *Host) reportDue(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.now().Before(h.reportWait[id])
}

func (h *Host) reportLater(id string, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reportWait == nil {
		h.reportWait = map[string]time.Time{}
	}
	h.reportWait[id] = at
}

func (h *Host) reportDone(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.reportWait, id)
}

func (h *Host) reportRun(ctx context.Context, r store.Run) error {
	if r.State == store.StateRefused {
		// A refusal is Pluto declining to run, not a verdict on the commit. A red
		// status would fail every bot's pull request, so GitHub hears nothing and
		// pluto history has the reason.
		return h.Store.MarkReported(ctx, r.ID, r.State, "")
	}
	state, description := commitStatus(r)
	if r.Reported != "" {
		// A run going from queued to waiting reads the same on GitHub, so the
		// request would only repeat what GitHub already shows.
		was := r
		was.State = r.Reported
		if oldState, oldDescription := commitStatus(was); oldState == state && oldDescription == description {
			return h.Store.MarkReported(ctx, r.ID, r.State, "")
		}
	}
	if r.SHA == "" {
		return h.Store.MarkReported(ctx, r.ID, r.State, "") // Nothing to put a status on.
	}
	repo, err := h.Store.Repo(ctx, r.Repo)
	if err != nil {
		return err
	}
	err = h.GitHub(repo.Token).SetStatus(ctx, r.Repo, r.SHA, state, "pluto/"+r.Job, description)
	var refused *github.StatusError
	switch {
	case err == nil:
		h.reportDone(r.ID)
		return h.Store.MarkReported(ctx, r.ID, r.State, "")
	case errors.As(err, &refused) && (refused.Code == http.StatusForbidden || refused.Code == http.StatusNotFound || refused.Code == http.StatusUnprocessableEntity):
		// Asking again won't change the answer, so say so on the run and stop.
		note := needsStatusWrite
		switch refused.Code {
		case http.StatusNotFound:
			note = cantSeeCommit
		case http.StatusUnprocessableEntity:
			note = "couldn't report to GitHub: " + err.Error()
		}
		if strings.Contains(r.Note, note) {
			note = "" // Said already, on an earlier state.
		}
		h.reportDone(r.ID)
		return h.Store.MarkReported(ctx, r.ID, r.State, note)
	}
	h.reportLater(r.ID, h.now().Add(reportRetry))
	return err
}
