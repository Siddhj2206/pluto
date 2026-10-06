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
	"github.com/Siddhj2206/pluto/internal/fsutil"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Port is the vsock port the agent listens on inside the box.
const Port = 1024

// ErrTimeout reports a hook that outlived its timebox.
var ErrTimeout = errors.New("timed out")

// System runs hooks, services, jobs, and clones for the agent. The real
// implementation drives the box's user systemd and git; tests replace it.
type System interface {
	// RunHook runs one hook in a transient user unit and returns its exit
	// code. Leftover processes are reaped when the unit stops.
	RunHook(ctx context.Context, name, worktree string, spec contract.Exec, logPath string) (int, error)
	// RunJob runs one bounded command as a per-job user unit, streaming its
	// output to emit, and returns its exit code.
	RunJob(ctx context.Context, jobID, worktree string, spec contract.Exec, logPath string, emit func([]byte)) (int, error)
	// StopJob stops a job's unit, if it exists. A restarted agent uses it to
	// clean up a job it can no longer supervise.
	StopJob(jobID string) error
	// Sessions counts the box's live ssh sessions: the client-attached signal
	// auto-pause consults. An error means the count is unknown.
	Sessions() (int, error)
	// RestartServices renders and restarts the declared services, returning
	// their observed state. baseEnv is the contract's top-level environment
	// with PLUTO_WORKTREE, applied under each service's own env.
	RestartServices(worktree string, services map[string]contract.Service, baseEnv map[string]string) ([]state.ServiceStatus, error)
	// Statuses observes the declared services.
	Statuses(services map[string]contract.Service) []state.ServiceStatus
	// StartSessions renders and (re)starts each declared session as a
	// supervised user unit, returning their observed state. baseEnv is the
	// contract's top-level environment with PLUTO_WORKTREE, applied under each
	// session's own env.
	StartSessions(worktree string, sessions map[string]contract.Session, baseEnv map[string]string) ([]state.SessionStatus, error)
	// SessionStatuses observes the declared sessions, including whether a
	// client is attached.
	SessionStatuses(sessions map[string]contract.Session) []state.SessionStatus
	// SessionUsage returns the cumulative cgroup v2 CPU and IO the declared
	// sessions have consumed, summed across sessions. The counters only grow,
	// so the daemon compares successive readings; a failed read is an error,
	// not a zero, so unknown is never mistaken for idle.
	SessionUsage(sessions map[string]contract.Session) (state.SessionUsage, error)
	// ServiceLog returns the recent journal for one service.
	ServiceLog(name string, lines int) (string, error)
	// CloneRepo clones a bundle into the box worktree and checks out branch.
	CloneRepo(ctx context.Context, bundle, worktree, branch string) error
	// MirrorRemotes recreates the host worktree's remotes in the box: names,
	// fetch URLs, distinct push URLs, and the default fetch refspec. It is
	// idempotent and additive.
	MirrorRemotes(worktree string, remotes []state.Remote) error
	// TrackBranch sets the branch's upstream to the tracked remote (origin, or
	// the sole remote) and push.default=current. It leaves the branch untracked
	// when several remotes exist and none is origin.
	TrackBranch(worktree, branch string, remotes []state.Remote) error
	// HasCheckout reports whether the worktree holds a usable git checkout:
	// a repo with a resolvable HEAD, not a directory left by an interrupted
	// clone.
	HasCheckout(worktree string) bool
}

// Agent applies contracts inside one box. It is safe for concurrent use.
type Agent struct {
	root   string
	logDir string
	system System

	mu       sync.Mutex
	status   state.Phases
	sessions map[string]contract.Session
	job      *state.Job
	busy     bool
	syncing  bool
}

// New loads the previous status, if any, from root and marks phases a
// previous agent instance left running as failed: they died with it. The
// same reconciliation marks a job that outlived its supervisor as failed;
// the daemon uses the job record to recover a run after a daemon restart.
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

	if data, err := os.ReadFile(a.jobPath()); err == nil {
		var job state.Job
		if err := json.Unmarshal(data, &job); err == nil && job.ID != "" {
			if job.State == state.JobRunning {
				// The job's unit can outlive the agent. Stop it: this agent
				// cannot supervise it, and a job still running in the box
				// would break the one-job-at-a-time rule.
				_ = system.StopJob(job.ID)
				job.Finish(state.JobFailed, 0, "agent restarted during the job")
			}
			a.job = &job
		}
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

// Status returns the last known phase state, with the live client count
// folded in. Sessions are observed now, not cached: an ssh session can come
// and go without the agent being asked anything else. A failed count leaves
// Clients nil — unknown — so the daemon never mistakes it for "no client".
func (a *Agent) Status() state.Phases {
	a.mu.Lock()
	defer a.mu.Unlock()
	status := a.status
	status.Clients = nil
	if n, err := a.system.Sessions(); err == nil {
		status.Clients = &n
	}
	// The sessions' cumulative cgroup work is observed live: a turn can burn
	// CPU/IO without the agent being asked to apply anything. A failed read
	// leaves SessionUsage nil — unknown — so auto-pause keeps the box awake
	// rather than mistaking it for an idle session.
	status.SessionUsage = nil
	if usage, err := a.system.SessionUsage(a.sessions); err == nil {
		status.SessionUsage = &usage
	}
	// Declared sessions are observed live too: a client can attach or detach
	// without the agent being asked to apply anything.
	if len(a.sessions) > 0 {
		if live := a.system.SessionStatuses(a.sessions); live != nil {
			status.Sessions = live
		}
	}
	return status
}

// Job returns the box's latest job as the agent knows it, or nil. The daemon
// merges it back into the box record, which recovers a run the daemon lost
// contact with (a restart, a closed laptop).
func (a *Agent) Job() *state.Job {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.job == nil {
		return nil
	}
	job := *a.job
	return &job
}

// RunJob runs a resolved command in the box, streams its output through emit,
// and records the outcome. One job runs at a time: a concurrent call fails.
// The job is persisted, so its outcome survives a daemon or agent restart.
func (a *Agent) RunJob(jobID string, spec contract.Exec, worktree string, emit func([]byte)) (*state.Job, error) {
	if !state.ValidID(jobID) {
		return nil, fmt.Errorf("invalid job id %q", jobID)
	}
	if spec.Command.IsZero() {
		return nil, errors.New("job command is required")
	}
	if worktree == "" {
		worktree = a.Status().Worktree
	}
	if worktree == "" {
		return nil, errors.New("job worktree is unknown")
	}
	spec.Env = withWorktree(spec.Env, worktree)
	job := state.StartJobCommand(jobID, spec.Command.String())

	a.mu.Lock()
	if a.job != nil && a.job.State == state.JobRunning {
		running := a.job.Command
		a.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", state.ErrJobRunning, running)
	}
	a.job = &job
	a.persistJobLocked()
	a.mu.Unlock()

	exit, err := a.system.RunJob(context.Background(), jobID, worktree, spec, a.jobLogPath(jobID), emit)

	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case err != nil:
		job.Finish(state.JobFailed, 0, err.Error())
	case exit != 0:
		job.Finish(state.JobFailed, exit, "")
	default:
		job.Finish(state.JobDone, 0, "")
	}
	a.job = &job
	a.persistJobLocked()
	return &job, nil
}

// Sync clones the bundled repository into the box worktree, once. Later ups
// are no-ops: the box's copy is the live one and git is the floor. A worktree
// that already exists is adopted: an abrupt stop can lose the agent's state
// while the durable disk keeps the checkout, and cloning over it would fail.
// remotes is the host worktree's remote list; it is mirrored into the box and
// the checked-out branch's upstream is set from it (ADR 0008). An empty list
// means the host had no remotes and the box is local-only.
func (a *Agent) Sync(ctx context.Context, bundle, worktree, branch string, remotes []state.Remote) error {
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

	var err error
	if a.system.HasCheckout(worktree) {
		// The box's copy survived; do not clone over it.
	} else {
		err = a.system.CloneRepo(ctx, bundle, worktree, branch)
	}
	if err == nil {
		err = a.system.MirrorRemotes(worktree, remotes)
	}
	if err == nil {
		err = a.system.TrackBranch(worktree, branch, remotes)
	}

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
	needProvision := c.HasProvision() && a.status.Provision.State != state.PhaseDone
	needWake := c.Wake != nil
	needServices := len(c.Services) > 0
	needSessions := len(c.Sessions) > 0
	if a.busy || (!needProvision && !needWake && !needServices && !needSessions) {
		a.persistLocked()
		return a.status
	}
	// Mark what is about to run so the caller sees it immediately. Wake is
	// gated on provision: while provision runs, wake has not started, and
	// marking it running would misreport a slow or failed provision.
	if needProvision {
		*a.phaseStatus("provision") = state.PhaseStatus{State: state.PhaseRunning}
	} else if needWake {
		*a.phaseStatus("wake") = state.PhaseStatus{State: state.PhaseRunning}
	}
	// Remember the sessions this apply is responsible for, so status can
	// observe their live state between applies.
	a.sessions = c.Sessions
	a.busy = true
	go a.runPhases(c, worktree)
	a.persistLocked()
	return a.status
}

// runPhases is the background phase sequence: provision (once), then wake,
// then services and sessions. A failed provision stops the sequence and keeps
// the machine as the debugging surface; a failed wake does not block services
// or sessions.
func (a *Agent) runPhases(c *contract.Contract, worktree string) {
	ctx := context.Background()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.persistLocked()
		a.mu.Unlock()
	}()

	if c.HasProvision() && a.phaseState("provision") != state.PhaseDone {
		if !a.runHook(ctx, "provision", worktree, provisionExec(c, worktree)) {
			// Provision is the gate: a failure stops the sequence, and wake
			// was never marked running.
			return
		}
	}
	if c.Wake != nil {
		a.runHook(ctx, "wake", worktree, phaseExec(c, c.Wake, worktree, c.WakeTimeout()))
	}
	if len(c.Services) > 0 {
		statuses, err := a.system.RestartServices(worktree, c.Services, withWorktree(c.EnvFor(nil), worktree))
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
	if len(c.Sessions) > 0 {
		statuses, err := a.system.StartSessions(worktree, c.Sessions, withWorktree(c.EnvFor(nil), worktree))
		a.mu.Lock()
		// sessions are (re)started on every wake; keep whatever the system
		// reports, even on a partial failure.
		if statuses != nil {
			a.status.Sessions = statuses
		}
		a.mu.Unlock()
		if err != nil {
			a.appendLogLine("wake", "sessions: "+err.Error())
		}
	}
}

// phaseExec resolves a provision or wake hook into an executable spec: its
// command, dir, timebox, and the merged environment plus PLUTO_WORKTREE.
func phaseExec(c *contract.Contract, p *contract.Phase, worktree string, timeout time.Duration) contract.Exec {
	return contract.Exec{
		Command: p.Command,
		Dir:     p.Dir,
		Env:     withWorktree(c.EnvFor(p.Env), worktree),
		Timeout: timeout,
	}
}

// provisionExec resolves the provision phase: the generated [tools] apt
// preamble composed with any declared [provision] command. The [provision]
// dir and env apply to the composed command; a [tools]-only contract uses the
// contract's top-level env and the default timebox.
func provisionExec(c *contract.Contract, worktree string) contract.Exec {
	var dir string
	var env map[string]string
	if c.Provision != nil {
		dir, env = c.Provision.Dir, c.Provision.Env
	}
	return contract.Exec{
		Command: c.ProvisionCommand(),
		Dir:     dir,
		Env:     withWorktree(c.EnvFor(env), worktree),
		Timeout: c.ProvisionTimeout(),
	}
}

// withWorktree sets PLUTO_WORKTREE, the one built-in M1 adds to every
// command's environment (ADR 0007).
func withWorktree(env map[string]string, worktree string) map[string]string {
	out := contract.MergeEnv(nil, env)
	if out == nil {
		out = map[string]string{}
	}
	out["PLUTO_WORKTREE"] = worktree
	return out
}

// runHook runs one phase, updating status and the phase log. It reports
// whether the phase succeeded.
func (a *Agent) runHook(ctx context.Context, phase, worktree string, spec contract.Exec) bool {
	start := time.Now().UTC()
	a.setPhase(phase, state.PhaseStatus{State: state.PhaseRunning, StartedAt: &start})
	a.appendLogHeader(phase, spec.Command.String(), spec.Timeout)

	exit, err := a.system.RunHook(ctx, phase, worktree, spec, a.logPath(phase))
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
		return fsutil.TailFile(a.logPath(phase), lines)
	default:
		return "", fmt.Errorf("unknown phase %q", phase)
	}
}

// JobLog returns the tail of a job's output as recorded in the box.
func (a *Agent) JobLog(jobID string, lines int) (string, error) {
	if !state.ValidID(jobID) {
		return "", fmt.Errorf("invalid job id %q", jobID)
	}
	if lines <= 0 {
		lines = 100
	}
	return fsutil.TailFile(a.jobLogPath(jobID), lines)
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
func (a *Agent) jobPath() string             { return filepath.Join(a.root, "job.json") }
func (a *Agent) jobLogPath(jobID string) string {
	return filepath.Join(a.logDir, "jobs", jobID+".log")
}

// persistLocked writes status atomically; the caller holds the mutex.
func (a *Agent) persistLocked() {
	a.status.UpdatedAt = time.Now().UTC()
	a.persistJSON(a.statusPath(), a.status)
}

// persistJobLocked writes the latest job atomically; the caller holds the mutex.
func (a *Agent) persistJobLocked() {
	if a.job == nil {
		return
	}
	a.persistJSON(a.jobPath(), a.job)
}

// persistJSON writes v to path durably. The agent's state is the box's
// memory of provision and sync, so an abrupt stop must not lose it: the
// write is fsynced before the rename, and the directory after it.
func (a *Agent) persistJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		return
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
