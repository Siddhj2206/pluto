package daemon

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Overview defaults. The overview is one cheap local read: a handful of box
// records, the queue, the schedules, the redacted webhook sources, and a short
// event tail. It is not a metrics pipeline.
const (
	DefaultOverviewEvents = 20
	MaxOverviewEvents     = 100
	// maxQueueProblems bounds the blocked/rejected list so a long-lived host
	// with a full queue cannot grow the screen without bound.
	maxQueueProblems = 10
)

// handleOverview serves the one aggregate read behind `pluto overview` and
// `pluto watch`. Everything comes from the existing store, schedule
// projection, webhook registry, and ledger; there is no second store.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	limit := DefaultOverviewEvents
	if raw := r.URL.Query().Get("events"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("events must be a positive integer"))
			return
		}
		if n > MaxOverviewEvents {
			n = MaxOverviewEvents
		}
		limit = n
	}

	boxes, _, err := s.store.Boxes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	queue, err := s.store.Queue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	webhooks, err := s.WebhookSources()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := s.now()

	started := s.StartedAt
	out := api.OverviewResponse{
		SchemaVersion: 1,
		Daemon: api.DaemonStatus{
			Status:       "ok",
			Version:      s.version,
			Host:         s.host,
			StateVersion: state.StateVersion,
			StateDir:     s.store.Root(),
			Socket:       s.socketPath(),
			StartedAt:    started,
			Now:          now,
			UptimeMS:     s.uptime().Milliseconds(),
		},
		Host:         hostStatus(),
		Boxes:        boxCounts(boxes, s.MaxRunningBoxes),
		BoxSummaries: boxSummaries(boxes, now),
		Queue:        queueStatus(queue, s.QueueCapacity),
		ActiveJobs:   activeJobs(boxes),
		Schedules:    scheduleInfos(boxes, now),
		Webhooks:     webhooks,
		RecentEvents: []events.Event{},
	}
	if s.Events != nil {
		list, err := s.Events.List(events.Filter{Limit: limit})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if list != nil {
			out.RecentEvents = list
		}
	}
	if out.BoxSummaries == nil {
		out.BoxSummaries = []api.BoxSummary{}
	}
	if out.ActiveJobs == nil {
		out.ActiveJobs = []api.ActiveJob{}
	}
	if out.Schedules == nil {
		out.Schedules = []api.ScheduleInfo{}
	}
	if out.Webhooks == nil {
		out.Webhooks = []api.WebhookSourceStatus{}
	}
	writeJSON(w, http.StatusOK, out)
}

// errInvalidOverviewEvents is the 400 fact for a bad ?events bound.
// hostStatus reads the host's coarse load and memory. Both files are small
// and local; a missing or unreadable file leaves the field zero (unknown).
func hostStatus() api.HostStatus {
	h := api.HostStatus{CPUs: runtime.NumCPU()}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			h.Load1, _ = strconv.ParseFloat(fields[0], 64)
			h.Load5, _ = strconv.ParseFloat(fields[1], 64)
			h.Load15, _ = strconv.ParseFloat(fields[2], 64)
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			kib, err := strconv.Atoi(fields[1])
			if err != nil {
				continue
			}
			switch fields[0] {
			case "MemTotal:":
				h.MemoryTotalMiB = kib / 1024
			case "MemAvailable:":
				h.MemoryAvailableMiB = kib / 1024
			}
		}
	}
	return h
}

// boxCounts is the box-state census and the host's running capacity.
func boxCounts(boxes []*state.Box, maxRunning int) api.BoxCounts {
	out := api.BoxCounts{Total: len(boxes), MaxRunning: maxRunning}
	for _, box := range boxes {
		switch box.State {
		case state.StateRunning:
			out.Running++
		case state.StatePaused:
			out.Paused++
		case state.StateCreated:
			out.Created++
		case state.StateFailed:
			out.Failed++
		}
	}
	return out
}

// boxSummaries is one stable row per box, sorted by project, branch, and id so
// a screenshot does not reshuffle between refreshes.
func boxSummaries(boxes []*state.Box, now time.Time) []api.BoxSummary {
	out := make([]api.BoxSummary, 0, len(boxes))
	for _, box := range boxes {
		row := api.BoxSummary{
			ID:        box.ID,
			Project:   box.Project,
			Branch:    box.Branch,
			Worktree:  box.Worktree,
			State:     string(box.State),
			Phase:     boxPhase(box),
			Image:     box.Image,
			Resources: box.Resources,
			LastJob:   jobSummary(box.LatestJob()),
			NextDueAt: earliestDue(box, now),
		}
		if box.State == state.StateRunning && box.Phases != nil && box.Phases.SessionUsage != nil {
			row.Usage = &api.BoxUsage{
				CPUUsec: box.Phases.SessionUsage.CPUUsec,
				IOBytes: box.Phases.SessionUsage.IOBytes,
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Branch != out[j].Branch {
			return out[i].Branch < out[j].Branch
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// boxPhase names the phase worth showing: the one running now, else the last
// failure, else the most recent completed phase.
func boxPhase(box *state.Box) string {
	if box.Phases == nil {
		return ""
	}
	phases := []struct {
		name   string
		status state.PhaseStatus
	}{
		{"provision", box.Phases.Provision},
		{"wake", box.Phases.Wake},
	}
	for _, phase := range phases {
		if phase.status.State == state.PhaseRunning {
			return phase.name + ":running"
		}
	}
	for _, phase := range phases {
		if phase.status.State == state.PhaseFailed {
			return phase.name + ":failed"
		}
	}
	if box.Phases.Wake.State != "" {
		return "wake:" + string(box.Phases.Wake.State)
	}
	if box.Phases.Provision.State != "" {
		return "provision:" + string(box.Phases.Provision.State)
	}
	return ""
}

// jobSummary is the compact view of a job for the box row.
func jobSummary(job *state.Job) *api.JobSummary {
	if job == nil {
		return nil
	}
	return &api.JobSummary{
		ID:         job.ID,
		Command:    job.Command,
		State:      string(job.State),
		ExitCode:   job.ExitCode,
		DurationMS: job.DurationMS,
		StartedAt:  job.StartedAt,
		FinishedAt: job.FinishedAt,
	}
}

// earliestDue is the box's next scheduled occurrence, if it has any.
func earliestDue(box *state.Box, now time.Time) *time.Time {
	var earliest *time.Time
	for _, sched := range box.Schedules {
		due := nextDueAt(sched)
		if due == nil {
			continue
		}
		if earliest == nil || due.Before(*earliest) {
			earliest = due
		}
	}
	return earliest
}

// queueStatus counts the queue by state and keeps the blocked/rejected items
// that need a person, newest first. A wake-only item has no job; the counts
// still show it.
func queueStatus(items []state.QueueItem, capacity int) api.QueueStatus {
	out := api.QueueStatus{Capacity: capacity}
	problems := make([]api.QueueProblem, 0)
	for _, item := range items {
		switch item.State {
		case state.QueuePending:
			out.Pending++
		case state.QueueStarting:
			out.Starting++
			out.Pending++
		case state.QueueRunning:
			out.Running++
		case state.QueueBlocked:
			out.Blocked++
		case state.QueueRejected:
			out.Rejected++
		case state.QueueDone:
			out.Done++
		case state.QueueFailed:
			out.Failed++
		}
		if item.State == state.QueueBlocked || item.State == state.QueueRejected {
			problems = append(problems, api.QueueProblem{
				ID:        item.ID,
				BoxID:     item.BoxID,
				Source:    item.Source,
				Job:       item.Job,
				State:     item.State,
				Reason:    item.Reason,
				UpdatedAt: item.UpdatedAt,
			})
		}
	}
	sort.SliceStable(problems, func(i, j int) bool {
		return problems[i].UpdatedAt.After(problems[j].UpdatedAt)
	})
	if len(problems) > maxQueueProblems {
		problems = problems[:maxQueueProblems]
	}
	out.Problems = problems
	return out
}

// activeJobs lists the jobs currently in flight, across every box.
func activeJobs(boxes []*state.Box) []api.ActiveJob {
	out := make([]api.ActiveJob, 0)
	for _, box := range boxes {
		for i := range box.Jobs {
			job := &box.Jobs[i]
			if job.State != state.JobRunning {
				continue
			}
			out = append(out, api.ActiveJob{
				BoxID:     box.ID,
				Project:   box.Project,
				Branch:    box.Branch,
				JobID:     job.ID,
				Command:   job.Command,
				Origin:    job.JobOrigin,
				Source:    job.Source,
				TraceID:   job.TraceID,
				StartedAt: job.StartedAt,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].JobID < out[j].JobID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}
