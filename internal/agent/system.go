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
	"github.com/Siddhj2206/pluto/internal/state"
)

// Systemd is the real System: it drives the box's user systemd and git.
type Systemd struct {
	Home     string
	StateDir string
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
func (s Systemd) RunHook(ctx context.Context, name, worktree, command string, timeout time.Duration, logPath string) (int, error) {
	unit := HookUnit(name)
	scriptDir := filepath.Join(s.StateDir, "hooks")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return -1, fmt.Errorf("create hook dir: %w", err)
	}
	script := filepath.Join(scriptDir, name+".sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+command+"\n"), 0o755); err != nil {
		return -1, fmt.Errorf("write hook script: %w", err)
	}
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return -1, fmt.Errorf("create unit dir: %w", err)
	}
	body := hookUnitFile(name, worktree, script, s.hookPATH(), logPath, timeout)
	if err := os.WriteFile(filepath.Join(unitDir, unit), []byte(body), 0o644); err != nil {
		return -1, fmt.Errorf("write hook unit: %w", err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return -1, fmt.Errorf("daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Starting a oneshot blocks until the hook finishes.
	out, startErr := exec.CommandContext(runCtx, "systemctl", "--user", "start", unit).CombinedOutput()
	_ = exec.Command("systemctl", "--user", "stop", unit).Run()
	exit, loaded := s.mainStatus(unit)
	_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()

	if runCtx.Err() == context.DeadlineExceeded {
		return exit, fmt.Errorf("%w after %s", ErrTimeout, timeout)
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
// dies; only the streaming stops. TimeoutStartSec is infinite because a job
// is bounded by its command, not by systemd's 90-second start default.
func (s Systemd) RunJob(ctx context.Context, jobID, worktree string, argv []string, logPath string, emit func([]byte)) (int, error) {
	unit := JobUnit(jobID)
	scriptDir := filepath.Join(s.StateDir, "jobs")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return -1, fmt.Errorf("create job dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return -1, fmt.Errorf("create job log dir: %w", err)
	}
	script := filepath.Join(scriptDir, jobID+".sh")
	if err := os.WriteFile(script, []byte(jobScript(argv)), 0o755); err != nil {
		return -1, fmt.Errorf("write job script: %w", err)
	}
	unitDir := filepath.Join(s.Home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return -1, fmt.Errorf("create unit dir: %w", err)
	}
	unitPath := filepath.Join(unitDir, unit)
	if err := os.WriteFile(unitPath, []byte(jobUnitFile(jobID, worktree, script, s.hookPATH(), logPath)), 0o644); err != nil {
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
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
}

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

// jobScript is the job's command as an executable script: argv is executed
// directly, with no shell interpretation of its arguments.
func jobScript(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	return "#!/bin/sh\nexec " + strings.Join(quoted, " ") + "\n"
}

// shellQuote quotes s for /bin/sh when the script would split or interpret it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func jobUnitFile(jobID, worktree, script, path, logPath string) string {
	return fmt.Sprintf(`[Unit]
Description=pluto job %s

[Service]
Type=oneshot
WorkingDirectory=%s
Environment=PATH=%s
ExecStart=%s
StandardOutput=%s
StandardError=%s
KillMode=control-group
TimeoutStartSec=infinity
`, jobID, quoteUnitValue(worktree), quoteUnitValue(path), quoteUnitValue(script),
		quoteUnitValue("append:"+logPath), quoteUnitValue("append:"+logPath))
}

func hookUnitFile(name, worktree, script, path, logPath string, timeout time.Duration) string {
	return fmt.Sprintf(`[Unit]
Description=pluto %s hook

[Service]
Type=oneshot
WorkingDirectory=%s
Environment=PATH=%s
ExecStart=%s
StandardOutput=%s
StandardError=%s
KillMode=control-group
TimeoutStartSec=%d
`, name, quoteUnitValue(worktree), quoteUnitValue(path), quoteUnitValue(script),
		quoteUnitValue("append:"+logPath), quoteUnitValue("append:"+logPath), int(timeout.Seconds()))
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
// it. The command is written to a script so shell semantics are exact.
func (s Systemd) RestartServices(worktree string, services map[string]contract.Service) ([]state.ServiceStatus, error) {
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
		script := filepath.Join(scriptDir, name+".sh")
		body := "#!/bin/sh\n" + services[name].Command + "\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			return nil, fmt.Errorf("write service script %s: %w", name, err)
		}
		unit := filepath.Join(unitDir, ServiceUnit(name))
		if err := os.WriteFile(unit, []byte(serviceUnitFile(name, worktree, script, s.hookPATH())), 0o644); err != nil {
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
			Name:  name,
			State: activeState(ServiceUnit(name)),
			Port:  services[name].Port,
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

func serviceUnitFile(name, worktree, script, path string) string {
	return fmt.Sprintf(`[Unit]
Description=pluto service %s

[Service]
Type=simple
WorkingDirectory=%s
Environment=PATH=%s
ExecStart=%s
Restart=on-failure
RestartSec=2
`, name, quoteUnitValue(worktree), quoteUnitValue(path), quoteUnitValue(script))
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
