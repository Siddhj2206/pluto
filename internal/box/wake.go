package box

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/Siddhj2206/pluto/internal/container"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// wakeOutputLimit bounds the wake output carried in a failure message so a
// chatty phase cannot bloat the box record or an error response.
const wakeOutputLimit = 2 << 10

// WakeError reports a wake phase that failed. It names the failing step (the
// declared command) and its exit code so `pluto run`/`pluto box status` can
// surface exactly what refused to come up (#146).
type WakeError struct {
	Command  string
	ExitCode int
	Reason   string
}

func (e *WakeError) Error() string {
	if e.ExitCode != 0 {
		return fmt.Sprintf("wake phase failed: %s exited %d", e.Command, e.ExitCode)
	}
	return fmt.Sprintf("wake phase failed: %s: %s", e.Command, e.Reason)
}

// runWake executes the workspace's wake phase inside a just-started box. It
// runs on every start — first use, a start after a stop, a retry after a wake
// failure — and never on a thaw, which preserves the running memory. A failed
// wake leaves the box in wake_failed with the failing command and exit code
// recorded; the caller then starts no services and no run.
func (m *Manager) runWake(ctx context.Context, box *state.Box) (*state.Box, error) {
	if box.WorkspacePath == "" || box.EngineID == "" {
		return box, nil
	}
	project, err := contract.LoadProject(box.WorkspacePath)
	if err != nil {
		return m.failWake(box, contract.Command{}, 0, err.Error(), time.Now().UTC(), time.Now().UTC())
	}
	if project.Wake == nil || project.Wake.Run.IsZero() {
		return box, nil
	}
	command := project.Wake.Run
	started := time.Now().UTC()
	var out bytes.Buffer
	result, execErr := m.engine.Exec(ctx, box.EngineID, container.ExecSpec{Argv: command.Argv()}, &out)
	finished := time.Now().UTC()
	if execErr != nil {
		return m.failWake(box, command, result.ExitCode, execErr.Error(), started, finished)
	}
	if result.ExitCode != 0 {
		return m.failWake(box, command, result.ExitCode, wakeTail(out.Bytes()), started, finished)
	}
	return m.storeWake(box, state.PhaseStatus{
		State:      state.PhaseDone,
		Command:    command.String(),
		StartedAt:  &started,
		FinishedAt: &finished,
	})
}

// failWake records the failing wake step on the box and moves it to
// wake_failed, keeping the record and container for inspection.
func (m *Manager) failWake(box *state.Box, command contract.Command, exitCode int, reason string, started, finished time.Time) (*state.Box, error) {
	status := state.PhaseStatus{
		State:      state.PhaseFailed,
		Command:    command.String(),
		ExitCode:   exitCode,
		Error:      reason,
		StartedAt:  &started,
		FinishedAt: &finished,
	}
	if _, err := m.storeWake(box, status); err != nil {
		return nil, err
	}
	failed, err := m.store.Transition(box.ID, state.StateWakeFailed)
	if err != nil {
		return nil, err
	}
	return failed, &WakeError{Command: status.Command, ExitCode: exitCode, Reason: reason}
}

// storeWake writes the wake phase's status onto the box, preserving any other
// observed phases.
func (m *Manager) storeWake(box *state.Box, status state.PhaseStatus) (*state.Box, error) {
	phases := state.Phases{}
	if box.Phases != nil {
		phases = *box.Phases
	}
	phases.Wake = status
	phases.Synced = true
	return m.store.SetPhases(box.ID, phases)
}

// wakeTail returns the last wakeOutputLimit bytes of output, trimmed, for a
// failure message.
func wakeTail(data []byte) string {
	if len(data) > wakeOutputLimit {
		data = data[len(data)-wakeOutputLimit:]
	}
	return string(bytes.TrimSpace(data))
}
