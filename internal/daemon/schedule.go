package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/api"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/envcache"
	"github.com/Siddhj2206/pluto/internal/state"
)

// SchedulerInterval is how often the daemon looks for due schedules. Cron
// resolution is one minute, so a half minute of granularity is plenty.
const SchedulerInterval = 30 * time.Second

// SchedulerLoop fires due schedules. It evaluates once at startup, catching
// up whatever was missed while the daemon was down, then every interval until
// ctx is done. Firing never blocks evaluation: each due schedule runs on its
// own goroutine, and the store refuses a second concurrent job.
func (s *Server) SchedulerLoop(ctx context.Context, interval time.Duration) {
	if err := s.store.RecoverQueue(s.now()); err != nil {
		s.logf("queue recovery: %v", err)
	}
	s.fireDueSchedules(ctx, s.now())
	s.dispatchQueue(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fireDueSchedules(ctx, s.now())
			s.dispatchQueue(ctx)
		}
	}
}

// dispatchQueue starts requests while host and per-box capacity allow it.
func (s *Server) dispatchQueue(ctx context.Context) {
	s.queueDispatchMu.Lock()
	defer s.queueDispatchMu.Unlock()
	if s.queueReservedBoxes == nil {
		s.queueReservedBoxes = make(map[string]bool)
	}
	boxes, _, err := s.store.Boxes()
	if err != nil {
		s.logf("queue: list boxes: %v", err)
		return
	}
	running := 0
	byID := make(map[string]*state.Box, len(boxes))
	for _, b := range boxes {
		byID[b.ID] = b
		if b.State == state.StateRunning {
			running++
		}
	}
	for {
		item, err := s.store.NextQueueItem(s.now(), s.QueueAgingInterval)
		if err != nil {
			s.logf("queue: claim: %v", err)
			return
		}
		if item == nil {
			return
		}
		box, ok := byID[item.BoxID]
		if !ok {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueueFailed, "", "box no longer exists", s.now())
			continue
		}
		if s.queueReservedBoxes[item.BoxID] || (box.JobRunning() && item.Job != "") {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueuePending, "", "", s.now())
			return
		}
		needsSlot := box.State != state.StateRunning
		if needsSlot && running+s.queueRunning >= s.MaxRunningBoxes {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueuePending, "", "", s.now())
			return
		}
		s.queueReservedBoxes[item.BoxID] = true
		if needsSlot {
			s.queueRunning++
			running++
		}
		go func(q state.QueueItem, b *state.Box, reserves bool) {
			s.executeQueued(ctx, q, b)
			s.queueDispatchMu.Lock()
			delete(s.queueReservedBoxes, q.BoxID)
			if reserves {
				s.queueRunning--
			}
			s.queueDispatchMu.Unlock()
		}(*item, box, needsSlot)
	}
}

func (s *Server) executeQueued(ctx context.Context, item state.QueueItem, box *state.Box) {
	if item.Source == state.QueueEvent && item.ContractRevision != "" {
		current, _, _, err := trustedDefaultBranch(ctx, box)
		approval, approvalErr := s.store.ContractApproval(box.Project)
		if err != nil || approvalErr != nil || current.Hash() != item.ContractRevision || approval == nil || approval.Revision != item.ContractRevision || item.TrustDecision != state.ContractTrustApproved {
			reason := "contract trust changed after the event was queued"
			if err != nil {
				reason = "cannot verify trusted contract before event run: " + err.Error()
			} else if approvalErr != nil {
				reason = "cannot verify contract approval before event run: " + approvalErr.Error()
			}
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueueBlocked, "", reason, s.now())
			return
		}
	}
	// Attended (manual) task work is not revision-gated; every other queued
	// task — schedules and any future unattended source — must still hold the
	// exact approved revision it was admitted with (#110).
	if item.TaskID != "" && item.ContractRevision != "" && item.Source != state.QueueEvent && item.TrustDecision != state.ContractTrustAttended {
		revision, approved, err := s.contractAdmission(box)
		if err != nil || revision != item.ContractRevision || !approved || item.TrustDecision != state.ContractTrustApproved {
			reason := "contract trust changed after the run was queued"
			if err != nil {
				reason = "cannot verify contract trust before run: " + err.Error()
			}
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueueBlocked, "", reason, s.now())
			return
		}
	}
	_, err := s.store.UpdateQueueItem(item.ID, state.QueueRunning, "", "", s.now())
	if err != nil {
		s.logf("queue %s: %v", state.ShortID(item.ID), err)
		return
	}
	jobID := ""
	if advanceEventRef(box, item) {
		if err := s.advanceQueuedWorkItem(ctx, box, item); err != nil {
			var blocked *eventUpdateBlockedError
			next := state.QueueFailed
			if errors.As(err, &blocked) {
				next = state.QueueBlocked
				_ = s.store.SetRef(box.ID, box.Ref, blocked.Error())
			}
			_, _ = s.store.UpdateQueueItem(item.ID, next, "", err.Error(), s.now())
			s.logf("queue %s: %v", state.ShortID(item.ID), err)
			return
		}
		box, err = s.store.Box(box.ID)
		if err != nil {
			_, _ = s.store.UpdateQueueItem(item.ID, state.QueueFailed, "", err.Error(), s.now())
			return
		}
	}
	if item.Job == "" && len(item.Argv) == 0 {
		_, err = s.runner.Up(ctx, box)
	} else {
		var spec contract.Exec
		if (item.Event.Kind == "issue" || item.Event.Kind == "push") && item.Job != "" {
			ref := item.Event.Ref
			spec, err = resolveWorkItemJob(box, ref, item.Job)
		} else {
			spec, err = resolveRun(box, api.RunRequest{Job: item.Job, Argv: item.Argv})
		}
		if err == nil {
			if item.TaskID != "" {
				if spec.Env == nil {
					spec.Env = make(map[string]string)
				}
				spec.Env["PLUTO_TASK_ID"] = item.TaskID
				spec.Env["PLUTO_RUN_ID"] = item.RunID
				spec.Env["PLUTO_TASK_PROMPT"] = item.Prompt
			}
			if item.Event.Kind != "" {
				if spec.Env == nil {
					spec.Env = make(map[string]string)
				}
				spec.Env["PLUTO_EVENT_KIND"] = item.Event.Kind
				spec.Env["PLUTO_EVENT_ACTION"] = item.Event.Action
				spec.Env["PLUTO_EVENT_REPO"] = item.Event.Repo
				spec.Env["PLUTO_EVENT_REF"] = item.Event.Ref
				spec.Env["PLUTO_EVENT_HEAD_REF"] = item.Event.HeadRef
				spec.Env["PLUTO_EVENT_OBJECT_ID"] = item.Event.ObjectID
				spec.Env["PLUTO_EVENT_URL"] = item.Event.URL
				if len(item.Event.Payload) > 0 {
					spec.Env["PLUTO_EVENT_PAYLOAD"] = string(item.Event.Payload)
				}
			}
			err = applyEventCredentials(&spec, item.Event, os.LookupEnv)
		}
		if err == nil {
			var job *state.Job
			_, job, err = s.runner.RunJob(ctx, box, spec, nil)
			if job != nil {
				jobID = job.ID
				_, _ = s.store.UpdateQueueItem(item.ID, state.QueueRunning, jobID, "", s.now())
			}
		}
	}
	if errors.Is(err, envcache.ErrBuilding) {
		// Another box is already provisioning this missing environment layer.
		// Release the request back to pending and let a later dispatch reuse
		// the published layer instead of duplicating the build.
		_, _ = s.store.UpdateQueueItem(item.ID, state.QueuePending, "", "waiting for an environment build", s.now())
		return
	}
	if err != nil {
		_, _ = s.store.UpdateQueueItem(item.ID, state.QueueFailed, "", err.Error(), s.now())
		s.logf("queue %s: %v", state.ShortID(item.ID), err)
		return
	}
	_, _ = s.store.UpdateQueueItem(item.ID, state.QueueDone, jobID, "", s.now())
	if item.ScheduleName != "" && item.TaskID == "" {
		if _, err := s.store.AdvanceSchedule(item.BoxID, item.ScheduleName, s.now()); err != nil {
			s.logf("schedule %s on box %s: record last-fired: %v", item.ScheduleName, state.ShortID(item.BoxID), err)
		}
	}
}

// advanceEventRef reports whether a queued event must move the box's worktree
// to the event ref before its job runs: a work-item box (PR or issue) whose
// recorded ref differs, or a registered branch box that received a push.
func advanceEventRef(box *state.Box, item state.QueueItem) bool {
	if item.Event.Ref == "" || box.Ref == item.Event.Ref {
		return false
	}
	switch item.Event.Kind {
	case "pull_request", "issue", "push":
		return true
	default:
		return false
	}
}

type eventUpdateBlockedError struct{ reason string }

func (e *eventUpdateBlockedError) Error() string { return e.reason }

// advanceQueuedWorkItem moves a box's source and guest worktrees to an event's
// ref before its job runs, preserving local work: a dirty source worktree, a
// target that is not a descendant of the current ref, or a guest that refuses
// the fast-forward blocks the update visibly instead of running stale content.
// It serves work-item boxes (PR and issue) and registered branch boxes (push).
func (s *Server) advanceQueuedWorkItem(ctx context.Context, box *state.Box, item state.QueueItem) error {
	s.workItemMu.Lock()
	defer s.workItemMu.Unlock()
	kindName, branchName := "push", "push"
	switch item.Event.Kind {
	case "pull_request":
		kindName, branchName = "PR", "pull"
	case "issue":
		kindName, branchName = "issue", "issue"
	}
	// A work-item box must match its event; a registered branch box has no work
	// item and is advanced by a push on its own branch.
	if box.WorkItemType != "" && (box.WorkItemType != item.Event.Kind || box.WorkItemID != item.Event.ObjectID) {
		return &eventUpdateBlockedError{reason: kindName + " queue item does not match its work-item box"}
	}
	current := box.Ref
	if current == "" {
		// A registered branch box records no ref until its first advance; the
		// source worktree's HEAD is what its disk currently holds.
		out, err := exec.Command("git", "-C", box.Worktree, "rev-parse", "HEAD").Output()
		if err != nil {
			return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: current worktree revision is unknown: %v", kindName, err)}
		}
		current = strings.TrimSpace(string(out))
	}
	updater, ok := s.runner.(interface {
		AdvancePRRef(context.Context, *state.Box, string, string) error
	})
	if !ok {
		return &eventUpdateBlockedError{reason: kindName + " ref update blocked: runner cannot inspect guest worktree"}
	}
	status, err := exec.Command("git", "-C", box.Worktree, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: source worktree status is unknown: %v", kindName, err)}
	}
	if len(status) != 0 {
		return &eventUpdateBlockedError{reason: kindName + " ref update blocked: source worktree has local changes"}
	}
	// A push fetches from the registered repository directly; a work-item box
	// has an origin clone.
	fetchRemote := "origin"
	if item.Event.Kind == "push" && box.PrimaryRepoURL != "" {
		fetchRemote = box.PrimaryRepoURL
	}
	fetch := exec.Command("git", "-C", box.Worktree, "fetch", "--no-tags", fetchRemote, item.Event.Ref)
	if out, err := fetch.CombinedOutput(); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: fetch target: %v (%s)", kindName, err, strings.TrimSpace(string(out)))}
	}
	ancestor := exec.Command("git", "-C", box.Worktree, "merge-base", "--is-ancestor", current, item.Event.Ref)
	if out, err := ancestor.CombinedOutput(); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: target is older than or diverged from current box ref (%s)", kindName, strings.TrimSpace(string(out)))}
	}
	advanceID := box.WorkItemID
	if advanceID == "" {
		advanceID = box.ID
	}
	keepRef := exec.Command("git", "-C", box.Worktree, "update-ref", "refs/pluto/"+branchName+"/"+advanceID+"/target", item.Event.Ref)
	if out, err := keepRef.CombinedOutput(); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: could not retain target ref: %v (%s)", kindName, err, strings.TrimSpace(string(out)))}
	}
	bundle := filepath.Join(s.store.Root(), "projects", "advance-"+branchName+"-"+box.ID+".bundle")
	if err := os.MkdirAll(filepath.Dir(bundle), 0o700); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: could not prepare bundle directory: %v", kindName, err)}
	}
	defer os.Remove(bundle)
	create := exec.Command("git", "-C", box.Worktree, "bundle", "create", bundle, "--all")
	if out, err := create.CombinedOutput(); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: could not build target bundle: %v (%s)", kindName, err, strings.TrimSpace(string(out)))}
	}
	if err := updater.AdvancePRRef(ctx, box, bundle, item.Event.Ref); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: guest worktree could not be safely advanced: %v", kindName, err)}
	}
	status, err = exec.Command("git", "-C", box.Worktree, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil || len(status) != 0 {
		return &eventUpdateBlockedError{reason: kindName + " ref update blocked: source worktree became dirty or unreadable"}
	}
	merge := exec.Command("git", "-C", box.Worktree, "merge", "--ff-only", item.Event.Ref)
	if out, err := merge.CombinedOutput(); err != nil {
		return &eventUpdateBlockedError{reason: fmt.Sprintf("%s ref update blocked: guest advanced but source checkout did not: %v (%s)", kindName, err, strings.TrimSpace(string(out)))}
	}
	if err := s.store.SetRef(box.ID, item.Event.Ref, ""); err != nil {
		return fmt.Errorf("record %s ref: %w", kindName, err)
	}
	return nil
}

func applyEventCredentials(spec *contract.Exec, event state.EventContext, lookup func(string) (string, bool)) error {
	if (event.Kind != "pull_request" && event.Kind != "issue") || !event.Trusted {
		return nil
	}
	for _, key := range event.CredentialNames {
		value, ok := lookup(key)
		if !ok {
			return fmt.Errorf("trusted event credential %s is not available in host environment", key)
		}
		if spec.Env == nil {
			spec.Env = make(map[string]string)
		}
		spec.Env[key] = value
		spec.SensitiveEnv = append(spec.SensitiveEnv, key)
	}
	return nil
}

func resolveWorkItemJob(box *state.Box, ref, name string) (contract.Exec, error) {
	show := exec.Command("git", "-C", box.Worktree, "show", ref+":"+contract.FileName)
	data, err := show.Output()
	if err != nil {
		return contract.Exec{}, fmt.Errorf("read trusted default-branch contract: %w", err)
	}
	ct, err := contract.Parse(string(data))
	if err != nil {
		return contract.Exec{}, fmt.Errorf("parse trusted default-branch contract: %w", err)
	}
	return ct.ExecJob(name)
}

// now is the daemon's view of the current time. Tests replace Server.Now to
// drive the scheduler deterministically.
func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fireDueSchedules evaluates every box's stored schedules against now.
func (s *Server) fireDueSchedules(ctx context.Context, now time.Time) {
	boxes, _, err := s.store.Boxes()
	if err != nil {
		s.logf("scheduler: list boxes: %v", err)
		return
	}
	for _, box := range boxes {
		s.fireBoxSchedules(ctx, box, now)
	}
}

// fireBoxSchedules materializes the next due occurrence as one durable task.
// The persisted occurrence timestamp makes repeated scheduler ticks idempotent.
func (s *Server) fireBoxSchedules(_ context.Context, box *state.Box, now time.Time) {
	for _, sched := range box.Schedules {
		cron, err := contract.ParseCron(sched.Cron)
		if err != nil {
			s.logf("schedule %s on box %s: invalid cron %q: %v", sched.Name, state.ShortID(box.ID), sched.Cron, err)
			continue
		}
		occurrence, due := nextOccurrenceAfter(cron, consumedThrough(sched), now)
		if !due {
			continue
		}
		revision, approved := "", false
		if sched.Job != "" {
			var trustErr error
			revision, approved, trustErr = s.contractAdmission(box)
			if trustErr != nil {
				s.logf("schedule %s on box %s: contract trust: %v", sched.Name, state.ShortID(box.ID), trustErr)
				continue
			}
		}
		occurrenceID := occurrence.UTC().Format(time.RFC3339)
		identity := "schedule:" + box.ID + ":" + sched.Name + ":" + occurrenceID
		prompt := "Schedule " + sched.Name + " occurrence at " + occurrenceID
		if sched.Job == "" {
			prompt = "Schedule " + sched.Name + " warm-up occurrence at " + occurrenceID
		}
		_, enqueueErr := s.store.AcceptTriggeredTask(
			state.Task{Source: "schedule", Project: box.Project, Ref: sched.Name, BoxID: box.ID, IdempotencyKey: identity},
			state.TaskRun{Job: sched.Job, Prompt: prompt, IdempotencyKey: identity, ContractRevision: revision, TrustDecision: trustDecision(approved)},
			state.QueueItem{Source: state.QueueScheduled, EventSource: "schedule", EventID: identity, BoxID: box.ID, Repo: box.PrimaryRepoURL, Ref: box.Ref, Job: sched.Job, ScheduleName: sched.Name, ContractRevision: revision, TrustDecision: trustDecision(approved), Event: state.EventContext{Kind: "schedule", Action: sched.Name, Repo: box.PrimaryRepoURL, Ref: box.Ref, ObjectID: occurrenceID}},
			s.QueueCapacity, now,
		)
		if enqueueErr != nil && !errors.Is(enqueueErr, state.ErrQueueFull) {
			s.logf("schedule %s on box %s: task: %v", sched.Name, state.ShortID(box.ID), enqueueErr)
			continue
		}
		// The durable queue item/task (or durable rejection) now represents this
		// occurrence. Its occurrence ID makes a retry after a crash idempotent.
		if _, err := s.store.AdvanceSchedule(box.ID, sched.Name, occurrence); err != nil {
			s.logf("schedule %s on box %s: record last-fired: %v", sched.Name, state.ShortID(box.ID), err)
		}
	}
}

func trustDecision(approved bool) string {
	if approved {
		return state.ContractTrustApproved
	}
	return ""
}

// consumedThrough is the latest occurrence materialized as a task/run, or the
// arm time before the first one. A schedule with neither has no known arming
// and never backfills.
func consumedThrough(sched state.Schedule) time.Time {
	if sched.LastFired != nil && sched.LastFired.After(sched.ArmedAt) {
		return *sched.LastFired
	}
	return sched.ArmedAt
}

func nextOccurrenceAfter(cron contract.Cron, last, now time.Time) (time.Time, bool) {
	if last.IsZero() {
		return time.Time{}, false
	}
	m := last.UTC().Truncate(time.Minute).Add(time.Minute)
	end := now.UTC().Truncate(time.Minute)
	for !m.After(end) {
		if cron.Matches(m) {
			return m, true
		}
		m = m.Add(time.Minute)
	}
	return time.Time{}, false
}
