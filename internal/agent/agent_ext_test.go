package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Siddhj2206/pluto/internal/agent"
	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/state"
)

// recordingSystem is the fake agent.System for the external tests: it records
// the specs and service env it is handed, so agent behavior is asserted
// through its public surface (spec Testing Decisions).
type recordingSystem struct {
	mu           sync.Mutex
	hookSpecs    map[string]contract.Exec
	jobSpecs     []contract.Exec
	serviceEnv   map[string]string
	services     map[string]contract.Service
	sessionEnv   map[string]string
	sessionSpecs map[string]contract.Session
}

func newRecordingSystem() *recordingSystem {
	return &recordingSystem{hookSpecs: map[string]contract.Exec{}}
}

func (r *recordingSystem) RunHook(_ context.Context, name, worktree string, spec contract.Exec, logPath string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hookSpecs[name] = spec
	return 0, nil
}

func (r *recordingSystem) RunJob(_ context.Context, jobID, worktree string, spec contract.Exec, logPath string, emit func([]byte)) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobSpecs = append(r.jobSpecs, spec)
	return 0, nil
}

func (r *recordingSystem) StopJob(string) error                   { return nil }
func (r *recordingSystem) Sessions() (int, error)                 { return 0, nil }
func (r *recordingSystem) ServiceLog(string, int) (string, error) { return "", nil }
func (r *recordingSystem) CloneRepo(context.Context, string, string, string) error {
	return nil
}
func (r *recordingSystem) SetRemote(string, string) error { return nil }
func (r *recordingSystem) HasCheckout(string) bool        { return false }

func (r *recordingSystem) RestartServices(worktree string, services map[string]contract.Service, baseEnv map[string]string) ([]state.ServiceStatus, error) {
	r.mu.Lock()
	r.serviceEnv = baseEnv
	r.services = services
	r.mu.Unlock()
	return r.Statuses(services), nil
}

func (r *recordingSystem) Statuses(services map[string]contract.Service) []state.ServiceStatus {
	var out []state.ServiceStatus
	for name, svc := range services {
		out = append(out, state.ServiceStatus{Name: name, State: "active", Port: svc.Port, Description: svc.Description})
	}
	return out
}

func (r *recordingSystem) StartSessions(worktree string, sessions map[string]contract.Session, baseEnv map[string]string) ([]state.SessionStatus, error) {
	r.mu.Lock()
	r.sessionEnv = baseEnv
	r.sessionSpecs = sessions
	r.mu.Unlock()
	return r.SessionStatuses(sessions), nil
}

func (r *recordingSystem) SessionStatuses(sessions map[string]contract.Session) []state.SessionStatus {
	var out []state.SessionStatus
	for name, sess := range sessions {
		out = append(out, state.SessionStatus{Name: name, State: "running", Description: sess.Description})
	}
	return out
}

func (r *recordingSystem) SessionUsage(map[string]contract.Session) (state.SessionUsage, error) {
	return state.SessionUsage{}, nil
}

func (r *recordingSystem) hookSpec(name string) contract.Exec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hookSpecs[name]
}

func (r *recordingSystem) jobSpec() contract.Exec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobSpecs) == 0 {
		return contract.Exec{}
	}
	return r.jobSpecs[len(r.jobSpecs)-1]
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

// waitPersisted waits until a fresh agent reading the state directory sees
// the phases, which proves the background sequence's final status write has
// landed. Without it the test can return while the agent is still writing
// and the temp-dir cleanup races that write.
func waitPersisted(t *testing.T, root string, sys agent.System, cond func(state.Phases) bool) {
	t.Helper()
	waitFor(t, "phases persisted", func() bool {
		probe, err := agent.New(root, sys)
		return err == nil && cond(probe.Status())
	})
}

// A run carries the resolved spec to the system: dir, timeout, the job's env
// merged over the contract's, and the built-in PLUTO_WORKTREE.
func TestRunJobCarriesTheResolvedSpec(t *testing.T) {
	sys := newRecordingSystem()
	ag, err := agent.New(t.TempDir(), sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec := contract.Exec{
		Command: contract.ShellCommand("pnpm test"),
		Dir:     "web",
		Env:     map[string]string{"CI": "1"},
		Timeout: 30 * time.Minute,
	}
	job, err := ag.RunJob(state.NewID(), spec, "/home/dev/work/x", func([]byte) {})
	if err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	got := sys.jobSpec()
	if got.Dir != "web" || got.Timeout != 30*time.Minute || got.Env["CI"] != "1" || got.Env["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("spec = %+v, want the dir, timeout, env, and PLUTO_WORKTREE", got)
	}
	if job.Command != "pnpm test" {
		t.Fatalf("job command = %q, want the declared string", job.Command)
	}
}

// Apply resolves the declared provision, wake, and services into executable
// specs: command shape, dir, per-entity env merged over the top level, and
// the built-in PLUTO_WORKTREE on every phase.
func TestApplyResolvesCommandsDirEnvAndTimeouts(t *testing.T) {
	sys := newRecordingSystem()
	root := t.TempDir()
	ag, err := agent.New(root, sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse(`
[env]
TOP = "top"

[provision]
command = ["/bin/setup"]
dir = "sub"
env = { ONLY = "prov" }

[wake]
command = "repair"

[services.web]
description = "web UI"
command = ["serve", "--port", "3000"]
env = { SVC = "web" }
port = 3000
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "provision done", func() bool { return ag.Status().Provision.State == state.PhaseDone })
	waitFor(t, "wake done", func() bool { return ag.Status().Wake.State == state.PhaseDone })
	waitFor(t, "services", func() bool { return len(ag.Status().Services) == 1 })
	waitPersisted(t, root, sys, func(st state.Phases) bool {
		return st.Wake.State == state.PhaseDone && len(st.Services) == 1
	})

	prov := sys.hookSpec("provision")
	if strings.Join(prov.Command.Argv(), " ") != "/bin/setup" || prov.Dir != "sub" || prov.Timeout != contract.DefaultProvisionTimeout {
		t.Fatalf("provision spec = %+v", prov)
	}
	if prov.Env["TOP"] != "top" || prov.Env["ONLY"] != "prov" || prov.Env["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("provision env = %v", prov.Env)
	}
	wake := sys.hookSpec("wake")
	if wake.Command.String() != "repair" || wake.Env["TOP"] != "top" || wake.Env["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("wake spec = %+v", wake)
	}
	if len(wake.Env) != 2 {
		t.Fatalf("wake env = %v, want only the top level plus PLUTO_WORKTREE", wake.Env)
	}
	if sys.serviceEnv["TOP"] != "top" || sys.serviceEnv["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("services base env = %v", sys.serviceEnv)
	}
	if got := sys.services["web"]; got.Description != "web UI" || strings.Join(got.Command.Argv(), " ") != "serve --port 3000" || got.Env["SVC"] != "web" {
		t.Fatalf("service = %+v", got)
	}
	if got := ag.Status().Services[0]; got.Description != "web UI" {
		t.Fatalf("service status = %+v, want the description", got)
	}
}

// Apply resolves declared sessions into executable specs: command shape, dir,
// per-session env merged over the top level, and the built-in PLUTO_WORKTREE.
func TestApplyResolvesSessionSpecs(t *testing.T) {
	sys := newRecordingSystem()
	root := t.TempDir()
	ag, err := agent.New(root, sys)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ct, err := contract.Parse(`
[env]
TOP = "top"

[sessions.agent]
description = "the coding agent"
command = ["opencode", "--model", "x"]
dir = "sub"
env = { SESS = "yes" }
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ag.Apply(ct, "/home/dev/work/x")
	waitFor(t, "session reported", func() bool { return len(ag.Status().Sessions) == 1 })
	waitPersisted(t, root, sys, func(st state.Phases) bool { return len(st.Sessions) == 1 })

	got := sys.sessionSpecs["agent"]
	if strings.Join(got.Command.Argv(), " ") != "opencode --model x" || got.Dir != "sub" || got.Description != "the coding agent" {
		t.Fatalf("session spec = %+v", got)
	}
	if sys.sessionEnv["TOP"] != "top" || sys.sessionEnv["PLUTO_WORKTREE"] != "/home/dev/work/x" {
		t.Fatalf("session base env = %v", sys.sessionEnv)
	}
	if st := ag.Status().Sessions[0]; st.State != "running" || st.Description != "the coding agent" {
		t.Fatalf("session status = %+v", st)
	}
}
