package daemon

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/Siddhj2206/pluto/internal/events"
	"github.com/Siddhj2206/pluto/internal/state"
)

// This file is the daemon's single write point for the activity ledger. The
// ledger records decisions; Logf keeps the detailed evidence. Every record
// carries the daemon host and, when known, the flat box/job location.

// eventCause maps a trace id to the event that caused the next one, so a
// trace reads as a chain (queue.accepted -> queue.dispatched -> job.started ->
// job.done). It is bounded by the work in flight: entries are removed when a
// child takes them.
type eventCause struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *eventCause) remember(traceID, eventID string) {
	if traceID == "" || eventID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]string)
	}
	c.m[traceID] = eventID
}

func (c *eventCause) take(traceID string) string {
	if traceID == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.m[traceID]
	delete(c.m, traceID)
	return id
}

// forget drops an in-flight cause when a trace ends without a child to take
// it, so the map tracks work in flight rather than every trace ever seen.
func (c *eventCause) forget(traceID string) {
	if traceID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, traceID)
}

// traceActors remembers who caused an in-flight trace so the runner's later
// job facts carry the same actor. Entries are removed with the terminal job
// event; a trace with no entry falls back to the daemon.
type traceActors struct {
	mu sync.Mutex
	m  map[string]events.Actor
}

func (a *traceActors) remember(traceID string, actor events.Actor) {
	if traceID == "" || actor.Type == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = make(map[string]events.Actor)
	}
	a.m[traceID] = actor
}

func (a *traceActors) get(traceID string) events.Actor {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.m[traceID]
}

func (a *traceActors) take(traceID string) events.Actor {
	a.mu.Lock()
	defer a.mu.Unlock()
	actor := a.m[traceID]
	delete(a.m, traceID)
	return actor
}

// record appends one event, filling the host and the box's where-fields. A
// daemon without a ledger is silent. It returns the stored event, or nil when
// nothing was written.
func (s *Server) record(e events.Event, box *state.Box) *events.Event {
	if s.Events == nil {
		return nil
	}
	if e.Actor.Host == "" {
		e.Actor.Host = s.host
	}
	if e.Where.Host == "" {
		e.Where.Host = s.host
	}
	if box != nil {
		if e.Where.BoxID == "" {
			e.Where.BoxID = box.ID
		}
		if e.Where.Project == "" {
			e.Where.Project = box.Project
		}
		if e.Where.Branch == "" {
			e.Where.Branch = box.Branch
		}
		if e.Where.Worktree == "" {
			e.Where.Worktree = box.Worktree
		}
	}
	stored, err := s.Events.Append(e)
	if err != nil {
		s.logf("events: %v", err)
		return nil
	}
	return &stored
}

// requestOrigin reads the best-effort trace and client identity a CLI sends.
// Neither is ever used for authorization.
func requestOrigin(r *http.Request) (traceID string, actor events.Actor) {
	traceID = r.Header.Get("X-Pluto-Trace")
	client := r.Header.Get("X-Pluto-Client")
	actor = events.Actor{Type: "api", Client: client}
	if client != "" {
		actor.Type = "cli"
	}
	return traceID, actor
}

// actorName is the record-friendly label for an actor: the client name when
// known, otherwise the actor class.
func actorName(actor events.Actor) string {
	if actor.Client != "" {
		return actor.Client
	}
	return actor.Type
}

// originFor resolves a request's trace and actor, letting an explicit body
// value win over the headers for non-CLI API callers.
func originFor(r *http.Request, bodyTrace string, bodyActor events.Actor) (string, events.Actor) {
	traceID, actor := requestOrigin(r)
	if bodyTrace != "" {
		traceID = bodyTrace
	}
	if bodyActor.Type != "" {
		actor = bodyActor
	}
	if traceID == "" {
		traceID = events.NewTraceID()
	}
	return traceID, actor
}

// observeFact turns a runner lifecycle fact into a ledger event. The runner
// knows the job; the daemon adds the trace and where from the job record.
func (s *Server) observeFact(f events.Fact) {
	if s.Events == nil || f.Kind == "" {
		return
	}
	box, err := s.store.Box(f.BoxID)
	if err != nil {
		box = nil
	}
	attrs := make(map[string]string, len(f.Attrs)+2)
	for k, v := range f.Attrs {
		attrs[k] = v
	}
	if len(attrs) == 0 {
		attrs = nil
	}
	e := events.Event{
		Kind:       f.Kind,
		Level:      f.Level,
		Summary:    f.Summary,
		Reason:     f.Reason,
		DurationMS: f.Duration.Milliseconds(),
		Attrs:      attrs,
		Where:      events.Where{BoxID: f.BoxID, JobID: f.JobID},
	}
	if f.ExitCode != 0 || f.ExitCodeKnown {
		if e.Attrs == nil {
			e.Attrs = make(map[string]string, 1)
		}
		e.Attrs["exit_code"] = strconv.Itoa(f.ExitCode)
	}
	e.TraceID = f.TraceID
	e.Where.TaskID = f.TaskID
	e.Where.RunID = f.RunID
	e.Where.QueueID = f.QueueID
	e.Where.Schedule = f.ScheduleName
	e.Where.EventSource = f.EventSource
	e.Where.DeliveryID = f.EventID
	if box != nil && f.JobID != "" {
		if job := box.Job(f.JobID); job != nil {
			e.TraceID = job.TraceID
			e.Where.TaskID = job.TaskID
			e.Where.RunID = job.RunID
			e.Where.QueueID = job.QueueID
			e.Where.Schedule = job.ScheduleName
			e.Where.EventSource = job.EventSource
			e.Where.DeliveryID = job.EventID
			if e.Attrs == nil {
				e.Attrs = make(map[string]string, 2)
			}
			e.Attrs["job_state"] = string(job.State)
			e.Attrs["source"] = job.Source
		}
	}
	switch f.Kind {
	case events.KindJobStarted:
		e.Summary = "job " + state.ShortID(f.JobID) + " started"
	case events.KindJobDone:
		e.Summary = "job " + state.ShortID(f.JobID) + " done"
	case events.KindJobFailed:
		e.Summary = "job " + state.ShortID(f.JobID) + " failed"
	}
	if f.JobID != "" {
		if actor := s.actors.get(e.TraceID); actor.Type != "" {
			e.Actor = actor
		} else if e.Actor.Type == "" {
			// A job adopted after a restart has no live request to name; the
			// daemon is the truthful actor.
			e.Actor = events.Actor{Type: "daemon"}
		}
	}
	switch f.Kind {
	case events.KindJobStarted:
		e.ParentID = s.causes.take(e.TraceID)
		if stored := s.record(e, box); stored != nil {
			s.causes.remember(e.TraceID, stored.ID)
		}
	case events.KindJobDone, events.KindJobFailed:
		e.ParentID = s.causes.take(e.TraceID)
		if actor := s.actors.take(e.TraceID); actor.Type != "" {
			e.Actor = actor
		}
		if stored := s.record(e, box); stored != nil {
			// A queue item's terminal event chains onto the job's outcome.
			s.causes.remember(e.TraceID, stored.ID)
		}
	default:
		s.record(e, box)
	}
}

// recordQueued records the acceptance of a durable request and remembers it as
// the cause of the next event in the same trace. It returns the stored event
// so a caller can continue a chain.
func (s *Server) recordQueued(box *state.Box, item state.QueueItem, traceID string, actor events.Actor) *events.Event {
	summary := "queued work"
	if item.Job != "" {
		summary = "queued job " + item.Job
	} else if len(item.Argv) > 0 {
		summary = "queued ad-hoc command"
	}
	queued := s.record(events.Event{
		Kind:     events.KindQueueAccepted,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  summary,
		Where: events.Where{
			BoxID:       item.BoxID,
			QueueID:     item.ID,
			TaskID:      item.TaskID,
			RunID:       item.RunID,
			Schedule:    item.ScheduleName,
			EventSource: item.EventSource,
			DeliveryID:  item.EventID,
		},
		Attrs: map[string]string{"source": item.Source},
	}, box)
	if queued != nil {
		s.causes.remember(traceID, queued.ID)
	}
	return queued
}

// recordQueueDispatched records that the queue handed an item to the runner,
// after any admission re-check and before the job starts.
func (s *Server) recordQueueDispatched(box *state.Box, item state.QueueItem, traceID string, actor events.Actor) {
	if dispatched := s.record(events.Event{
		Kind:     events.KindQueueDispatched,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  "dispatching " + queueSummary(item),
		Where:    queueWhere(item),
		Attrs:    map[string]string{"source": item.Source},
	}, box); dispatched != nil {
		s.causes.remember(traceID, dispatched.ID)
	}
}

// recordQueueTerminal records a queue item's final outcome. A job item also
// has its job.done/job.failed event from the runner; this is the queue's own
// decision, so a wake-only item and a failed item are equally explainable.
func (s *Server) recordQueueTerminal(box *state.Box, item state.QueueItem, traceID string, actor events.Actor, failure error) {
	kind, level, summary := events.KindQueueDone, events.LevelInfo, "queue item done"
	reason := ""
	if failure != nil {
		kind, level, summary, reason = events.KindQueueFailed, events.LevelError, "queue item failed", failure.Error()
	}
	done := s.record(events.Event{
		Kind:     kind,
		Level:    level,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  summary,
		Reason:   reason,
		Where:    queueWhere(item),
		Attrs:    map[string]string{"source": item.Source},
	}, box)
	if item.TaskID == "" {
		return
	}
	taskKind := events.KindTaskRunDone
	if failure != nil {
		taskKind = events.KindTaskRunFailed
	}
	parentID := ""
	if done != nil {
		parentID = done.ID
	}
	s.record(events.Event{
		Kind:     taskKind,
		Level:    level,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: parentID,
		Summary:  "task run " + summary,
		Reason:   reason,
		Where:    queueWhere(item),
	}, box)
	s.causes.forget(traceID)
}

// recordTaskRunStarted records the transition from a queued run to running.
func (s *Server) recordTaskRunStarted(box *state.Box, item state.QueueItem, traceID string, actor events.Actor) {
	if item.TaskID == "" {
		return
	}
	if started := s.record(events.Event{
		Kind:     events.KindTaskRunStarted,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  "task run started",
		Where:    queueWhere(item),
	}, box); started != nil {
		s.causes.remember(traceID, started.ID)
	}
}

// recordTaskAccepted records the task and its first run after the queue
// accepted them, keeping task, run, queue, and trace identity together.
func (s *Server) recordTaskAccepted(box *state.Box, task *state.Task, traceID string, actor events.Actor) {
	if task == nil {
		return
	}
	where := events.Where{BoxID: box.ID, TaskID: task.ID}
	summary := "created task"
	if len(task.Runs) > 0 {
		where.RunID = task.Runs[len(task.Runs)-1].ID
		where.QueueID = task.Runs[len(task.Runs)-1].QueueID
		if task.Runs[len(task.Runs)-1].Job != "" {
			summary = "created task for job " + task.Runs[len(task.Runs)-1].Job
		}
	}
	if created := s.record(events.Event{
		Kind:     events.KindTaskCreated,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  summary,
		Where:    where,
		Attrs:    map[string]string{"source": task.Source},
	}, box); created != nil {
		s.causes.remember(traceID, created.ID)
	}
}

// jobOriginFor builds the trigger context a queued item's job inherits.
func jobOriginFor(item state.QueueItem, traceID string) state.JobOrigin {
	origin := "manual"
	switch {
	case item.ScheduleName != "":
		// An explicit occurrence is still caused by a person; the schedule
		// name on the job keeps it in the schedule's chain.
		if item.Source != state.QueueManual {
			origin = "schedule"
		}
	case item.EventSource != "":
		origin = "event"
	case item.TaskID != "":
		origin = "task"
	}
	return state.JobOrigin{
		Source:       item.Source,
		JobOrigin:    origin,
		TraceID:      traceID,
		TaskID:       item.TaskID,
		RunID:        item.RunID,
		QueueID:      item.ID,
		ScheduleName: item.ScheduleName,
		EventSource:  item.EventSource,
		EventID:      item.EventID,
	}
}

// queueSummary names a queue item for a human reading the ledger without
// exposing an ad-hoc command's argv.
func queueSummary(item state.QueueItem) string {
	switch {
	case item.Job != "":
		return "job " + item.Job
	case len(item.Argv) > 0:
		return "ad-hoc command"
	default:
		return "box wake"
	}
}

// queueWhere is the flat location of a queue item: its box, queue id, and the
// task/run/job/schedule/delivery it belongs to.
func queueWhere(item state.QueueItem) events.Where {
	return events.Where{
		BoxID:       item.BoxID,
		QueueID:     item.ID,
		TaskID:      item.TaskID,
		RunID:       item.RunID,
		JobID:       item.JobID,
		Schedule:    item.ScheduleName,
		EventSource: item.EventSource,
		DeliveryID:  item.EventID,
	}
}

// recordQueueBlocked records a queue decision that stops work until something
// changes, with the visible reason.
func (s *Server) recordQueueBlocked(box *state.Box, item state.QueueItem, traceID string, actor events.Actor, reason string) {
	where := queueWhere(item)
	blocked := s.record(events.Event{
		Kind:     events.KindQueueBlocked,
		Level:    events.LevelWarn,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: s.causes.take(traceID),
		Summary:  "queue item blocked",
		Reason:   reason,
		Where:    where,
		Attrs:    map[string]string{"source": item.Source},
	}, box)
	if blocked != nil {
		s.causes.remember(traceID, blocked.ID)
	}
	if item.TaskID == "" {
		return
	}
	parentID := ""
	if blocked != nil {
		parentID = blocked.ID
	}
	s.record(events.Event{
		Kind:     events.KindTaskRunBlocked,
		Level:    events.LevelWarn,
		TraceID:  traceID,
		Actor:    actor,
		ParentID: parentID,
		Summary:  "task run blocked",
		Reason:   reason,
		Where:    where,
	}, box)
}
