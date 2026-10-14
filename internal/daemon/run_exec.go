package daemon

import (
	"context"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/mask"
	"github.com/Siddhj2206/pluto/internal/state"
)

// resolvedTask is a declared task resolved for execution: its lock policy, its
// command, and the host-side secret context.
type resolvedTask struct {
	lock    contract.Lock
	exec    contract.Exec
	mask    *mask.Masker
	secrets map[string]string
}

// serviceTaskError reports a service task, which cannot be triggered as a run:
// it starts with its box instead. It is a caller error, not a host failure.
type serviceTaskError struct{ name string }

func (e serviceTaskError) Error() string {
	return "task " + e.name + " is a service; it starts with the box rather than through a run"
}

// resolveTask resolves a declared task for execution in a box: its lock policy,
// its command, and the project's secrets and masker. A service task is refused.
// An unknown task errors with contract.ErrNoSuchTask.
func (s *Server) resolveTask(box *state.Box, project *contract.Project, name string) (resolvedTask, error) {
	lock, err := project.TaskLock(name)
	if err != nil {
		return resolvedTask{}, err
	}
	if lock == contract.LockService {
		return resolvedTask{}, serviceTaskError{name: name}
	}
	exec, err := project.TaskExec(name)
	if err != nil {
		return resolvedTask{}, err
	}
	resolved, err := resolveProject(project, box.WorkspacePath)
	if err != nil {
		return resolvedTask{}, err
	}
	return resolvedTask{lock: lock, exec: exec, mask: resolved.mask, secrets: resolved.secrets}, nil
}

// taskExecSpec builds the engine exec spec for a task's command, injecting each
// resolved secret under its declared name so the task can read it with $NAME.
// The values are already registered with the masker, so they are redacted from
// every output surface.
func taskExecSpec(spec contract.Exec, secrets map[string]string) container.ExecSpec {
	env := make(map[string]string, len(spec.Env)+len(secrets))
	for k, v := range spec.Env {
		env[k] = v
	}
	for name, value := range secrets {
		env[name] = value
	}
	return container.ExecSpec{Argv: spec.Command.Argv(), Dir: spec.Dir, Env: env}
}

// executeRun is the one path a resolved task's run takes: it retains and masks
// the command's output, runs it in box under the task's lock policy, and stamps
// the run's terminal outcome. run must already be persisted. beforeSave, when
// non-nil, runs with the outcome in hand and before the run is persisted, so a
// CI run can retain or discard its ephemeral box.
//
// It returns the saved run, the engine's error (nil on success; a failed run is
// still recorded), and a store failure.
func (s *Server) executeRun(ctx context.Context, m BoxManager, box *state.Box, run *state.Run, task resolvedTask, stream *runStream, beforeSave func(*state.Run)) (*state.Run, error, error) {
	runLog, err := s.store.CreateRunLog(run.ID, 0)
	if err != nil {
		return nil, nil, err
	}
	defer runLog.Close()
	if len(run.Steps) == 0 {
		run.Steps = []state.Step{state.NewStep(implicitStepName, task.exec.Command.String())}
	}
	run.Steps[0].Log = s.store.RunLogRef(run.ID, 0)
	started := s.now()
	run.StartedAt = &started
	run.Steps[0].Start(started)
	// Persist the running record with its log reference before exec, so another
	// client can read the step's output while the run is still going.
	if _, err := s.store.SetRun(*run); err != nil {
		return nil, nil, err
	}
	sink := &runOutputSink{log: runLog, stream: stream, mask: task.mask}
	result, execErr := m.Run(ctx, box.ID, task.lock, taskExecSpec(task.exec, task.secrets), sink)
	// Release the masker's held-back tail before the run is described, so the
	// last bytes reach the log and the client and nothing is lost at the end.
	sink.Close()
	finished := s.now()
	if sink.logErr != nil {
		s.logf("run %s: retaining step output failed: %v", state.ShortID(run.ID), sink.logErr)
	}
	if execErr != nil {
		run.Steps[0].Finish(1, execErr.Error(), finished)
		run.Fail(execErr.Error(), finished)
	} else {
		run.Steps[0].Finish(result.ExitCode, "", finished)
		run.Finish(finished)
	}
	if beforeSave != nil {
		beforeSave(run)
	}
	saved, err := s.store.SetRun(*run)
	if err != nil {
		return nil, execErr, err
	}
	return saved, execErr, nil
}
