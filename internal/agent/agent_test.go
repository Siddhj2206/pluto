package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

type fakeSystem struct {
	mu          sync.Mutex
	hooks       []string
	hookSpecs   map[string]contract.Exec
	clones      []string
	cloneData   []byte
	restarts    int
	restartErr  error
	timeouts    map[string]time.Duration
	hookExit    map[string]int
	hookErr     map[string]error
	block       map[string]chan struct{}
	jobSpecs    []contract.Exec
	jobExit     int
	jobErr      error
	jobChunks   []string
	jobBlock    chan struct{}
	stoppedJobs []string
	sessions    int
	sessionsErr error
	hasCheckout bool

	sessionStarts   int
	sessionErr      error
	sessionEnv      map[string]string
	sessionSpecs    map[string]contract.Session
	attached        map[string]bool
	sessionUsage    state.SessionUsage
	sessionUsageErr error

	remotes       []state.Remote
	mirrorCalls   int
	trackCalls    int
	trackedBranch string
}

func (f *fakeSystem) MirrorRemotes(worktree string, remotes []state.Remote) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mirrorCalls++
	f.remotes = remotes
	return nil
}

func (f *fakeSystem) TrackBranch(worktree, branch string, remotes []state.Remote) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trackCalls++
	f.trackedBranch = branch
	return nil
}

func (f *fakeSystem) HasCheckout(worktree string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasCheckout
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		timeouts:  map[string]time.Duration{},
		hookExit:  map[string]int{},
		hookErr:   map[string]error{},
		hookSpecs: map[string]contract.Exec{},
		block:     map[string]chan struct{}{},
		attached:  map[string]bool{},
	}
}

func (f *fakeSystem) RunJob(ctx context.Context, jobID, worktree string, spec contract.Exec, logPath string, emit func([]byte)) (int, error) {
	f.mu.Lock()
	f.jobSpecs = append(f.jobSpecs, spec)
	chunks := f.jobChunks
	block := f.jobBlock
	exit, err := f.jobExit, f.jobErr
	f.mu.Unlock()
	for _, chunk := range chunks {
		emit([]byte(chunk))
	}
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			_ = os.WriteFile(logPath, []byte(strings.Join(chunks, "")), 0o644)
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return exit, err
}

func (f *fakeSystem) StopJob(jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedJobs = append(f.stoppedJobs, jobID)
	return nil
}

func (f *fakeSystem) Sessions() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessionsErr != nil {
		return 0, f.sessionsErr
	}
	return f.sessions, nil
}

func (f *fakeSystem) RunHook(ctx context.Context, name, worktree string, spec contract.Exec, logPath string) (int, error) {
	f.mu.Lock()
	f.hooks = append(f.hooks, name)
	f.hookSpecs[name] = spec
	f.timeouts[name] = spec.Timeout
	block := f.block[name]
	err := f.hookErr[name]
	exit := f.hookExit[name]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if err != nil {
		return 0, err
	}
	return exit, nil
}

// hookSpec returns the spec the named hook last received.
func (f *fakeSystem) hookSpec(name string) contract.Exec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hookSpecs[name]
}

func (f *fakeSystem) RestartServices(worktree string, services map[string]contract.Service, baseEnv map[string]string) ([]state.ServiceStatus, error) {
	f.mu.Lock()
	f.restarts++
	err := f.restartErr
	f.mu.Unlock()
	return f.Statuses(services), err
}

// jobSpec returns the spec the last job run received.
func (f *fakeSystem) jobSpec() contract.Exec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobSpecs) == 0 {
		return contract.Exec{}
	}
	return f.jobSpecs[len(f.jobSpecs)-1]
}

func (f *fakeSystem) timeout(name string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.timeouts[name]
}

func (f *fakeSystem) Statuses(services map[string]contract.Service) []state.ServiceStatus {
	var out []state.ServiceStatus
	for name, svc := range services {
		out = append(out, state.ServiceStatus{Name: name, State: "active", Port: svc.Port, Description: svc.Description})
	}
	return out
}

// StartSessions records the declared sessions it was handed and reports each
// one running, with the fake's attached state.
func (f *fakeSystem) StartSessions(worktree string, sessions map[string]contract.Session, baseEnv map[string]string) ([]state.SessionStatus, error) {
	f.mu.Lock()
	f.sessionStarts++
	f.sessionSpecs = sessions
	f.sessionEnv = baseEnv
	err := f.sessionErr
	f.mu.Unlock()
	return f.SessionStatuses(sessions), err
}

func (f *fakeSystem) SessionStatuses(sessions map[string]contract.Session) []state.SessionStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]state.SessionStatus, 0, len(names))
	for _, name := range names {
		out = append(out, state.SessionStatus{
			Name:        name,
			State:       "running",
			Attached:    f.attached[name],
			Description: sessions[name].Description,
		})
	}
	return out
}

// SessionUsage reports the fake's configured cumulative counters. An error
// stands in for an unreadable session cgroup.
func (f *fakeSystem) SessionUsage(sessions map[string]contract.Session) (state.SessionUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionUsage, f.sessionUsageErr
}

// sessionStartCount is the number of StartSessions calls the system has seen.
func (f *fakeSystem) sessionStartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionStarts
}

func (f *fakeSystem) ServiceLog(name string, lines int) (string, error) {
	return "journal of " + name, nil
}

func (f *fakeSystem) CloneRepo(ctx context.Context, bundle, worktree, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clones = append(f.clones, worktree)
	if data, err := os.ReadFile(bundle); err == nil {
		f.cloneData = data
	}
	return nil
}

func (f *fakeSystem) hooksNamed(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, h := range f.hooks {
		if h == name {
			out = append(out, h)
		}
	}
	return out
}

// restartCount is the number of service restarts the system has seen.
func (f *fakeSystem) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}

func (f *fakeSystem) setExit(name string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hookExit[name] = code
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitIdle waits for the background phase sequence to finish. Apply returns
// before provision, wake, and services run; without this a test can return
// while runPhases is still writing status and logs into the test's temp dir,
// and the cleanup races those writes. It also matters before a second Apply:
// Apply is a no-op while the agent is busy.
func waitIdle(t *testing.T, ag *Agent) {
	t.Helper()
	waitFor(t, "agent idle", func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.busy
	})
}

func testContract(t *testing.T) *contract.Contract {
	t.Helper()
	c, err := contract.Parse(`
[provision]
command = "true"

[wake]
command = "true"

[services.web]
command = "serve"
port = 3000
`)
	if err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	return c
}

func TestApplyRunsProvisionOnceThenWakeAndServices(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct := testContract(t)

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitFor(t, "wake done", func() bool { return ag.Status().Wake.State == state.PhaseDone })
	waitFor(t, "services", func() bool { return len(ag.Status().Services) == 1 })
	if got := ag.Status().Services[0]; got.Name != "web" || got.State != "active" || got.Port != 3000 {
		t.Fatalf("service = %+v", got)
	}
	if got := len(sys.hooksNamed("provision")); got != 1 {
		t.Fatalf("provision hooks = %d, want 1", got)
	}
	if got := len(sys.hooksNamed("wake")); got != 1 {
		t.Fatalf("wake hooks = %d, want 1", got)
	}

	// A second up never re-provisions, but wakes and restarts services. The
	// restart lands after the wake hook returns, so wait on the restart, not
	// the hook. The first sequence must be idle or the second Apply is a
	// no-op.
	waitIdle(t, ag)
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "second wake", func() bool { return len(sys.hooksNamed("wake")) == 2 })
	waitFor(t, "second service restart", func() bool { return sys.restartCount() == 2 })
	if got := len(sys.hooksNamed("provision")); got != 1 {
		t.Fatalf("provision re-ran: %d hooks", got)
	}
	waitIdle(t, ag)
}

func TestWakeIsNotMarkedRunningWhileProvisionRuns(t *testing.T) {
	sys := newFakeSystem()
	block := make(chan struct{})
	sys.block["provision"] = block
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ag.Apply(testContract(t), "/home/dev/work/x")
	if st := ag.Status(); st.Provision.State != state.PhaseRunning || st.Wake.State != "" {
		t.Fatalf("status = provision %q, wake %q; want provision running and wake unstarted", st.Provision.State, st.Wake.State)
	}
	close(block)
	waitFor(t, "wake done", func() bool { return ag.Status().Wake.State == state.PhaseDone })
	waitIdle(t, ag)
}

func TestFailedProvisionSkipsWakeAndRetries(t *testing.T) {
	sys := newFakeSystem()
	sys.setExit("provision", 7)
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct := testContract(t)

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "failed provision with wake unstarted", func() bool {
		st := ag.Status()
		return st.Provision.State == state.PhaseFailed && st.Wake.State == ""
	})
	waitIdle(t, ag)
	st := ag.Status()
	if st.Provision.ExitCode != 7 || !strings.Contains(st.Provision.Error, "exit 7") {
		t.Fatalf("provision status = %+v", st.Provision)
	}
	if got := len(sys.hooksNamed("wake")); got != 0 {
		t.Fatalf("wake ran after a failed provision: %d", got)
	}
	if got := sys.restartCount(); got != 0 {
		t.Fatalf("services started after a failed provision: %d", got)
	}

	// A later up retries the failed provision.
	sys.setExit("provision", 0)
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	if got := len(sys.hooksNamed("provision")); got != 2 {
		t.Fatalf("provision retries = %d, want 2", got)
	}
	waitIdle(t, ag)
}

func TestSlowWakeDoesNotBlockApply(t *testing.T) {
	sys := newFakeSystem()
	block := make(chan struct{})
	sys.block["wake"] = block
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[wake]\ncommand = \"sleep 60\"\ntimeout = \"5s\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	start := time.Now()
	ag.Apply(ct, "/home/dev/work/x")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Apply blocked for %s", elapsed)
	}
	waitFor(t, "wake running", func() bool { return ag.Status().Wake.State == state.PhaseRunning })
	close(block)
	waitFor(t, "wake done", func() bool { return ag.Status().Wake.State == state.PhaseDone })
	waitIdle(t, ag)
}

func TestStatusReportsAttachedClients(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sys.sessions = 2
	if got := ag.Status().Clients; got == nil || *got != 2 {
		t.Fatalf("clients = %v, want 2", got)
	}
	sys.sessions = 0
	if got := ag.Status().Clients; got == nil || *got != 0 {
		t.Fatalf("clients after detach = %v, want a known zero", got)
	}
	// A failed count is unknown, never zero: the daemon must not pause on it.
	sys.sessionsErr = errors.New("pgrep failed")
	if got := ag.Status().Clients; got != nil {
		t.Fatalf("clients = %v, want unknown when the count fails", got)
	}
}

func TestStatusReportsSessionUsageAndKeepsAFailureUnknown(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sys.sessionUsage = state.SessionUsage{CPUUsec: 1234, IOBytes: 5678}
	got := ag.Status().SessionUsage
	if got == nil || got.CPUUsec != 1234 || got.IOBytes != 5678 {
		t.Fatalf("SessionUsage = %v, want the system's reading", got)
	}
	// A failed read is unknown, never zero: the daemon must not pause on it.
	sys.sessionUsageErr = errors.New("cgroup unreadable")
	if got := ag.Status().SessionUsage; got != nil {
		t.Fatalf("SessionUsage = %v, want unknown when the cgroup cannot be read", got)
	}
}

func TestSyncOnce(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := ag.Sync(ctx, "bundle", "/home/dev/work/x", "master", nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := ag.Sync(ctx, "bundle", "/home/dev/work/x", "master", nil); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(sys.clones) != 1 {
		t.Fatalf("clones = %v, want one", sys.clones)
	}
	if !ag.Status().Synced || ag.Status().Worktree != "/home/dev/work/x" {
		t.Fatalf("status = %+v", ag.Status())
	}
}

func TestSyncAdoptsAnExistingWorktree(t *testing.T) {
	sys := newFakeSystem()
	sys.hasCheckout = true
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ag.Sync(context.Background(), "bundle", "/home/dev/work/x", "master", nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sys.clones) != 0 {
		t.Fatalf("clones = %v, want adoption of the surviving worktree, not a clone", sys.clones)
	}
	if st := ag.Status(); !st.Synced || st.Worktree != "/home/dev/work/x" {
		t.Fatalf("status = %+v, want the surviving worktree marked synced", st)
	}
}

func TestSyncMirrorsTheHostRemotesAndTracksTheBranch(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	remotes := []state.Remote{
		{Name: "origin", Fetch: "https://example.com/acme/app.git"},
		{Name: "upstream", Fetch: "https://example.com/org/app.git"},
	}

	if err := ag.Sync(context.Background(), "bundle", "/home/dev/work/x", "master", remotes); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sys.clones) != 1 {
		t.Fatalf("clones = %v, want one", sys.clones)
	}
	if sys.mirrorCalls != 1 || len(sys.remotes) != 2 || sys.remotes[0].Name != "origin" {
		t.Fatalf("mirrored %d calls with %+v, want the host remote list", sys.mirrorCalls, sys.remotes)
	}
	if sys.trackCalls != 1 || sys.trackedBranch != "master" {
		t.Fatalf("tracked %d calls branch %q, want master", sys.trackCalls, sys.trackedBranch)
	}
}

func TestSyncMirrorsRemotesOnAdoption(t *testing.T) {
	sys := newFakeSystem()
	sys.hasCheckout = true
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	remotes := []state.Remote{{Name: "origin", Fetch: "git@example.com:acme/app.git"}}

	if err := ag.Sync(context.Background(), "bundle", "/home/dev/work/x", "master", remotes); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sys.clones) != 0 {
		t.Fatalf("clones = %v, want adoption, not a clone", sys.clones)
	}
	if sys.mirrorCalls != 1 || len(sys.remotes) != 1 || sys.remotes[0].Fetch != "git@example.com:acme/app.git" {
		t.Fatalf("mirrored %+v, want the surviving checkout configured from the host", sys.remotes)
	}
}

func TestSyncLocalOnlyStillSyncs(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ag.Sync(context.Background(), "bundle", "/home/dev/work/x", "master", nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sys.clones) != 1 {
		t.Fatalf("clones = %v, want the box synced anyway", sys.clones)
	}
	if sys.mirrorCalls != 1 || len(sys.remotes) != 0 {
		t.Fatalf("mirrored %+v, want none for a worktree with no remotes", sys.remotes)
	}
}

func TestStatusSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	sys := newFakeSystem()
	ag, err := New(root, sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ag.Apply(testContract(t), "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitIdle(t, ag)

	restarted, err := New(root, newFakeSystem())
	if err != nil {
		t.Fatalf("restart New: %v", err)
	}
	st := restarted.Status()
	if st.Provision.State != state.PhaseDone || st.Worktree != "/home/dev/work/x" {
		t.Fatalf("status after restart = %+v", st)
	}
}

func TestLogsTail(t *testing.T) {
	root := t.TempDir()
	ag, err := New(root, newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logPath := filepath.Join(root, "logs", "wake.log")
	if err := os.WriteFile(logPath, []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	log, err := ag.Logs("wake", "", 2)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if log != "4\n5\n" {
		t.Fatalf("log tail = %q, want 4,5", log)
	}
	if _, err := ag.Logs("bogus", "", 10); err == nil {
		t.Fatal("unknown phase should fail")
	}
	serviceLog, err := ag.Logs("", "web", 10)
	if err != nil || serviceLog != "journal of web" {
		t.Fatalf("service log = %q, %v", serviceLog, err)
	}
}

func TestHookTimeoutsDefaultWhenUnset(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[provision]\ncommand = \"true\"\n[wake]\ncommand = \"true\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitFor(t, "wake done", func() bool { return ag.Status().Wake.State == state.PhaseDone })
	waitIdle(t, ag)
	if got := sys.timeout("provision"); got != contract.DefaultProvisionTimeout {
		t.Fatalf("provision timeout = %s, want the default", got)
	}
	if got := sys.timeout("wake"); got != contract.DefaultWakeTimeout {
		t.Fatalf("wake timeout = %s, want the default", got)
	}
}

func TestNewReconcilesStaleRunningPhases(t *testing.T) {
	root := t.TempDir()
	sys := newFakeSystem()
	ag, err := New(root, sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ag.mu.Lock()
	ag.status.Provision = state.PhaseStatus{State: state.PhaseRunning}
	ag.persistLocked()
	ag.mu.Unlock()

	restarted, err := New(root, sys)
	if err != nil {
		t.Fatalf("restart New: %v", err)
	}
	st := restarted.Status()
	if st.Provision.State != state.PhaseFailed || !strings.Contains(st.Provision.Error, "restarted") {
		t.Fatalf("provision after restart = %+v, want failed/restarted", st.Provision)
	}
	if st.BootID == "" {
		t.Fatal("agent should report the guest boot id")
	}
}

func TestPartialServiceFailureKeepsStatuses(t *testing.T) {
	sys := newFakeSystem()
	sys.restartErr = errors.New("one service failed to restart")
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[services.web]\ncommand = \"x\"\nport = 80\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "services", func() bool { return len(ag.Status().Services) == 1 })
	// The wake log line is appended just after the statuses are set; wait for
	// the effect under test, not for the status update.
	waitFor(t, "services error logged", func() bool {
		log, err := ag.Logs("wake", "", 10)
		return err == nil && strings.Contains(log, "one service failed to restart")
	})
	log, err := ag.Logs("wake", "", 10)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(log, "one service failed to restart") {
		t.Fatalf("wake log = %q, want the services error", log)
	}
	waitIdle(t, ag)
}

// A sessions-only contract still starts work: the applied sessions are a
// reason to run, not a no-op.
func TestSessionsOnlyContractIsNotANoop(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[sessions.agent]\ncommand = \"opencode\"\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "session started", func() bool { return sys.sessionStartCount() == 1 })
	waitFor(t, "session reported", func() bool { return len(ag.Status().Sessions) == 1 })
	waitIdle(t, ag)
}

// A declared session starts on every apply/wake, is reported with its live
// attach state, and carries the contract's base env and PLUTO_WORKTREE.
func TestApplyStartsDeclaredSessionsAndReportsThem(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse(`
[env]
TOP = "top"

[sessions.agent]
description = "the coding agent"
command = "opencode"
env = { SESS = "yes" }
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "session started", func() bool { return sys.sessionStartCount() == 1 })
	waitFor(t, "session reported", func() bool { return len(ag.Status().Sessions) == 1 })

	got := ag.Status().Sessions[0]
	if got.Name != "agent" || got.State != "running" || got.Description != "the coding agent" {
		t.Fatalf("session status = %+v", got)
	}
	if got.Attached {
		t.Fatalf("a session starts detached: %+v", got)
	}
	if sys.sessionEnv["TOP"] != "top" || sys.sessionEnv["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("session base env = %v", sys.sessionEnv)
	}

	// Attach is observed live: a client attaches without another apply.
	sys.mu.Lock()
	sys.attached["agent"] = true
	sys.mu.Unlock()
	if got := ag.Status().Sessions[0]; !got.Attached {
		t.Fatalf("session status = %+v, want attached", got)
	}

	// A wake (a later apply) restarts the session.
	waitIdle(t, ag)
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "session restarted", func() bool { return sys.sessionStartCount() == 2 })
	waitIdle(t, ag)
}

func TestRunJobStreamsOutputAndRecords(t *testing.T) {
	root := t.TempDir()
	sys := newFakeSystem()
	sys.jobChunks = []string{"building\n", "ok\n"}
	ag, err := New(root, sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := state.NewID()

	var got []byte
	job, err := ag.RunJob(id, contract.Exec{Command: contract.ArgvCommand([]string{"make", "test"})}, "/home/dev/work/x", func(data []byte) {
		got = append(got, data...)
	})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if string(got) != "building\nok\n" {
		t.Fatalf("streamed = %q, want the chunks in order", got)
	}
	if job.ID != id || job.State != state.JobDone || job.Command != "make test" {
		t.Fatalf("job = %+v, want done/id/command", job)
	}
	if job.FinishedAt == nil {
		t.Fatalf("job = %+v, want a finish time", job)
	}
	if got := sys.jobSpec().Command.Argv(); strings.Join(got, " ") != "make test" {
		t.Fatalf("spec passed to the system = %v", got)
	}

	// The outcome survives an agent restart: it is the daemon's recovery path.
	restarted, err := New(root, newFakeSystem())
	if err != nil {
		t.Fatalf("restart New: %v", err)
	}
	if got := restarted.Job(); got == nil || got.ID != id || got.State != state.JobDone {
		t.Fatalf("job after restart = %+v, want the recorded job", got)
	}

	// The box-side log is complete and readable through the agent.
	log, err := restarted.JobLog(id, 10)
	if err != nil {
		t.Fatalf("JobLog: %v", err)
	}
	if log != "building\nok\n" {
		t.Fatalf("job log = %q, want the job's output", log)
	}
}

func TestRunJobRecordsFailure(t *testing.T) {
	sys := newFakeSystem()
	sys.jobExit = 7
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	job, err := ag.RunJob(state.NewID(), contract.Exec{Command: contract.ArgvCommand([]string{"make"})}, "/home/dev/work/x", func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if job.State != state.JobFailed || job.ExitCode != 7 {
		t.Fatalf("job = %+v, want failed exit 7", job)
	}

	sys.jobErr = errors.New("unit failed to start")
	job, err = ag.RunJob(state.NewID(), contract.Exec{Command: contract.ArgvCommand([]string{"make"})}, "/home/dev/work/x", func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	if job.State != state.JobFailed || job.Error != "unit failed to start" {
		t.Fatalf("job = %+v, want failed with the system error", job)
	}
}

func TestRunJobRefusesConcurrent(t *testing.T) {
	sys := newFakeSystem()
	sys.jobBlock = make(chan struct{})
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		if _, err := ag.RunJob(state.NewID(), contract.Exec{Command: contract.ArgvCommand([]string{"sleep"})}, "/home/dev/work/x", func([]byte) {}); err != nil {
			t.Errorf("first RunJob: %v", err)
		}
	}()
	waitFor(t, "first job running", func() bool {
		job := ag.Job()
		return job != nil && job.State == state.JobRunning
	})

	if _, err := ag.RunJob(state.NewID(), contract.Exec{Command: contract.ArgvCommand([]string{"other"})}, "/home/dev/work/x", func([]byte) {}); err == nil {
		t.Fatal("a second RunJob must be refused while one is running")
	}
	close(sys.jobBlock)
	<-firstDone
}

func TestRunJobRequiresWorktreeAndCommand(t *testing.T) {
	ag, err := New(t.TempDir(), newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	make := contract.Exec{Command: contract.ArgvCommand([]string{"make"})}
	if _, err := ag.RunJob(state.NewID(), make, "", func([]byte) {}); err == nil {
		t.Fatal("RunJob without a worktree should fail")
	}
	if _, err := ag.RunJob("not-a-uuid", make, "/home/dev/work/x", func([]byte) {}); err == nil {
		t.Fatal("RunJob with a malformed id should fail")
	}
	if _, err := ag.RunJob(state.NewID(), contract.Exec{}, "/home/dev/work/x", func([]byte) {}); err == nil {
		t.Fatal("RunJob without a command should fail")
	}
}

func TestAgentRestartMarksRunningJobFailed(t *testing.T) {
	root := t.TempDir()
	ag, err := New(root, newFakeSystem())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job := state.Job{ID: state.NewID(), Command: "make", State: state.JobRunning, StartedAt: time.Now().UTC()}
	ag.mu.Lock()
	ag.job = &job
	ag.persistJobLocked()
	ag.mu.Unlock()

	restartSys := newFakeSystem()
	restarted, err := New(root, restartSys)
	if err != nil {
		t.Fatalf("restart New: %v", err)
	}
	got := restarted.Job()
	if got == nil || got.State != state.JobFailed || !strings.Contains(got.Error, "restart") {
		t.Fatalf("job after restart = %+v, want failed/restarted", got)
	}
	if len(restartSys.stoppedJobs) != 1 || restartSys.stoppedJobs[0] != job.ID {
		t.Fatalf("stopped jobs = %v, want the orphaned job stopped", restartSys.stoppedJobs)
	}
}

// A [tools]-only contract is not a no-op: its generated apt install is the
// whole provision.
func TestToolsOnlyContractProvisions(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse("[tools]\npackages = [\"git\", \"curl\"]\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitIdle(t, ag)
	if got := len(sys.hooksNamed("provision")); got != 1 {
		t.Fatalf("provision hooks = %d, want 1", got)
	}
	want := "sudo -n apt-get update && sudo -n apt-get install -y git curl"
	if got := sys.hookSpec("provision").Command.String(); got != want {
		t.Fatalf("provision command = %q, want %q", got, want)
	}
}

// [tools] and [provision] compose: the apt preamble runs first, then the
// declared command, with the [provision] dir and env carried through.
func TestToolsComposeWithProvisionInTheAgent(t *testing.T) {
	sys := newFakeSystem()
	ag, err := New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse(`
[env]
TOP = "top"

[tools]
packages = ["git"]

[provision]
command = "make setup"
dir = "tools"
env = { LOCAL = "yes" }
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitIdle(t, ag)
	spec := sys.hookSpec("provision")
	want := "sudo -n apt-get update && sudo -n apt-get install -y git && make setup"
	if got := spec.Command.String(); got != want {
		t.Fatalf("provision command = %q, want %q", got, want)
	}
	if spec.Dir != "tools" {
		t.Fatalf("provision dir = %q, want tools", spec.Dir)
	}
	if spec.Env["TOP"] != "top" || spec.Env["LOCAL"] != "yes" || spec.Env["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("provision env = %v", spec.Env)
	}
}
