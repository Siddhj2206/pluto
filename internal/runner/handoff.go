package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// AgentClient is the guest agent surface the runner drives.
type AgentClient interface {
	Ping() error
	Status() (state.Phases, error)
	JobStatus() (*state.Job, error)
	Sync(bundle, worktree, branch string) error
	Apply(ct *contract.Contract, worktree string) (state.Phases, error)
	Run(jobID string, spec contract.Exec, worktree string, emit func([]byte)) (*state.Job, error)
	Logs(phase, service string, lines int) (string, error)
	JobLog(jobID string, lines int) (string, error)
}

// handoff applies the box's contract through the guest agent: wait for the
// agent, sync the worktree once, send the contract, and record the phases.
// It runs after the box is reachable; phases then proceed in the background.
func (r *Runner) handoff(ctx context.Context, box *state.Box, boxDir string) error {
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return err
	}
	client := r.NewAgent(vsockPath(boxDir))

	pingCtx, cancel := context.WithTimeout(ctx, r.AgentTimeout)
	defer cancel()
	for {
		if err := client.Ping(); err == nil {
			break
		}
		select {
		case <-pingCtx.Done():
			return fmt.Errorf("agent did not answer: %w", pingCtx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}

	status, err := client.Status()
	if err != nil {
		return err
	}
	if !status.Synced {
		bundle := filepath.Join(boxDir, "sync.bundle")
		if err := r.MakeBundle(ctx, box.Worktree, bundle); err != nil {
			return err
		}
		defer os.Remove(bundle)
		if err := client.Sync(bundle, boxWorktreePath(box), box.Branch); err != nil {
			return err
		}
	}
	status, err = client.Apply(ct, boxWorktreePath(box))
	if err != nil {
		return err
	}
	// Store the schedules the box just applied, so the daemon fires them
	// across restarts and reboots (ADR 0003), and record what was applied so
	// status can tell when the worktree's contract drifts; the apply above is
	// the only thing that makes them current.
	if _, err := r.Store.SetSchedules(box.ID, contractSchedules(ct), time.Now().UTC()); err != nil {
		return err
	}
	if _, err := r.Store.SetContractHash(box.ID, ct.Hash()); err != nil {
		return err
	}
	_, err = r.Store.SetPhases(box.ID, status)
	return err
}

// contractSchedules converts a contract's schedules into box-record entries.
// Only the declaration is carried here; the store preserves the arm time and
// last-fired clock of entries that did not change.
func contractSchedules(ct *contract.Contract) []state.Schedule {
	if len(ct.Schedules) == 0 {
		return nil
	}
	out := make([]state.Schedule, 0, len(ct.Schedules))
	for _, sched := range ct.Schedules {
		out = append(out, state.Schedule{Name: sched.Name, Cron: sched.Cron, Job: sched.Job})
	}
	return out
}

// ContractStale reports whether the contract now on disk in the box's
// worktree differs from the one the box applied at its last handoff. It is
// silent when there is nothing to compare: a box that never recorded a hash,
// or a contract that cannot be read or parsed (a broken contract is the next
// handoff's to report, not status's to guess about). The daemon computes this
// on the host, so a remote CLI never needs the worktree.
func (r *Runner) ContractStale(box *state.Box) bool {
	if box.ContractHash == "" {
		return false
	}
	ct, err := contract.Load(box.Worktree)
	if err != nil {
		return false
	}
	return ct.Hash() != box.ContractHash
}

// Refresh asks the agent for the latest phases and job and persists them. A
// box that is not running keeps its last known state. The job merge is how a
// run that outlived the daemon gets its real outcome back.
func (r *Runner) Refresh(box *state.Box) (*state.Box, error) {
	if box.State != state.StateRunning {
		return box, nil
	}
	client := r.NewAgent(vsockPath(r.boxDir(box.ID)))
	status, err := client.Status()
	if err != nil {
		return box, err
	}
	box, err = r.Store.SetPhases(box.ID, status)
	if err != nil {
		return box, err
	}
	job, err := client.JobStatus()
	if err != nil || job == nil {
		return box, nil
	}
	if updated, err := r.Store.SetJob(box.ID, *job); err == nil {
		box = updated
	}
	return box, nil
}

// Logs returns a box's phase log or service journal from the agent.
func (r *Runner) Logs(box *state.Box, phase, service string, lines int) (string, error) {
	if box.State != state.StateRunning {
		return "", fmt.Errorf("box %s is not running; start it with 'pluto up'", shortID(box.ID))
	}
	client := r.NewAgent(vsockPath(r.boxDir(box.ID)))
	return client.Logs(phase, service, lines)
}

// boxWorktreePath is where the repo lives inside the box.
func boxWorktreePath(box *state.Box) string {
	project := box.Project
	if project == "" {
		project = "worktree"
	}
	return filepath.Join("/home/dev/work", project)
}

// makeBundle writes a git bundle of every ref in the worktree: the unit of
// sync into the box. Only committed state travels, so gitignored files never
// cross over.
func makeBundle(ctx context.Context, worktree, out string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "bundle", "create", out, "--all")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git bundle: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
