package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// This file is the schedule read/run surface. Schedules live on the box
// record (ADR 0003); the daemon projects them with a computed next due time
// and lets a person materialize one occurrence explicitly without waiting
// for a cron window.

// handleScheduleList serves every box's schedules, or one box's with ?box=ID.
func (s *Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	boxes, _, err := s.store.Boxes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if filter := r.URL.Query().Get("box"); filter != "" {
		if !state.ValidID(filter) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid box id %q", filter))
			return
		}
		box, err := s.store.Box(filter)
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		boxes = []*state.Box{box}
	}
	now := s.now()
	out := scheduleInfos(boxes, now)
	writeJSON(w, http.StatusOK, api.ScheduleListResponse{SchemaVersion: 1, Schedules: out})
}

// scheduleInfos projects every box's stored schedules into the response shape,
// sorted by box then name. Both the schedule listing and the overview read it.
func scheduleInfos(boxes []*state.Box, now time.Time) []api.ScheduleInfo {
	out := make([]api.ScheduleInfo, 0)
	for _, box := range boxes {
		for _, sched := range box.Schedules {
			out = append(out, scheduleInfo(box, sched, now))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].BoxID != out[j].BoxID {
			return out[i].BoxID < out[j].BoxID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// scheduleInfo projects one stored schedule into the response shape.
func scheduleInfo(box *state.Box, sched state.Schedule, now time.Time) api.ScheduleInfo {
	return api.ScheduleInfo{
		BoxID:       box.ID,
		Project:     box.Project,
		Branch:      box.Branch,
		BoxState:    string(box.State),
		Name:        sched.Name,
		Cron:        sched.Cron,
		Job:         sched.Job,
		ArmedAt:     sched.ArmedAt,
		LastFired:   sched.LastFired,
		NextDueAt:   nextDueAt(sched),
		LastOutcome: sched.LastOutcome,
		LastRunID:   sched.LastRunID,
		LastJobID:   sched.LastJobID,
	}
}

// nextDueAt is the first occurrence the scheduler has not materialized yet.
// It is in the past while a missed occurrence waits for the next tick, which
// is the truthful "due" answer after a restart or a long pause.
func nextDueAt(sched state.Schedule) *time.Time {
	cron, err := contract.ParseCron(sched.Cron)
	if err != nil {
		return nil
	}
	next, ok := cron.Next(consumedThrough(sched))
	if !ok {
		return nil
	}
	return &next
}

// withScheduleDue fills the response-only next due time on a box copy. The
// daemon is the only place that can compute it; a remote CLI never has the
// cron dialect's answer cached.
func withScheduleDue(box *state.Box) *state.Box {
	if box == nil || len(box.Schedules) == 0 {
		return box
	}
	for i := range box.Schedules {
		box.Schedules[i].NextDueAt = nextDueAt(box.Schedules[i])
	}
	return box
}

// scheduleByName finds a stored schedule on a box.
func scheduleByName(box *state.Box, name string) (state.Schedule, bool) {
	for _, sched := range box.Schedules {
		if sched.Name == name {
			return sched, true
		}
	}
	return state.Schedule{}, false
}

// handleScheduleRun materializes one explicit occurrence of a schedule now.
// It uses the same trust gate as the scheduler and never moves the cron
// clock: the next cron slot is still the one the schedule had before.
func (s *Server) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	box, ok := s.lookup(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	sched, ok := scheduleByName(box, name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("box %s has no schedule %q", state.ShortID(box.ID), name))
		return
	}
	var req api.ScheduleRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}

	// A schedule that names a job is unattended work: it needs the exact
	// approved contract revision, whether the occurrence is materialized by
	// cron or by a person (#110).
	revision, approved := "", false
	if sched.Job != "" {
		var err error
		revision, approved, err = s.contractAdmission(box)
		if err != nil {
			reason := "cannot verify contract trust: " + err.Error()
			s.skipSchedule(box, sched, state.ScheduleSkipped, reason)
			writeError(w, http.StatusUnprocessableEntity, errors.New(reason))
			return
		}
		if !approved {
			reason := fmt.Sprintf("schedule %q runs job %q, which requires an approved contract revision", name, sched.Job)
			s.skipSchedule(box, sched, state.ScheduleSkipped, reason)
			writeError(w, http.StatusConflict, errors.New(reason+"; approve it with 'pluto trust <box>'"))
			return
		}
	}

	traceID, actor := originFor(r, req.TraceID, req.Actor)
	key := req.IdempotencyKey
	if key == "" {
		// No key means no retry identity: each call is a distinct occurrence.
		key = events.NewTraceID()
	}
	now := s.now()
	occurrence := "manual:" + now.UTC().Format(time.RFC3339Nano)
	identity := scheduleIdentity(box.ID, name, key)
	if existing, ok := s.manualOccurrence(box.ID, name, identity); ok {
		if task, err := s.store.Task(existing.TaskID); err == nil {
			if run := taskRunByID(task, existing.RunID); run != nil {
				original := occurrence
				if existing.Event.ObjectID != "" {
					original = existing.Event.ObjectID
				}
				writeJSON(w, http.StatusOK, api.ScheduleRunResponse{
					SchemaVersion: 1, Schedule: name, BoxID: box.ID, Occurrence: original,
					TraceID: existing.TraceID, Task: *task, Run: *run,
				})
				return
			}
		}
	}

	prompt := "Schedule " + name + " occurrence at " + occurrence
	if sched.Job == "" {
		prompt = "Schedule " + name + " warm-up occurrence at " + occurrence
	}
	task, err := s.acceptScheduleOccurrence(box, sched, scheduleOccurrence{
		source: state.QueueManual, eventID: identity, occurrence: occurrence,
		prompt: prompt, traceID: traceID, actor: actor,
		revision: revision, approved: approved,
	}, now)
	if err != nil {
		if errors.Is(err, state.ErrQueueFull) {
			s.skipSchedule(box, sched, state.ScheduleRejected, "queue is full")
			writeError(w, http.StatusConflict, errors.New("queue is full; retry when work drains"))
			return
		}
		s.recordScheduleFailure(box, state.QueueItem{BoxID: box.ID, ScheduleName: name, TraceID: traceID}, "", err.Error())
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	run := task.Runs[len(task.Runs)-1]
	if fired := s.record(events.Event{
		Kind:    events.KindScheduleFired,
		TraceID: traceID,
		Actor:   actor,
		Summary: "schedule " + name + " ran explicitly",
		Reason:  "explicit run; the cron clock is unchanged",
		Where:   events.Where{BoxID: box.ID, Schedule: name, QueueID: run.QueueID, TaskID: task.ID, RunID: run.ID},
		Attrs:   map[string]string{"occurrence": occurrence, "source": "manual"},
	}, box); fired != nil {
		s.causes.remember(traceID, fired.ID)
	}
	// The same acceptance chain the scheduler records, so a manual occurrence
	// reads as schedule -> queue -> task -> run -> job like a cron one.
	s.recordQueued(box, state.QueueItem{
		ID: run.QueueID, Source: state.QueueManual, BoxID: box.ID,
		Job: run.Job, TaskID: task.ID, RunID: run.ID,
		ScheduleName: name, EventSource: "schedule", EventID: identity,
	}, traceID, actor)
	s.recordTaskAccepted(box, task, traceID, actor)
	writeJSON(w, http.StatusAccepted, api.ScheduleRunResponse{
		SchemaVersion: 1, Schedule: name, BoxID: box.ID, Occurrence: occurrence,
		TraceID: traceID, Task: *task, Run: run,
	})
}

// manualOccurrence finds an already materialized explicit occurrence by its
// idempotency identity, so a retried request returns the original run instead
// of growing the queue.
func (s *Server) manualOccurrence(boxID, name, identity string) (state.QueueItem, bool) {
	items, err := s.store.Queue()
	if err != nil {
		return state.QueueItem{}, false
	}
	for _, item := range items {
		if item.Source == state.QueueManual && item.BoxID == boxID && item.ScheduleName == name && item.EventID == identity {
			return item, true
		}
	}
	return state.QueueItem{}, false
}

// taskRunByID finds one run in a task.
func taskRunByID(task *state.Task, id string) *state.TaskRun {
	for i := range task.Runs {
		if task.Runs[i].ID == id {
			return &task.Runs[i]
		}
	}
	return nil
}

// scheduleOccurrence is one occurrence ready to become durable queue work.
type scheduleOccurrence struct {
	source     string // QueueScheduled or QueueManual
	eventID    string // idempotency identity, unique per occurrence
	occurrence string // display identity: an RFC3339 slot or "manual:<time>"
	prompt     string
	traceID    string
	actor      events.Actor
	revision   string
	approved   bool
}

// acceptScheduleOccurrence is the one enqueue path shared by the scheduler
// and an explicit run, so both produce the same durable task/run/queue shape.
func (s *Server) acceptScheduleOccurrence(box *state.Box, sched state.Schedule, occ scheduleOccurrence, now time.Time) (*state.Task, error) {
	actor := actorName(occ.actor)
	return s.store.AcceptTriggeredTask(
		state.Task{Source: "schedule", Project: box.Project, Ref: sched.Name, BoxID: box.ID, IdempotencyKey: occ.eventID, TraceID: occ.traceID, Actor: actor},
		state.TaskRun{Job: sched.Job, Prompt: occ.prompt, IdempotencyKey: occ.eventID, ContractRevision: occ.revision, TrustDecision: trustDecision(occ.approved), TraceID: occ.traceID, Actor: actor},
		state.QueueItem{
			Source: occ.source, EventSource: "schedule", EventID: occ.eventID,
			BoxID: box.ID, Repo: box.PrimaryRepoURL, Ref: box.Ref, Job: sched.Job,
			ScheduleName: sched.Name, ContractRevision: occ.revision, TrustDecision: trustDecision(occ.approved),
			TraceID: occ.traceID, Actor: actor,
			Event: state.EventContext{Kind: "schedule", Action: sched.Name, Repo: box.PrimaryRepoURL, Ref: box.Ref, ObjectID: occ.occurrence},
		},
		s.QueueCapacity, now,
	)
}

// scheduleIdentity is the durable identity of one occurrence. The occurrence
// slot (or the request's idempotency key) makes a replay the same occurrence
// and a new slot a new one.
func scheduleIdentity(boxID, name, slot string) string {
	return "schedule:" + boxID + ":" + name + ":" + slot
}

// skipSchedule records the scheduler's or the run endpoint's decision not to
// materialize an occurrence, with its reason, and persists the outcome for
// `pluto schedule ls`. A repeated identical skip is not recorded again: the
// scheduler evaluates every interval, and the decision has not changed.
func (s *Server) skipSchedule(box *state.Box, sched state.Schedule, outcome, reason string) {
	if sched.LastOutcome == outcome {
		return
	}
	s.record(events.Event{
		Kind:    events.KindScheduleSkipped,
		Level:   events.LevelWarn,
		Actor:   events.Actor{Type: "schedule", ID: sched.Name},
		Summary: "schedule " + sched.Name + " skipped",
		Reason:  reason,
		Where:   events.Where{BoxID: box.ID, Schedule: sched.Name},
	}, box)
	if _, err := s.store.SetScheduleOutcome(box.ID, sched.Name, outcome, "", ""); err != nil {
		s.logf("schedule %s on box %s: record skip: %v", sched.Name, state.ShortID(box.ID), err)
	}
}

// recordScheduleFailure records a schedule-level failure and its reason. The
// queue and job events remain the detailed evidence; this event answers "why
// did the schedule not produce work?" at the schedule's own level.
func (s *Server) recordScheduleFailure(box *state.Box, item state.QueueItem, jobID, reason string) {
	if item.ScheduleName == "" {
		return
	}
	if _, err := s.store.SetScheduleOutcome(box.ID, item.ScheduleName, state.ScheduleFailed, item.RunID, jobID); err != nil {
		s.logf("schedule %s on box %s: record failure: %v", item.ScheduleName, state.ShortID(box.ID), err)
	}
	s.record(events.Event{
		Kind:    events.KindScheduleFailed,
		Level:   events.LevelError,
		TraceID: item.TraceID,
		Actor:   events.Actor{Type: "schedule", ID: item.ScheduleName},
		Summary: "schedule " + item.ScheduleName + " failed",
		Reason:  reason,
		Where:   events.Where{BoxID: box.ID, Schedule: item.ScheduleName, QueueID: item.ID, TaskID: item.TaskID, RunID: item.RunID, JobID: jobID},
	}, box)
}

// recordScheduleTerminal persists the last occurrence's outcome and records a
// schedule failure event when the occurrence failed. A schedule with no job
// that woke the box is "warm"; one that ran a job is "done".
func (s *Server) recordScheduleTerminal(box *state.Box, item state.QueueItem, outcome, jobID, reason string) {
	if item.ScheduleName == "" {
		return
	}
	if _, err := s.store.SetScheduleOutcome(box.ID, item.ScheduleName, outcome, item.RunID, jobID); err != nil {
		s.logf("schedule %s on box %s: record outcome: %v", item.ScheduleName, state.ShortID(box.ID), err)
	}
	if outcome == state.ScheduleFailed {
		s.recordScheduleFailure(box, item, jobID, reason)
	}
}
