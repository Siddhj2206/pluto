// Package agent is the box-side process that applies a repository's
// contract: provision once, wake on every start, services as supervised
// systemd user units. It listens on vsock; the daemon drives it and reads
// back phase status (ADR 0007).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Port is the vsock port the agent listens on inside the box.
const Port = 1024

// ErrTimeout reports a hook that outlived its timebox.
var ErrTimeout = errors.New("timed out")

// System runs hooks, services, and clones for the agent. The real
// implementation drives the box's user systemd and git; tests replace it.
type System interface {
	// RunHook runs one hook in a transient user unit and returns its exit
	// code. Leftover processes are reaped when the unit stops.
	RunHook(ctx context.Context, name, worktree, command string, timeout time.Duration, logPath string) (int, error)
	// RestartServices renders and restarts the declared services, returning
	// their observed state.
	RestartServices(worktree string, services map[string]contract.Service) ([]state.ServiceStatus, error)
	// Statuses observes the declared services.
	Statuses(services map[string]contract.Service) []state.ServiceStatus
	// ServiceLog returns the recent journal for one service.
	ServiceLog(name string, lines int) (string, error)
	// CloneRepo clones a bundle into the box worktree and checks out branch.
	CloneRepo(ctx context.Context, bundle, worktree, branch string) error
}

// Agent applies contracts inside one box. It is safe for concurrent use.
type Agent struct {
	root   string
	logDir string
	system System

	mu      sync.Mutex
	status  state.Phases
	busy    bool
	syncing bool
}

// New loads the previous status, if any, from root and marks phases a
// previous agent instance left running as failed: they died with it.
func New(root string, system System) (*Agent, error) {
	a := &Agent{
		root:   root,
		logDir: filepath.Join(root, "logs"),
		system: system,
	}
	if err := os.MkdirAll(a.logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create agent state dir: %w", err)
	}
	data, err := os.ReadFile(a.statusPath())
	if err == nil {
		_ = json.Unmarshal(data, &a.status)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read agent status: %w", err)
	}
	a.status.BootID = bootID()
	if a.status.Provision.State == state.PhaseRunning {
		a.status.Provision = state.PhaseStatus{State: state.PhaseFailed, Error: "agent restarted during provision"}
	}
	if a.status.Wake.State == state.PhaseRunning {
		a.status.Wake = state.PhaseStatus{State: state.PhaseFailed, Error: "agent restarted during wake"}
	}
	return a, nil
}

// bootID is the guest's per-boot identifier. It changes whenever the VMM
// restarts, which is how the daemon knows a wake is owed.
func bootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Status returns the last known phase state.
func (a *Agent) Status() state.Phases {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// Sync clones the bundled repository into the box worktree, once. Later ups
// are no-ops: the box's copy is the live one and git is the floor. The mutex
// is not held across the clone.
func (a *Agent) Sync(ctx context.Context, bundle, worktree, branch string) error {
	a.mu.Lock()
	if a.status.Synced {
		a.mu.Unlock()
		return nil
	}
	if a.syncing {
		a.mu.Unlock()
		return errors.New("sync already in progress")
	}
	a.syncing = true
	a.mu.Unlock()

	err := a.system.CloneRepo(ctx, bundle, worktree, branch)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncing = false
	if err != nil {
		return err
	}
	a.status.Synced = true
	a.status.Worktree = worktree
	a.persistLocked()
	return nil
}

// Apply records the contract and kicks its phases. It returns immediately:
// provision and wake run in the background, so a slow phase never blocks the
// box (ADR 0007). A provision that failed is retried on the next apply; one
// that succeeded never runs again.
func (a *Agent) Apply(c *contract.Contract, worktree string) state.Phases {
	a.mu.Lock()
	defer a.mu.Unlock()
	if worktree != "" {
		a.status.Worktree = worktree
	}
	needProvision := c.Provision != nil && a.status.Provision.State != state.PhaseDone
	needWake := c.Wake != nil
	needServices := len(c.Services) > 0
	if a.busy || (!needProvision && !needWake && !needServices) {
		a.persistLocked()
		return a.status
	}
	// Mark what is about to run so the caller sees it immediately.
	if needProvision {
		*a.phaseStatus("provision") = state.PhaseStatus{State: state.PhaseRunning}
	}
	if needWake {
		*a.phaseStatus("wake") = state.PhaseStatus{State: state.PhaseRunning}
	}
	a.busy = true
	go a.runPhases(c, worktree)
	a.persistLocked()
	return a.status
}

// runPhases is the background phase sequence: provision (once), then wake,
// then services. A failed provision stops the sequence and keeps the machine
// as the debugging surface; a failed wake does not block services.
func (a *Agent) runPhases(c *contract.Contract, worktree string) {
	ctx := context.Background()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.persistLocked()
		a.mu.Unlock()
	}()

	if c.Provision != nil && a.phaseState("provision") != state.PhaseDone {
		if !a.runHook(ctx, "provision", worktree, c.Provision.Command, c.ProvisionTimeout()) {
			return
		}
	}
	if c.Wake != nil {
		a.runHook(ctx, "wake", worktree, c.Wake.Command, c.WakeTimeout())
	}
	if len(c.Services) > 0 {
		statuses, err := a.system.RestartServices(worktree, c.Services)
		a.mu.Lock()
		// Keep whatever the system reports, even on a partial failure; the
		// per-service states carry the truth.
		if statuses != nil {
			a.status.Services = statuses
		}
		a.mu.Unlock()
		if err != nil {
			a.appendLogLine("wake", "services: "+err.Error())
		}
	}
}

// runHook runs one phase, updating status and the phase log. It reports
// whether the phase succeeded.
func (a *Agent) runHook(ctx context.Context, phase, worktree, command string, timeout time.Duration) bool {
	start := time.Now().UTC()
	a.setPhase(phase, state.PhaseStatus{State: state.PhaseRunning, StartedAt: &start})
	a.appendLogHeader(phase, command, timeout)

	exit, err := a.system.RunHook(ctx, phase, worktree, command, timeout, a.logPath(phase))
	finish := time.Now().UTC()
	result := state.PhaseStatus{ExitCode: exit, StartedAt: &start, FinishedAt: &finish}
	switch {
	case err != nil:
		result.State = state.PhaseFailed
		result.Error = err.Error()
	case exit != 0:
		result.State = state.PhaseFailed
		result.Error = fmt.Sprintf("exit %d", exit)
	default:
		result.State = state.PhaseDone
	}
	a.setPhase(phase, result)
	return result.State == state.PhaseDone
}

// Logs returns the tail of a phase log, or a service's journal.
func (a *Agent) Logs(phase, service string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	if service != "" {
		return a.system.ServiceLog(service, lines)
	}
	switch phase {
	case "provision", "wake":
		return tailFile(a.logPath(phase), lines)
	default:
		return "", fmt.Errorf("unknown phase %q", phase)
	}
}

// phaseStatus points at a phase's record; the caller holds the mutex.
func (a *Agent) phaseStatus(name string) *state.PhaseStatus {
	if name == "provision" {
		return &a.status.Provision
	}
	return &a.status.Wake
}

func (a *Agent) phaseState(phase string) state.PhaseState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phaseStatus(phase).State
}

func (a *Agent) setPhase(phase string, st state.PhaseStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	*a.phaseStatus(phase) = st
	a.status.UpdatedAt = time.Now().UTC()
	a.persistLocked()
}

func (a *Agent) appendLogHeader(phase, command string, timeout time.Duration) {
	f, err := os.OpenFile(a.logPath(phase), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "\n=== %s %s (timeout %s): %s\n", phase, time.Now().UTC().Format(time.RFC3339), timeout, command)
}

// appendLogLine adds one line to a phase log.
func (a *Agent) appendLogLine(phase, line string) {
	f, err := os.OpenFile(a.logPath(phase), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func (a *Agent) logPath(phase string) string { return filepath.Join(a.logDir, phase+".log") }
func (a *Agent) statusPath() string          { return filepath.Join(a.root, "status.json") }

// persistLocked writes status atomically; the caller holds the mutex.
func (a *Agent) persistLocked() {
	a.status.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(a.status, "", "  ")
	if err != nil {
		return
	}
	tmp := a.statusPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, a.statusPath())
}

// tailFile returns the last n lines of a file; a missing file is empty.
func tailFile(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n", nil
}
