// Package contract parses a repository's .pluto.toml: the declaration of
// provision, wake, services, jobs, and schedules that a box applies
// (ADR 0007).
//
// The daemon parses the contract on the host, where the worktree lives, and
// sends it to the guest agent. The box itself never needs to parse TOML.
package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

//go:generate go run ../../cmd/pluto-schema ../../pluto.schema.json

// FileName is the contract's path inside a worktree.
const FileName = ".pluto.toml"

// Defaults from ADR 0007.
const (
	DefaultProvisionTimeout = 20 * time.Minute
	DefaultWakeTimeout      = 30 * time.Second
	// DefaultAutoPause is how long a box may sit idle (no client attached,
	// no job running) before the daemon pauses it (ADR 0002).
	DefaultAutoPause = time.Hour
)

// nameRule is the shared rule for service and job names.
var nameRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ErrNoSuchJob reports a `pluto run` or schedule reference to a job no
// [jobs.<name>] declares.
var ErrNoSuchJob = errors.New("no such job")

// Contract is a parsed .pluto.toml.
type Contract struct {
	Box       Box                `toml:"box"`
	Env       map[string]string  `toml:"env"`
	Provision *Phase             `toml:"provision"`
	Wake      *Phase             `toml:"wake"`
	Services  map[string]Service `toml:"services"`
	Jobs      map[string]Job     `toml:"jobs"`
	Schedules []Schedule         `toml:"schedule"`
}

// Box is the box-level section.
type Box struct {
	Image     string    `toml:"image"`
	Resources Resources `toml:"resources"`
	// AutoPause is the idle window: a duration like "30m", or "off" to keep
	// the box running until it is paused by hand. Empty means the default.
	AutoPause string `toml:"auto_pause"`
}

// Resources declares the machine's size; the runner reads it later.
type Resources struct {
	CPUs   int    `toml:"cpus"`
	Memory string `toml:"memory"`
	Disk   string `toml:"disk"`
}

// Phase is a provision or wake hook: a declared command and its timebox.
type Phase struct {
	Command Command           `toml:"command" schema:"required"`
	Dir     string            `toml:"dir"`
	Env     map[string]string `toml:"env"`
	Timeout string            `toml:"timeout"`
}

// Service is a long-lived declared process.
type Service struct {
	Description string            `toml:"description"`
	Command     Command           `toml:"command" schema:"required"`
	Dir         string            `toml:"dir"`
	Env         map[string]string `toml:"env"`
	Port        int               `toml:"port"`
}

// Job is a named, bounded command: the unit `pluto run <name>` and schedules
// invoke. A timeout is unlimited by default (ADR 0007).
type Job struct {
	Description string            `toml:"description"`
	Command     Command           `toml:"command" schema:"required"`
	Dir         string            `toml:"dir"`
	Env         map[string]string `toml:"env"`
	Timeout     string            `toml:"timeout"`
}

// Schedule is a recurring wake. It names a declared job or, without one, is a
// warm-up that only ensures the box is running.
type Schedule struct {
	Name string `toml:"name" schema:"required"`
	Cron string `toml:"cron" schema:"required"`
	Job  string `toml:"job"`
}

// Command is a declared command. A string runs via '/bin/sh -c'; an array is
// exec'd directly, with no shell (ADR 0007). It is opaque: build one with
// ShellCommand or ArgvCommand and run it with Argv.
type Command struct {
	shell string
	argv  []string
}

// ShellCommand declares a command that runs via '/bin/sh -c'.
func ShellCommand(s string) Command { return Command{shell: s} }

// ArgvCommand declares a command that is exec'd directly, with no shell.
func ArgvCommand(argv []string) Command { return Command{argv: append([]string(nil), argv...)} }

// IsZero reports whether no command was declared.
func (c Command) IsZero() bool { return c.shell == "" && len(c.argv) == 0 }

// String renders the command for display and job records.
func (c Command) String() string {
	if c.shell != "" {
		return c.shell
	}
	return strings.Join(c.argv, " ")
}

// Argv returns the command in exec form: a shell string becomes
// ['/bin/sh', '-c', s]; an argv command runs as declared.
func (c Command) Argv() []string {
	if c.shell != "" {
		return []string{"/bin/sh", "-c", c.shell}
	}
	return append([]string(nil), c.argv...)
}

// UnmarshalTOML accepts a string (shell form) or an array (argv form).
func (c *Command) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		if t == "" {
			return errors.New("command must not be empty")
		}
		c.shell, c.argv = t, nil
	case []any:
		argv := make([]string, len(t))
		for i, item := range t {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("command array items must be strings, item %d is not", i+1)
			}
			argv[i] = s
		}
		if len(argv) == 0 {
			return errors.New("command must not be empty")
		}
		c.shell, c.argv = "", argv
	default:
		return errors.New("command must be a string or an array of strings")
	}
	return nil
}

// MarshalJSON sends the command in its declared shape.
func (c Command) MarshalJSON() ([]byte, error) {
	if c.shell != "" {
		return json.Marshal(c.shell)
	}
	return json.Marshal(c.argv)
}

// UnmarshalJSON accepts either declared shape from the daemon.
func (c *Command) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.shell, c.argv = s, nil
		return nil
	}
	var argv []string
	if err := json.Unmarshal(data, &argv); err != nil {
		return errors.New("command must be a string or an array of strings")
	}
	c.shell, c.argv = "", argv
	return nil
}

// Exec is a command resolved for execution: the command, the working
// directory (relative to the in-box worktree unless absolute), the
// environment already merged with the contract's top level, and the timebox
// (zero is unlimited). The guest agent adds PLUTO_WORKTREE and resolves Dir
// against the worktree at run time.
type Exec struct {
	Command Command           `json:"command"`
	Dir     string            `json:"dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout time.Duration     `json:"timeout,omitempty"`
}

// Load reads the contract from a worktree. A missing file is not an error:
// it yields an empty contract, a box with no declared phases.
func Load(worktree string) (*Contract, error) {
	path := filepath.Join(worktree, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Contract{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c, err := Parse(string(data))
	if err != nil {
		return nil, contractError(path, err)
	}
	return c, nil
}

// contractError renders a contract failure with its file, and with the line
// when the TOML parser reports one: the fix is an edit at that spot (ADR 0009).
func contractError(path string, err error) error {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) && parseErr.Position.Line > 0 {
		return fmt.Errorf("%s:%d: %s", path, parseErr.Position.Line, parseErr.Message)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// Parse parses and validates contract TOML. Unknown keys are errors so a
// typo in a contract is caught at load time, not at boot.
func Parse(data string) (*Contract, error) {
	var c Contract
	md, err := toml.Decode(data, &c)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown keys: %v", keys)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Contract) validate() error {
	if err := validateEnv("env", c.Env); err != nil {
		return err
	}
	if c.Box.AutoPause != "" && c.Box.AutoPause != "off" {
		if _, err := parseTimeout(c.Box.AutoPause); err != nil {
			return fmt.Errorf("box.auto_pause: %w", err)
		}
	}
	if c.Provision != nil {
		if c.Provision.Command.IsZero() {
			return errors.New("provision: command is required")
		}
		if _, err := parseTimeout(c.Provision.Timeout); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
		if err := validateEnv("provision.env", c.Provision.Env); err != nil {
			return err
		}
	}
	if c.Wake != nil {
		if c.Wake.Command.IsZero() {
			return errors.New("wake: command is required")
		}
		if _, err := parseTimeout(c.Wake.Timeout); err != nil {
			return fmt.Errorf("wake: %w", err)
		}
		if err := validateEnv("wake.env", c.Wake.Env); err != nil {
			return err
		}
	}
	for name, svc := range c.Services {
		if !nameRule.MatchString(name) {
			return fmt.Errorf("services.%s: name must be letters, digits, '-' or '_'", name)
		}
		if svc.Command.IsZero() {
			return fmt.Errorf("services.%s: command is required", name)
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return fmt.Errorf("services.%s: port %d is out of range", name, svc.Port)
		}
		if err := validateEnv("services."+name+".env", svc.Env); err != nil {
			return err
		}
	}
	for name, job := range c.Jobs {
		if !nameRule.MatchString(name) {
			return fmt.Errorf("jobs.%s: name must be letters, digits, '-' or '_'", name)
		}
		if job.Command.IsZero() {
			return fmt.Errorf("jobs.%s: command is required", name)
		}
		if _, err := parseTimeout(job.Timeout); err != nil {
			return fmt.Errorf("jobs.%s: %w", name, err)
		}
		if err := validateEnv("jobs."+name+".env", job.Env); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(c.Schedules))
	for _, s := range c.Schedules {
		if s.Name == "" {
			return errors.New("schedule: name is required")
		}
		if seen[s.Name] {
			return fmt.Errorf("schedule %q: duplicate name", s.Name)
		}
		seen[s.Name] = true
		if s.Cron == "" {
			return fmt.Errorf("schedule %q: cron is required", s.Name)
		}
		if _, err := ParseCron(s.Cron); err != nil {
			return fmt.Errorf("schedule %q: invalid cron %q: %w", s.Name, s.Cron, err)
		}
		if s.Job != "" {
			if _, ok := c.Jobs[s.Job]; !ok {
				return fmt.Errorf("schedule %q: job %q is not declared in [jobs.*]", s.Name, s.Job)
			}
		}
	}
	return nil
}

// validateEnv enforces the [env] rules: flat string values, no interpolation
// concerns, and a reserved PLUTO_ prefix (ADR 0007).
func validateEnv(section string, env map[string]string) error {
	for key, value := range env {
		switch {
		case key == "":
			return fmt.Errorf("%s: env keys must not be empty", section)
		case strings.HasPrefix(key, "PLUTO_"):
			return fmt.Errorf("%s.%s: the PLUTO_ prefix is reserved", section, key)
		case strings.ContainsAny(key, "=\x00"):
			return fmt.Errorf("%s: invalid env key %q", section, key)
		case strings.ContainsAny(value, "\x00\n\r"):
			return fmt.Errorf("%s.%s: env values must be single-line", section, key)
		}
	}
	return nil
}

// Empty reports whether the contract declares nothing to run: no phases,
// services, jobs, or schedules. Top-level env alone does not count.
func (c *Contract) Empty() bool {
	return c.Provision == nil && c.Wake == nil &&
		len(c.Services) == 0 && len(c.Jobs) == 0 && len(c.Schedules) == 0
}

// ProvisionTimeout is the effective provision timebox.
func (c *Contract) ProvisionTimeout() time.Duration {
	if c.Provision != nil && c.Provision.Timeout != "" {
		if d, err := parseTimeout(c.Provision.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return DefaultProvisionTimeout
}

// WakeTimeout is the effective wake timebox.
func (c *Contract) WakeTimeout() time.Duration {
	if c.Wake != nil && c.Wake.Timeout != "" {
		if d, err := parseTimeout(c.Wake.Timeout); err == nil && d > 0 {
			return d
		}
	}
	return DefaultWakeTimeout
}

// AutoPauseWindow is the effective idle window before the daemon pauses the
// box; zero means auto-pause is off. Parse has already validated the value.
func (c *Contract) AutoPauseWindow() time.Duration {
	return ResolveAutoPause(c.Box.AutoPause)
}

// ResolveAutoPause turns an auto_pause setting into its effective window:
// "" means unset (the default), "off" disables, and a duration string sets
// the window. Values Parse would reject fall back to the default, so a
// rendered setting on a box record resolves the same way.
func ResolveAutoPause(setting string) time.Duration {
	switch setting {
	case "":
		return DefaultAutoPause
	case "off":
		return 0
	default:
		d, err := time.ParseDuration(setting)
		if err != nil || d <= 0 {
			return DefaultAutoPause
		}
		return d
	}
}

// EnvFor merges an entity's env over the contract's top-level env, per key:
// the entity wins. The result is a copy.
func (c *Contract) EnvFor(over map[string]string) map[string]string {
	return MergeEnv(c.Env, over)
}

// MergeEnv merges over onto base per key; over wins. The result is a copy; it
// is nil when both inputs are empty.
func MergeEnv(base, over map[string]string) map[string]string {
	if len(base) == 0 && len(over) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// ResolveDir resolves a declared working directory against the in-box
// worktree root. Empty means the root; a relative value resolves against it.
func ResolveDir(worktree, dir string) string {
	if dir == "" {
		return worktree
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(worktree, dir)
}

// ExecJob resolves the named job into an executable spec, merging the
// top-level env under the job's own. An unknown name errors with
// ErrNoSuchJob and lists the declared jobs.
func (c *Contract) ExecJob(name string) (Exec, error) {
	job, ok := c.Jobs[name]
	if !ok {
		return Exec{}, fmt.Errorf("%w %q; %s", ErrNoSuchJob, name, describeJobs(c.Jobs))
	}
	timeout, _ := parseTimeout(job.Timeout) // Parse validated the value
	return Exec{
		Command: job.Command,
		Dir:     job.Dir,
		Env:     c.EnvFor(job.Env),
		Timeout: timeout,
	}, nil
}

// AdHocExec resolves an ad-hoc argv run under the top-level env.
func (c *Contract) AdHocExec(argv []string) Exec {
	return Exec{Command: ArgvCommand(argv), Env: c.EnvFor(nil)}
}

// JobNames returns declared job names in stable order.
func (c *Contract) JobNames() []string {
	names := make([]string, 0, len(c.Jobs))
	for name := range c.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ServiceNames returns service names in stable order.
func (c *Contract) ServiceNames() []string {
	names := make([]string, 0, len(c.Services))
	for name := range c.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// describeJobs renders the declared jobs for an unknown-job error: sorted
// names, each with its description in parentheses.
func describeJobs(jobs map[string]Job) string {
	if len(jobs) == 0 {
		return "no jobs declared"
	}
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		if desc := jobs[name].Description; desc != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", name, desc))
		} else {
			parts = append(parts, name)
		}
	}
	return "declared jobs: " + strings.Join(parts, ", ")
}

func parseTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid timeout %q", value)
	}
	return d, nil
}
