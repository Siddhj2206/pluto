// Task scheduling for the container path (#147). A pluto.yml schedule names a
// declared task; each distinct occurrence adds one run to that task's history.
// Redelivering an occurrence reuses the accepted run identity, a task keeps at
// most one pending run, and a due run on a busy box waits per the task's lock
// policy by going through box.Manager.Run's gate. The legacy job scheduler in
// schedule.go is untouched and skips these task schedules.
package daemon

import (
	"context"
	"time"

	boxlife "github.com/Siddhj2206/pluto/internal/box"
	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// acceptScheduledRuns is the per-tick entry point for the container scheduler,
// run alongside the legacy one. It is a no-op without a box lifecycle.
func (s *Server) acceptScheduledRuns(now time.Time) {
	if s.boxes == nil {
		return
	}
	boxes, _, err := s.store.Boxes()
	if err != nil {
		s.logf("task schedules: list boxes: %v", err)
		return
	}
	for _, box := range boxes {
		if box.WorkspacePath == "" {
			continue
		}
		s.acceptBoxTaskSchedules(box, now)
	}
}

// acceptBoxTaskSchedules reconciles one box's schedules with its contract and
// materializes each due occurrence.
func (s *Server) acceptBoxTaskSchedules(box *state.Box, now time.Time) {
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		s.logf("task schedules on %s: load contract: %v", state.ShortID(box.ID), err)
		return
	}
	declared := taskSchedules(project)
	if !schedulesEqual(box.Schedules, declared) {
		box, err = s.store.SetSchedules(box.ID, declared, now)
		if err != nil {
			s.logf("task schedules on %s: reconcile: %v", state.ShortID(box.ID), err)
			return
		}
	}
	for _, sched := range box.Schedules {
		if sched.Task == "" {
			continue
		}
		s.acceptScheduleOccurrences(box, project, sched, now)
	}
}

// acceptScheduleOccurrences accepts every occurrence due since the schedule's
// cursor. The first creates a run; the rest coalesce into it by way of the
// store, so a task never accumulates more than one pending run.
func (s *Server) acceptScheduleOccurrences(box *state.Box, project *contract.Project, sched state.Schedule, now time.Time) {
	cron, err := contract.ParseCron(sched.Cron)
	if err != nil {
		s.logf("schedule %s on %s: invalid cron %q: %v", sched.Name, state.ShortID(box.ID), sched.Cron, err)
		return
	}
	for {
		occurrence, due := nextOccurrenceAfter(cron, consumedThrough(sched), now)
		if !due {
			return
		}
		s.acceptScheduleOccurrence(box, project, sched, occurrence, now)
		// The occurrence is now durably represented (as a run or as a note in
		// one); advancing the cursor makes a retry after a crash idempotent.
		if _, err := s.store.AdvanceSchedule(box.ID, sched.Name, occurrence); err != nil {
			s.logf("schedule %s on %s: record last-fired: %v", sched.Name, state.ShortID(box.ID), err)
			return
		}
		sched.LastFired = &occurrence
	}
}

// acceptScheduleOccurrence proposes one occurrence to the store, which reuses,
// coalesces, or records it.
func (s *Server) acceptScheduleOccurrence(box *state.Box, project *contract.Project, sched state.Schedule, occurrence, now time.Time) {
	candidate := state.NewScheduledRun(state.NewID(), sched.Task, box.ID, box.WorkspacePath, sched.Name, occurrence, now)
	if exec, err := project.TaskExec(sched.Task); err == nil {
		candidate.Steps = []state.Step{state.NewPendingStep(implicitStepName, exec.Command.String())}
	} else {
		s.logf("schedule %s on %s: resolve task %q: %v", sched.Name, state.ShortID(box.ID), sched.Task, err)
	}
	if _, _, err := s.store.AcceptScheduledRun(candidate, now); err != nil {
		s.logf("schedule %s on %s: accept occurrence %s: %v", sched.Name, state.ShortID(box.ID), occurrence.UTC().Format(time.RFC3339), err)
	}
}

// dispatchScheduledRuns starts each task's pending run once, when no run of
// that task is already active. Holding a task's runs to one at a time is what
// lets later occurrences coalesce into the pending run instead of piling up;
// the lock policy is enforced downstream by the box's gate.
func (s *Server) dispatchScheduledRuns(ctx context.Context, now time.Time) {
	if s.boxes == nil {
		return
	}
	runs, err := s.store.Runs()
	if err != nil {
		s.logf("task schedules: list runs: %v", err)
		return
	}
	active := make(map[string]bool)
	for _, run := range runs {
		if run.Trigger == state.TriggerSchedule && run.State == state.RunRunning {
			active[scheduledTaskKey(run)] = true
		}
	}
	for _, run := range runs {
		if run.Trigger != state.TriggerSchedule || run.State != state.RunPending {
			continue
		}
		key := scheduledTaskKey(run)
		if active[key] {
			continue
		}
		active[key] = true
		if err := s.startScheduledRun(ctx, run, now); err != nil {
			s.logf("scheduled run %s: %v", state.ShortID(run.ID), err)
		}
	}
}

// startScheduledRun claims a pending run by marking it running and hands it to
// its own goroutine. Claiming synchronously is what keeps the next tick from
// dispatching the same pending run twice.
func (s *Server) startScheduledRun(ctx context.Context, run *state.Run, now time.Time) error {
	started := now.UTC()
	run.State = state.RunRunning
	run.StartedAt = &started
	if len(run.Steps) > 0 {
		run.Steps[0].Start(started)
	}
	if _, err := s.store.SetRun(*run); err != nil {
		return err
	}
	go s.executeScheduledRun(ctx, run.ID)
	return nil
}

// executeScheduledRun resolves the task through the box lifecycle and runs it
// under its lock policy, recording the outcome on the run's history.
func (s *Server) executeScheduledRun(ctx context.Context, runID string) {
	run, err := s.store.Run(runID)
	if err != nil {
		s.logf("scheduled run %s: load: %v", state.ShortID(runID), err)
		return
	}
	box, _, err := s.boxes.Ensure(ctx, boxlife.CreateRequest{Workspace: run.WorkspacePath})
	if err != nil {
		s.failScheduledRun(run, "start box: "+err.Error())
		return
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		s.failScheduledRun(run, "load contract: "+err.Error())
		return
	}
	lock, err := project.TaskLock(run.Task)
	if err != nil {
		s.failScheduledRun(run, err.Error())
		return
	}
	if lock == contract.LockService {
		s.failScheduledRun(run, "task is a service; it starts with the box rather than through a run")
		return
	}
	spec, err := project.TaskExec(run.Task)
	if err != nil {
		s.failScheduledRun(run, err.Error())
		return
	}
	runLog, err := s.store.CreateRunLog(run.ID, 0)
	if err != nil {
		s.failScheduledRun(run, "open run log: "+err.Error())
		return
	}
	defer runLog.Close()
	if len(run.Steps) == 0 {
		run.Steps = []state.Step{state.NewStep(implicitStepName, spec.Command.String())}
	}
	run.Steps[0].Log = s.store.RunLogRef(run.ID, 0)
	sink := &runOutputSink{log: runLog}

	result, execErr := s.boxes.Run(ctx, box.ID, lock, container.ExecSpec{Argv: spec.Command.Argv(), Env: spec.Env}, sink)
	finished := s.now()
	if sink.logErr != nil {
		s.logf("scheduled run %s: retaining step output failed: %v", state.ShortID(run.ID), sink.logErr)
	}
	if execErr != nil {
		run.Steps[0].Finish(1, execErr.Error(), finished)
		run.Fail(execErr.Error(), finished)
		_, _ = s.store.SetRun(*run)
		return
	}
	run.Steps[0].Finish(result.ExitCode, "", finished)
	run.Finish(finished)
	if _, err := s.store.SetRun(*run); err != nil {
		s.logf("scheduled run %s: record outcome: %v", state.ShortID(run.ID), err)
	}
}

// failScheduledRun records a run failed before its command could produce an
// outcome, so a broken schedule is legible rather than a silent stuck pending.
func (s *Server) failScheduledRun(run *state.Run, reason string) {
	if len(run.Steps) > 0 {
		run.Steps[0].Finish(1, reason, s.now())
	}
	run.Fail(reason, s.now())
	if _, err := s.store.SetRun(*run); err != nil {
		s.logf("scheduled run %s: record failure: %v", state.ShortID(run.ID), err)
	}
}

// scheduledTaskKey identifies a task's runs on one box, so dispatch serialises
// a task without conflating it with another task on the same box.
func scheduledTaskKey(run *state.Run) string { return run.BoxID + "\x00" + run.Task }

// taskSchedules compiles a project's schedules into the box's stored shape, in
// stable name order.
func taskSchedules(project *contract.Project) []state.Schedule {
	out := make([]state.Schedule, 0, len(project.Schedules))
	for _, name := range project.ScheduleNames() {
		declared := project.Schedules[name]
		out = append(out, state.Schedule{Name: name, Cron: declared.Cron, Task: declared.Task})
	}
	return out
}

// schedulesEqual reports whether a box's stored schedules already match the
// declared list, so reconciliation does not rewrite the record every tick.
func schedulesEqual(stored, declared []state.Schedule) bool {
	if len(stored) != len(declared) {
		return false
	}
	for i := range stored {
		if stored[i].Name != declared[i].Name || stored[i].Cron != declared[i].Cron || stored[i].Job != declared[i].Job || stored[i].Task != declared[i].Task {
			return false
		}
	}
	return true
}
