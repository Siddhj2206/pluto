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
	Sync(bundle, worktree, branch string) error
	Apply(ct *contract.Contract, worktree string) (state.Phases, error)
	Logs(phase, service string, lines int) (string, error)
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
	_, err = r.Store.SetPhases(box.ID, status)
	return err
}

// Refresh asks the agent for the latest phases and persists them. A box that
// is not running keeps its last known phases.
func (r *Runner) Refresh(box *state.Box) (*state.Box, error) {
	if box.State != state.StateRunning {
		return box, nil
	}
	client := r.NewAgent(vsockPath(r.boxDir(box.ID)))
	status, err := client.Status()
	if err != nil {
		return box, err
	}
	return r.Store.SetPhases(box.ID, status)
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
