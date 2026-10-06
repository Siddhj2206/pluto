package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Siddhj2206/pluto/internal/contract"
	"github.com/Siddhj2206/pluto/internal/shquote"
	"github.com/Siddhj2206/pluto/internal/state"
)

// Systemd is the real System: it drives the box's user systemd and git.
type Systemd struct {
	Home     string
	StateDir string
	// CgroupRoot is where the cgroup v2 control files live. Empty means
	// /sys/fs/cgroup; tests point it at a scratch tree.
	CgroupRoot string
}

// cgroupRoot is the mounted cgroup v2 hierarchy.
func (s Systemd) cgroupRoot() string {
	if s.CgroupRoot != "" {
		return s.CgroupRoot
	}
	return "/sys/fs/cgroup"
}

// NewSystemd builds the real System for the invoking user.
func NewSystemd(stateDir string) Systemd {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/dev"
	}
	return Systemd{Home: home, StateDir: stateDir}
}

// hookPATH is what hooks and services see: the box user's tool directories
// first (bun, uv, pipx, …), then the system paths.
func (s Systemd) hookPATH() string {
	return strings.Join([]string{
		filepath.Join(s.Home, ".bun", "bin"),
		filepath.Join(s.Home, ".local", "bin"),
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
	}, ":")
}

// RunHook runs one hook as a fixed per-phase user unit. systemd-run is not
// used: it needs the session D-Bus, which a lingering user manager does not
// run. The unit's cgroup reaps leftover processes when it stops, and
// TimeoutStartSec plus the context bound the run.
func (s Systemd) RunHook(ctx context.Context, name, worktree string, spec contract.Exec, logPath string) (int, error) {
	unit := HookUnit(name)
	scriptDir := filepath.Join(s.StateDir, "hooks")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return -1, fmt.Errorf("create hook dir: %w", err)
	}
	script := filepath.Join(scriptDir, name+".sh")
	if err := os.WriteFile(script, []byte(commandScript(spec.Command)), 0o755); err != nil {
		return -1, fmt.Errorf("write hook script: %w", err)
	}
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return -1, fmt.Errorf("create unit dir: %w", err)
	}
	body := hookUnitFile(name, worktree, script, s.hookPATH(), logPath, spec)
	if err := os.WriteFile(filepath.Join(unitDir, unit), []byte(body), 0o644); err != nil {
		return -1, fmt.Errorf("write hook unit: %w", err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return -1, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()

	runCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	// Starting a oneshot blocks until the hook finishes.
	out, startErr := exec.CommandContext(runCtx, "systemctl", "--user", "start", unit).CombinedOutput()
	_ = exec.Command("systemctl", "--user", "stop", unit).Run()
	exit, loaded := s.mainStatus(unit)
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()

	if spec.Timeout > 0 && runCtx.Err() == context.DeadlineExceeded {
		return exit, fmt.Errorf("%w after %s", ErrTimeout, spec.Timeout)
	}
	if startErr != nil {
		if loaded {
			return exit, nil // the hook ran and failed; its code is the result
		}
		return -1, fmt.Errorf("start %s: %w (%s)", unit, startErr, strings.TrimSpace(string(out)))
	}
	if !loaded {
		return -1, fmt.Errorf("hook %s did not run", name)
	}
	return exit, nil
}

// HookUnit is the unit name for a provision or wake hook.
func HookUnit(name string) string { return "pluto-hook-" + name + ".service" }

// JobUnit is the unit name for a job.
func JobUnit(jobID string) string { return "pluto-job-" + jobID + ".service" }

// RunJob runs one bounded command as a fixed user unit, tailing its output
// while the unit runs. The unit keeps running if the agent's connection
// dies; only the streaming stops. TimeoutStartSec enforces the job's timebox
// (infinity when unlimited), not systemd's 90-second start default.
func (s Systemd) RunJob(ctx context.Context, jobID, worktree string, spec contract.Exec, logPath string, emit func([]byte)) (int, error) {
	unit := JobUnit(jobID)
	scriptDir := filepath.Join(s.StateDir, "jobs")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return -1, fmt.Errorf("create job dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, fmt.Errorf("create job log dir: %w", err)
	}
	script := filepath.Join(scriptDir, jobID+".sh")
	if err := os.WriteFile(script, []byte(commandScript(spec.Command)), 0o755); err != nil {
		return -1, fmt.Errorf("write job script: %w", err)
	}
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return -1, fmt.Errorf("create unit dir: %w", err)
	}
	unitPath := filepath.Join(unitDir, unit)
	if err := os.WriteFile(unitPath, []byte(jobUnitFile(jobID, worktree, script, s.hookPATH(), logPath, spec)), 0o644); err != nil {
		return -1, fmt.Errorf("write job unit: %w", err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return -1, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
	defer func() {
		_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
		_ = os.Remove(unitPath)
		_ = os.Remove(script)
	}()

	// Starting a oneshot unit blocks until the job exits.
	done := make(chan error, 1)
	go func() { done <- exec.Command("systemctl", "--user", "start", unit).Run() }()

	// TimeoutStartSec is what stops an overrunning job; the watch here is a
	// backstop so the collector cannot hang if systemd does not.
	runCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout+jobTimeoutGrace)
		defer cancel()
	}

	offset := int64(0)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			drainFile(logPath, &offset, emit)
			exit, loaded := s.mainStatus(unit)
			if !loaded {
				if err == nil {
					err = fmt.Errorf("job unit %s did not run", unit)
				}
				return -1, err
			}
			return exit, nil
		case <-ticker.C:
			drainFile(logPath, &offset, emit)
		case <-runCtx.Done():
			if spec.Timeout > 0 && runCtx.Err() == context.DeadlineExceeded {
				return -1, fmt.Errorf("%w after %s", ErrTimeout, spec.Timeout)
			}
			return -1, runCtx.Err()
		}
	}
}

// jobTimeoutGrace is how long the collector waits past a job's timebox before
// giving up on systemd stopping it.
const jobTimeoutGrace = 30 * time.Second

// StopJob stops a job's unit and clears its failed state. Stopping a unit
// that is not loaded is an error the caller can ignore.
func (s Systemd) StopJob(jobID string) error {
	unit := JobUnit(jobID)
	out, err := exec.Command("systemctl", "--user", "stop", unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("stop %s: %w (%s)", unit, err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
	return nil
}

// Sessions counts the box's live ssh sessions. Each connection gets an
// "sshd: <user>@..." process; the listener and the privilege-separation
// parent do not match, and pgrep never matches itself. pgrep exits 1 with a
// zero count when nothing matches; any other failure means unknown.
func (s Systemd) Sessions() (int, error) {
	out, err := exec.Command("pgrep", "-c", "-f", `sshd:.*@`).Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return 0, fmt.Errorf("count ssh sessions: %w", err)
		}
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if convErr != nil {
		return 0, fmt.Errorf("count ssh sessions: %w", convErr)
	}
	return n, nil
}

// drainFile emits what has been appended to path since offset and advances
// offset. A missing file just means no output yet.
func drainFile(path string, offset *int64, emit func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(*offset, io.SeekStart); err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			*offset += int64(n)
			emit(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// commandScript renders a declared command as an executable script: a shell
// string runs via '/bin/sh -c', an argv command is exec'd directly with no
// shell interpretation (ADR 0007).
func commandScript(cmd contract.Command) string {
	argv := cmd.Argv()
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shquote.Quote(arg)
	}
	return "#!/bin/sh\nexec " + strings.Join(quoted, " ") + "\n"
}

func jobUnitFile(jobID, worktree, script, path, logPath string, spec contract.Exec) string {
	return oneshotUnit("pluto job "+jobID, worktree, script, path, logPath, spec)
}

func hookUnitFile(name, worktree, script, path, logPath string, spec contract.Exec) string {
	return oneshotUnit("pluto "+name+" hook", worktree, script, path, logPath, spec)
}

// oneshotUnit renders a oneshot unit that runs one command to completion.
// WorkingDirectory resolves the declared dir against the in-box worktree;
// TimeoutStartSec enforces the timebox (infinity when unlimited).
func oneshotUnit(description, worktree, script, path, logPath string, spec contract.Exec) string {
	lines := []string{
		"[Unit]",
		"Description=" + description,
		"",
		"[Service]",
		"Type=oneshot",
		"WorkingDirectory=" + quoteUnitValue(contract.ResolveDir(worktree, spec.Dir)),
		"Environment=PATH=" + quoteUnitValue(path),
	}
	lines = append(lines, environmentLines(spec.Env)...)
	lines = append(lines,
		"ExecStart="+quoteUnitValue(script),
		"StandardOutput="+quoteUnitValue("append:"+logPath),
		"StandardError="+quoteUnitValue("append:"+logPath),
		"KillMode=control-group",
		"TimeoutStartSec="+timeoutSetting(spec.Timeout),
		"",
	)
	return strings.Join(lines, "\n")
}

// environmentLines renders an environment as systemd Environment= lines.
func environmentLines(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, "Environment="+quoteUnitValue(name+"="+env[name]))
	}
	return lines
}

// timeoutSetting renders a timebox for systemd: whole seconds, or infinity.
func timeoutSetting(d time.Duration) string {
	if d <= 0 {
		return "infinity"
	}
	secs := (d + time.Second - 1) / time.Second
	return strconv.FormatInt(int64(secs), 10)
}

// mainStatus reads a unit's exit code, reporting whether it ran at all. The
// property order is systemd's, so it is parsed by value, not position.
func (s Systemd) mainStatus(unit string) (int, bool) {
	out, err := exec.Command("systemctl", "--user", "show",
		"--property=LoadState", "--property=ExecMainStatus", "--value", unit).Output()
	if err != nil {
		return 0, false
	}
	loadState := ""
	code := -1
	for _, field := range strings.Fields(string(out)) {
		switch field {
		case "loaded", "not-found", "masked", "bad-setting", "error":
			loadState = field
		default:
			if n, err := strconv.Atoi(field); err == nil {
				code = n
			}
		}
	}
	if loadState != "loaded" || code < 0 {
		return 0, false
	}
	return code, true
}

// RestartServices renders each declared service as a user unit and restarts
// it. The command is written to a script so shell semantics are exact, and
// each service's env is merged over the contract's top level.
func (s Systemd) RestartServices(worktree string, services map[string]contract.Service, baseEnv map[string]string) ([]state.ServiceStatus, error) {
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	scriptDir := filepath.Join(s.StateDir, "services")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return nil, fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return nil, fmt.Errorf("create service script dir: %w", err)
	}

	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		svc := services[name]
		script := filepath.Join(scriptDir, name+".sh")
		if err := os.WriteFile(script, []byte(commandScript(svc.Command)), 0o755); err != nil {
			return nil, fmt.Errorf("write service script %s: %w", name, err)
		}
		unit := filepath.Join(unitDir, ServiceUnit(name))
		env := contract.MergeEnv(baseEnv, svc.Env)
		dir := contract.ResolveDir(worktree, svc.Dir)
		if err := os.WriteFile(unit, []byte(serviceUnitFile(name, dir, script, s.hookPATH(), env)), 0o644); err != nil {
			return nil, fmt.Errorf("write unit %s: %w", name, err)
		}
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// Restart every service even if one fails; each unit's own state reports
	// the truth, and one broken service must not block the others.
	var errs []error
	for _, name := range names {
		if out, err := exec.Command("systemctl", "--user", "restart", ServiceUnit(name)).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("restart %s: %w (%s)", name, err, strings.TrimSpace(string(out))))
		}
	}
	return s.Statuses(services), errors.Join(errs...)
}

// Statuses observes the declared services.
func (s Systemd) Statuses(services map[string]contract.Service) []state.ServiceStatus {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]state.ServiceStatus, 0, len(names))
	for _, name := range names {
		out = append(out, state.ServiceStatus{
			Name:        name,
			State:       activeState(ServiceUnit(name)),
			Port:        services[name].Port,
			Description: services[name].Description,
		})
	}
	return out
}

// ServiceLog returns the recent journal for one service.
func (s Systemd) ServiceLog(name string, lines int) (string, error) {
	out, err := exec.Command("journalctl", "--user", "-u", ServiceUnit(name),
		"-n", strconv.Itoa(lines), "--no-pager", "-o", "cat").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("journalctl %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// StartSessions renders each declared session as a user unit and restarts it.
// The command runs under a detached tmux session, so the tmux server owns the
// PTY and a client can attach and detach without ending it. Each session's env
// is merged over the contract's top level, and its dir resolves against the
// worktree, exactly like a service.
func (s Systemd) StartSessions(worktree string, sessions map[string]contract.Session, baseEnv map[string]string) ([]state.SessionStatus, error) {
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	scriptDir := filepath.Join(s.StateDir, "sessions")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return nil, fmt.Errorf("create unit dir: %w", err)
	}
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session script dir: %w", err)
	}

	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sess := sessions[name]
		script := filepath.Join(scriptDir, name+".sh")
		if err := os.WriteFile(script, []byte(commandScript(sess.Command)), 0o755); err != nil {
			return nil, fmt.Errorf("write session script %s: %w", name, err)
		}
		unit := filepath.Join(unitDir, SessionUnit(name))
		env := contract.MergeEnv(baseEnv, sess.Env)
		dir := contract.ResolveDir(worktree, sess.Dir)
		if err := os.WriteFile(unit, []byte(sessionUnitFile(name, dir, script, s.hookPATH(), env)), 0o644); err != nil {
			return nil, fmt.Errorf("write session unit %s: %w", name, err)
		}
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// Restart every session even if one fails; each unit's own state reports
	// the truth, and one broken session must not block the others.
	var errs []error
	for _, name := range names {
		if out, err := exec.Command("systemctl", "--user", "restart", SessionUnit(name)).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("restart session %s: %w (%s)", name, err, strings.TrimSpace(string(out))))
		}
	}
	return s.SessionStatuses(sessions), errors.Join(errs...)
}

// SessionStatuses observes the declared sessions, including whether a client
// is attached to each one's tmux session.
func (s Systemd) SessionStatuses(sessions map[string]contract.Session) []state.SessionStatus {
	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]state.SessionStatus, 0, len(names))
	for _, name := range names {
		out = append(out, state.SessionStatus{
			Name:        name,
			State:       sessionState(SessionUnit(name)),
			Attached:    sessionAttached(name),
			Description: sessions[name].Description,
		})
	}
	return out
}

// SessionUsage reads the cumulative cgroup v2 CPU and IO counters for every
// declared session's unit and sums them. A session whose cgroup cannot be
// read fails the whole reading: a partial sum could look idle while another
// session is working. The counters are monotonic, so callers compare
// successive readings rather than trusting a single value.
func (s Systemd) SessionUsage(sessions map[string]contract.Session) (state.SessionUsage, error) {
	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	var total state.SessionUsage
	var errs []error
	for _, name := range names {
		usage, err := s.sessionCgroupUsage(SessionUnit(name))
		if err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", name, err))
			continue
		}
		total.CPUUsec += usage.CPUUsec
		total.IOBytes += usage.IOBytes
	}
	return total, errors.Join(errs...)
}

// sessionCgroupUsage resolves a unit's cgroup v2 path and reads its counters.
func (s Systemd) sessionCgroupUsage(unit string) (state.SessionUsage, error) {
	out, err := exec.Command("systemctl", "--user", "show", unit, "-p", "ControlGroup", "--value").Output()
	if err != nil {
		return state.SessionUsage{}, fmt.Errorf("read cgroup of %s: %w", unit, err)
	}
	cg := strings.TrimSpace(string(out))
	if cg == "" {
		return state.SessionUsage{}, fmt.Errorf("unit %s has no cgroup", unit)
	}
	return cgroupUsage(filepath.Join(s.cgroupRoot(), cg))
}

// cgroupUsage reads a cgroup v2 directory's cumulative CPU and IO counters.
// The delegated user cgroup may have no io.stat, so a missing IO counter is
// read as zero while the CPU counter still comes back; any other IO read or
// parse failure is a real error, and a missing cpu.stat stays fatal.
func cgroupUsage(dir string) (state.SessionUsage, error) {
	cpu, err := cgroupCPUUsec(filepath.Join(dir, "cpu.stat"))
	if err != nil {
		return state.SessionUsage{}, err
	}
	io, err := cgroupIOBytes(filepath.Join(dir, "io.stat"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return state.SessionUsage{}, err
	}
	return state.SessionUsage{CPUUsec: cpu, IOBytes: io}, nil
}

// cgroupCPUUsec returns usage_usec from a cgroup v2 cpu.stat file.
func cgroupCPUUsec(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read cpu.stat: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "usage_usec" {
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse usage_usec: %w", err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s: no usage_usec", path)
}

// cgroupIOBytes sums rbytes and wbytes across every device in a cgroup v2
// io.stat file.
func cgroupIOBytes(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read io.stat: %w", err)
	}
	var total int64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, field := range fields[1:] {
			value, ok := strings.CutPrefix(field, "rbytes=")
			if !ok {
				value, ok = strings.CutPrefix(field, "wbytes=")
			}
			if !ok {
				continue
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse io bytes: %w", err)
			}
			total += n
		}
	}
	return total, nil
}

// SessionUnit is the unit name for a declared session.
func SessionUnit(name string) string { return "pluto-session-" + name + ".service" }

// sessionUnitFile renders a session's user unit. The unit runs tmux
// detached with the declared command as its only window; tmux owns the PTY,
// so the process outlives any attach. Type=forking supervises the tmux server
// the client leaves behind, and the unit's env carries the merged environment.
func sessionUnitFile(name, dir, script, path string, env map[string]string) string {
	lines := []string{
		"[Unit]",
		"Description=pluto session " + name,
		"",
		"[Service]",
		"Type=forking",
		"WorkingDirectory=" + quoteUnitValue(dir),
		"Environment=PATH=" + quoteUnitValue(path),
	}
	lines = append(lines, environmentLines(env)...)
	lines = append(lines,
		"ExecStart="+tmuxSessionCommand(name, dir, script),
		"Restart=on-failure",
		"RestartSec=2",
		"KillMode=control-group",
		"",
	)
	return strings.Join(lines, "\n")
}

// tmuxSessionCommand renders the ExecStart that starts a detached tmux session
// running script in dir. Each argument is quoted for systemd so a worktree or
// script path with spaces survives.
func tmuxSessionCommand(name, dir, script string) string {
	args := []string{"/usr/bin/tmux", "new-session", "-d", "-s", name, "-c", dir, script}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteUnitValue(arg)
	}
	return strings.Join(quoted, " ")
}

// sessionState reports a session's observed state: "running" while its unit is
// active, "stopped" when it is inactive, otherwise the unit's own state.
func sessionState(unit string) string {
	switch state := activeState(unit); state {
	case "active":
		return "running"
	case "inactive", "":
		return "stopped"
	default:
		return state
	}
}

// sessionAttached reports whether a client is attached to the named tmux
// session. A missing session, or a tmux that cannot answer, is not attached.
func sessionAttached(name string) bool {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", name, "#{session_attached}").Output()
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return err == nil && n > 0
}

// HasCheckout reports whether the worktree holds a usable git checkout: a
// repo whose HEAD resolves. An interrupted first clone leaves .git behind
// without a commit, and adopting it would mark a broken tree synced.
func (s Systemd) HasCheckout(worktree string) bool {
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		return false
	}
	out, err := exec.Command("git", "-C", worktree, "rev-parse", "--verify", "HEAD").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// MirrorRemotes recreates the host worktree's remotes in the box: each name,
// fetch URL, any distinct push URL(s), and the default fetch refspec. It is
// idempotent and additive — a remote already correct is left alone, a changed
// URL is updated, and a remote the box carries that the host list does not
// mention is never removed, so adoption cannot clobber the box's own git. No
// remote-tracking refs are fetched; the box fetches on demand (ADR 0008).
func (s Systemd) MirrorRemotes(worktree string, remotes []state.Remote) error {
	for _, r := range remotes {
		if err := mirrorOneRemote(worktree, r); err != nil {
			return err
		}
	}
	return nil
}

func mirrorOneRemote(worktree string, r state.Remote) error {
	current, err := gitRemoteURL(worktree, r.Name)
	switch {
	case err != nil:
		if out, addErr := exec.Command("git", "-C", worktree, "remote", "add", r.Name, r.Fetch).CombinedOutput(); addErr != nil {
			return fmt.Errorf("git remote add %s: %w (%s)", r.Name, addErr, strings.TrimSpace(string(out)))
		}
	case current != r.Fetch:
		if out, setErr := exec.Command("git", "-C", worktree, "remote", "set-url", r.Name, r.Fetch).CombinedOutput(); setErr != nil {
			return fmt.Errorf("git remote set-url %s: %w (%s)", r.Name, setErr, strings.TrimSpace(string(out)))
		}
	}
	if err := setFetchRefspec(worktree, r.Name); err != nil {
		return err
	}
	return setPushURLs(worktree, r.Name, r.Push)
}

// setFetchRefspec ensures remote.<name>.fetch is the default all-branches
// refspec, without fetching anything now.
func setFetchRefspec(worktree, name string) error {
	refspec := "+refs/heads/*:refs/remotes/" + name + "/*"
	values, _ := gitConfigAll(worktree, "remote."+name+".fetch")
	if len(values) == 1 && values[0] == refspec {
		return nil
	}
	_ = exec.Command("git", "-C", worktree, "config", "--unset-all", "remote."+name+".fetch").Run()
	if out, err := exec.Command("git", "-C", worktree, "config", "--add", "remote."+name+".fetch", refspec).CombinedOutput(); err != nil {
		return fmt.Errorf("git config remote.%s.fetch: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setPushURLs configures remote.<name>.pushurl for push URLs distinct from the
// fetch URL. No distinct push URLs (the common case) means no pushurl entry,
// so a push uses the fetch URL.
func setPushURLs(worktree, name string, want []string) error {
	current, _ := gitConfigAll(worktree, "remote."+name+".pushurl")
	if strings.Join(current, "\n") == strings.Join(want, "\n") {
		return nil
	}
	_ = exec.Command("git", "-C", worktree, "config", "--unset-all", "remote."+name+".pushurl").Run()
	for _, url := range want {
		if out, err := exec.Command("git", "-C", worktree, "remote", "set-url", "--add", "--push", name, url).CombinedOutput(); err != nil {
			return fmt.Errorf("git remote set-url --add --push %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// TrackBranch points the checked-out branch at the tracked remote — origin, or
// the sole remote when there is no origin — and sets push.default=current so a
// bare `git push` works with a single remote, for published and unpublished
// branches alike. When several remotes exist and none is origin the branch is
// left untracked (pluto warns). A worktree with no remotes is a no-op: the box
// is local-only.
func (s Systemd) TrackBranch(worktree, branch string, remotes []state.Remote) error {
	if len(remotes) == 0 {
		return nil
	}
	if err := setGitConfig(worktree, "push.default", "current"); err != nil {
		return err
	}
	if branch == "" || branch == "(detached)" {
		return nil
	}
	tracked, ok := state.TrackedRemote(remotes)
	if !ok {
		return nil
	}
	if err := setGitConfig(worktree, "branch."+branch+".remote", tracked.Name); err != nil {
		return err
	}
	return setGitConfig(worktree, "branch."+branch+".merge", "refs/heads/"+branch)
}

func setGitConfig(worktree, key, value string) error {
	if out, err := exec.Command("git", "-C", worktree, "config", key, value).CombinedOutput(); err != nil {
		return fmt.Errorf("git config %s: %w (%s)", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitRemoteURL(worktree, name string) (string, error) {
	out, err := exec.Command("git", "-C", worktree, "remote", "get-url", name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitConfigAll reads every value git holds for a key. A missing key is not an
// error: it returns no values.
func gitConfigAll(worktree, key string) ([]string, error) {
	out, err := exec.Command("git", "-C", worktree, "config", "--get-all", key).Output()
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return nil, nil
	}
	return strings.Split(text, "\n"), nil
}

// CloneRepo clones a bundle into the box worktree and checks out branch. The
// bundle's origin is removed (it is a temporary file) and a default commit
// identity is set so agents and hooks can commit inside the box.
func (s Systemd) CloneRepo(ctx context.Context, bundle, worktree, branch string) error {
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return fmt.Errorf("create worktree parent: %w", err)
	}
	if out, err := exec.CommandContext(ctx, "git", "clone", "-q", bundle, worktree).CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if branch != "" && branch != "(detached)" {
		if out, err := exec.CommandContext(ctx, "git", "-C", worktree, "checkout", "-q", branch).CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout %s: %w (%s)", branch, err, strings.TrimSpace(string(out)))
		}
	}
	_ = exec.CommandContext(ctx, "git", "-C", worktree, "remote", "remove", "origin").Run()
	for _, kv := range [][2]string{{"user.name", "pluto"}, {"user.email", "pluto@localhost"}} {
		_ = exec.CommandContext(ctx, "git", "-C", worktree, "config", kv[0], kv[1]).Run()
	}
	return nil
}

// ServiceUnit is the unit name for a declared service.
func ServiceUnit(name string) string { return "pluto-service-" + name + ".service" }

func serviceUnitFile(name, dir, script, path string, env map[string]string) string {
	lines := []string{
		"[Unit]",
		"Description=pluto service " + name,
		"",
		"[Service]",
		"Type=simple",
		"WorkingDirectory=" + quoteUnitValue(dir),
		"Environment=PATH=" + quoteUnitValue(path),
	}
	lines = append(lines, environmentLines(env)...)
	lines = append(lines,
		"ExecStart="+quoteUnitValue(script),
		"Restart=on-failure",
		"RestartSec=2",
		"",
	)
	return strings.Join(lines, "\n")
}

func activeState(unit string) string {
	out, _ := exec.Command("systemctl", "--user", "is-active", unit).Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		return "inactive"
	}
	return state
}

// quoteUnitValue quotes a systemd value when it contains characters systemd
// would split.
func quoteUnitValue(value string) string {
	if strings.ContainsAny(value, " \t\"'\\") {
		return strconv.Quote(value)
	}
	return value
}
