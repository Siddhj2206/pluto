// Package contract parses a repository's .pluto.toml: the declaration of
// provision, wake, tools, services, jobs, sessions, and schedules that a box
// applies (ADR 0007).
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
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Siddhj2206/pluto/internal/shquote"
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
	// DefaultCPUs and DefaultMemoryMiB size a box whose contract declares no
	// [box].resources: today's hardcoded 2 vCPU / 1024 MiB.
	DefaultCPUs      = 2
	DefaultMemoryMiB = 1024
)

// nameRule is the shared rule for service, job, and session names.
var nameRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var envNameRule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// aptPackageRule and aptVersionRule constrain [tools] packages to names and
// versions apt understands. Debian package names are lowercase alphanumerics,
// '+', '-', and '.'; versions add ':' (epoch) and '~' (upstream rebase).
var (
	aptPackageRule = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	aptVersionRule = regexp.MustCompile(`^[A-Za-z0-9.+:~_-]+$`)
)

// ErrNoSuchJob reports a `pluto run` or schedule reference to a job no
// [jobs.<name>] declares.
var ErrNoSuchJob = errors.New("no such job")

// ErrNoSuchSession reports a reference to a session no [sessions.<name>]
// declares.
var ErrNoSuchSession = errors.New("no such session")

// ErrInvalid reports a contract that failed to load: the file is malformed
// or fails validation. The daemon sends the fact over the wire, and the CLI
// turns it into the edit-and-retry hint (ADR 0009).
var ErrInvalid = errors.New("invalid contract")

// InvalidError wraps a contract load failure so callers can classify it with
// errors.Is(err, ErrInvalid) while the message stays the parser's.
type InvalidError struct{ err error }

func (e *InvalidError) Error() string { return e.err.Error() }
func (e *InvalidError) Unwrap() error { return e.err }
func (e *InvalidError) Is(target error) bool {
	return target == ErrInvalid
}

// Contract is a parsed .pluto.toml.
type Contract struct {
	Box       Box                `toml:"box"`
	Env       map[string]string  `toml:"env"`
	Tools     *Tools             `toml:"tools"`
	Provision *Phase             `toml:"provision"`
	Wake      *Phase             `toml:"wake"`
	Services  map[string]Service `toml:"services"`
	Jobs      map[string]Job     `toml:"jobs"`
	Sessions  map[string]Session `toml:"sessions"`
	Schedules []Schedule         `toml:"schedule"`
	Events    Events             `toml:"events"`
}

// Events declares the trusted jobs that repository events may start.
type Events struct {
	Push        *EventPolicy                  `toml:"push"`
	PullRequest *PullRequestPolicy            `toml:"pull_request"`
	Issue       *IssuePolicy                  `toml:"issue"`
	Generic     map[string]GenericEventPolicy `toml:"generic"`
}

// GenericEventPolicy maps a generic webhook event type and optional action
// allowlist to one declared job. Its authority is the trusted default branch.
type GenericEventPolicy struct {
	Job     string   `toml:"job" schema:"required"`
	Actions []string `toml:"actions"`
}

// Allows reports whether an action passes this generic event's optional filter.
func (p GenericEventPolicy) Allows(action string) bool {
	if len(p.Actions) == 0 {
		return true
	}
	for _, allowed := range p.Actions {
		if allowed == action {
			return true
		}
	}
	return false
}

// PullRequestPolicy maps a configured GitHub pull request action to one job.
// Actions are explicit so a PR cannot start work on unreviewed event kinds.
type PullRequestPolicy struct {
	Job             string   `toml:"job" schema:"required"`
	Actions         []string `toml:"actions" schema:"required"`
	CredentialNames []string `toml:"credentials"`
	TrustedLabel    string   `toml:"trusted_label"`
}

// Allows reports whether a pull request action is explicitly configured.
func (p *PullRequestPolicy) Allows(action string) bool {
	if p == nil {
		return false
	}
	for _, allowed := range p.Actions {
		if allowed == action {
			return true
		}
	}
	return false
}

// IssuePolicy maps configured GitHub issue actions to one job. Issue jobs
// use the trusted default-branch contract and only receive named credentials.
type IssuePolicy struct {
	Job             string   `toml:"job" schema:"required"`
	Actions         []string `toml:"actions" schema:"required"`
	CredentialNames []string `toml:"credentials"`
}

// Allows reports whether an issue action is explicitly configured.
func (p *IssuePolicy) Allows(action string) bool {
	if p == nil {
		return false
	}
	for _, allowed := range p.Actions {
		if allowed == action {
			return true
		}
	}
	return false
}

// EventPolicy maps an allowed event to one declared job.
type EventPolicy struct {
	Job string `toml:"job"`
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

// sizeRule matches a binary size: digits then a unit. The unit is a
// power-of-two prefix (K/M/G/T/P), optionally "i" and/or "B": "512MiB",
// "8GiB", "2G". Sizes are binary throughout. Memory and disk share the
// grammar, so one rule serves both.
var sizeRule = regexp.MustCompile(`^([0-9]+)([KMGTPkmgtp])([iI]?)([bB]?)$`)

// parseSizeMiB parses a binary size string into whole MiB. what names the
// field in error messages ("memory", "disk"). An empty string means "unset"
// and returns 0; a size below 1 MiB is an error. Anything else is an error too.
func parseSizeMiB(s, what string) (int, error) {
	if s == "" {
		return 0, nil
	}
	m := sizeRule.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid %s size %q (want e.g. \"512MiB\" or \"8GiB\")", what, s)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("invalid %s size %q: %w", what, s, err)
	}
	shift := map[byte]uint{'K': 10, 'M': 20, 'G': 30, 'T': 40, 'P': 50}[strings.ToUpper(m[2])[0]]
	miB := int(int64(n) << shift >> 20)
	if miB <= 0 {
		return 0, fmt.Errorf("%s size %q is below 1 MiB", what, s)
	}
	return miB, nil
}

// ParseMemoryMiB parses a [box].resources.memory string into whole MiB. Sizes
// are binary: 1 KiB = 1024 B and 1 MiB = 1024 KiB.
func ParseMemoryMiB(s string) (int, error) { return parseSizeMiB(s, "memory") }

// ParseDiskMiB parses a [box].resources.disk string into whole MiB. It shares
// the binary size grammar with memory. An empty string means "unset" and
// returns 0: the box keeps the base image's size.
func ParseDiskMiB(s string) (int, error) { return parseSizeMiB(s, "disk") }

// Phase is a provision or wake hook: a declared command and its timebox.
type Phase struct {
	Command Command           `toml:"command" schema:"required"`
	Dir     string            `toml:"dir"`
	Env     map[string]string `toml:"env"`
	Timeout string            `toml:"timeout"`
}

// Tools is the optional [tools] section: the apt packages a box installs
// before its [provision] command. It is the declarative half of packaging;
// anything apt cannot install stays in the imperative [provision] (#78).
type Tools struct {
	Packages []Package `toml:"packages" schema:"required"`
}

// Package is one entry under [tools] packages: a bare apt name, or a table
// with a name and an optional exact version. An exact version renders as apt's
// name=version spec; there is no floating-version resolution (#78).
type Package struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

// UnmarshalTOML accepts a string (a bare package name) or a table with name
// and version. Unknown keys inside the table are errors, matching the
// contract-wide rule that a typo fails at load time.
func (p *Package) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		p.Name, p.Version = t, ""
	case map[string]any:
		for key := range t {
			if key != "name" && key != "version" {
				return fmt.Errorf("unknown package key %q", key)
			}
		}
		name, ok := t["name"]
		if !ok {
			return errors.New("package: name is required")
		}
		s, ok := name.(string)
		if !ok {
			return errors.New("package: name must be a string")
		}
		version := ""
		if raw, ok := t["version"]; ok {
			vs, ok := raw.(string)
			if !ok {
				return errors.New("package: version must be a string")
			}
			version = vs
		}
		p.Name, p.Version = s, version
	default:
		return errors.New("package must be a package name or a table with name and version")
	}
	return nil
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

// Session is a long-lived interactive command run under tmux, attachable with
// `pluto attach <box> --session <name>` (ADR 0010). It mirrors Service but
// takes no port; unlike a job it has no bounded outcome or timeout.
type Session struct {
	Description string            `toml:"description"`
	Command     Command           `toml:"command" schema:"required"`
	Dir         string            `toml:"dir"`
	Env         map[string]string `toml:"env"`
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
	Command      Command           `json:"command"`
	Dir          string            `json:"dir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	SensitiveEnv []string          `json:"sensitive_env,omitempty"`
	Timeout      time.Duration     `json:"timeout,omitempty"`
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
		return nil, &InvalidError{err: contractError(path, string(data), err)}
	}
	return c, nil
}

// contractError renders a contract failure with its file and, when the spot
// is known, its line: the fix is an edit there (ADR 0009). Syntax errors
// carry the parser's position; semantic errors locate the key they blame,
// best effort.
func contractError(path, data string, err error) error {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) && parseErr.Position.Line > 0 {
		return fmt.Errorf("%s:%d: %s", path, parseErr.Position.Line, parseErr.Message)
	}
	if line := locateKeyLine(data, err); line > 0 {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// keyError is a semantic validation failure that blames a TOML key, so Load
// can point at the line where the key is declared. The locator is best
// effort: an unfindable key still reports the file alone.
type keyError struct {
	key string
	err error
}

func (e *keyError) Error() string { return e.err.Error() }
func (e *keyError) Unwrap() error { return e.err }

func keyErrorf(key, format string, args ...any) error {
	return &keyError{key: key, err: fmt.Errorf(format, args...)}
}

// locateKeyLine returns the line a keyError blames, or 0 when there is none.
func locateKeyLine(data string, err error) int {
	var keyed *keyError
	if !errors.As(err, &keyed) {
		return 0
	}
	return locateKey(data, keyed.key)
}

// locateKey returns the 1-based line where a dotted TOML key is declared in
// data, or 0 when a best-effort scan cannot place it. A key that is missing
// from the document falls back to the line that declares its nearest
// ancestor, so a missing `provision.command` still points at [provision].
// The scan understands section headers, dotted assignments, and [[schedule]]
// entries; it deliberately does not parse TOML.
func locateKey(data, key string) int {
	parts := splitTOMLKey(key)
	if parts[0] == "schedule" && len(parts) > 1 {
		if line := locateScheduleKey(data, parts[1], parts[2:]...); line > 0 {
			return line
		}
	}
	for i := len(parts); i > 0; i-- {
		if line := scanForKey(data, parts[:i]); line > 0 {
			return line
		}
	}
	return 0
}

// scanForKey finds the line declaring exactly want, the dotted path of a
// section header or an assignment.
func scanForKey(data string, want []string) int {
	var section []string
	for i, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "[["):
			if end := strings.Index(line, "]]"); end >= 0 {
				section = splitTOMLKey(strings.TrimSpace(line[2:end]))
				if keyPathEqual(section, want) {
					return i + 1
				}
			}
		case strings.HasPrefix(line, "["):
			if end := strings.Index(line, "]"); end >= 0 {
				section = splitTOMLKey(strings.TrimSpace(line[1:end]))
				if keyPathEqual(section, want) {
					return i + 1
				}
			}
		default:
			name := line
			if eq := strings.IndexByte(line, '='); eq >= 0 {
				name = strings.TrimSpace(line[:eq])
			}
			full := append(append([]string{}, section...), splitTOMLKey(name)...)
			if keyPathEqual(full, want) {
				return i + 1
			}
		}
	}
	return 0
}

// locateScheduleKey finds the [[schedule]] entry whose name matches and
// reports the line of one of its fields, or its name line when the field is
// missing. It returns 0 when no entry declares that name.
func locateScheduleKey(data, name string, fields ...string) int {
	lines := strings.Split(data, "\n")
	type block struct{ start, end int }
	var blocks []block
	for i, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		switch {
		case line == "[[schedule]]":
			if n := len(blocks); n > 0 && blocks[n-1].end == len(lines) {
				blocks[n-1].end = i
			}
			blocks = append(blocks, block{start: i, end: len(lines)})
		case len(blocks) > 0 && blocks[len(blocks)-1].end == len(lines) && strings.HasPrefix(line, "["):
			blocks[len(blocks)-1].end = i
		}
	}
	for _, b := range blocks {
		nameLine, fieldLine := 0, 0
		for i := b.start + 1; i < b.end; i++ {
			key, value, ok := strings.Cut(strings.TrimSpace(stripComment(lines[i])), "=")
			if !ok {
				continue
			}
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if key == "name" && value == strconv.Quote(name) {
				nameLine = i + 1
				continue
			}
			if nameLine > 0 {
				for _, field := range fields {
					if key == field && fieldLine == 0 {
						fieldLine = i + 1
					}
				}
			}
		}
		if nameLine > 0 {
			if fieldLine > 0 {
				return fieldLine
			}
			return nameLine
		}
	}
	return 0
}

// splitTOMLKey splits a dotted TOML key on dots outside quotes and strips
// one layer of quotes from each part.
func splitTOMLKey(s string) []string {
	var parts []string
	var b strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
			b.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			b.WriteRune(r)
		case r == '.':
			parts = append(parts, unquoteKey(strings.TrimSpace(b.String())))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(parts, unquoteKey(strings.TrimSpace(b.String())))
}

func unquoteKey(part string) string {
	if len(part) >= 2 && (part[0] == '"' && part[len(part)-1] == '"' || part[0] == '\'' && part[len(part)-1] == '\'') {
		return part[1 : len(part)-1]
	}
	return part
}

func keyPathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stripComment cuts a line at a # outside TOML strings.
func stripComment(line string) string {
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#':
			return line[:i]
		}
	}
	return line
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
		return nil, keyErrorf(keys[0], "unknown keys: %v", keys)
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
			return keyErrorf("box.auto_pause", "box.auto_pause: %w", err)
		}
	}
	if c.Box.Resources.CPUs < 0 {
		return keyErrorf("box.resources.cpus", "box.resources.cpus: must not be negative")
	}
	if _, err := ParseMemoryMiB(c.Box.Resources.Memory); err != nil {
		return keyErrorf("box.resources.memory", "box.resources.memory: %w", err)
	}
	if _, err := ParseDiskMiB(c.Box.Resources.Disk); err != nil {
		return keyErrorf("box.resources.disk", "box.resources.disk: %w", err)
	}
	if c.Tools != nil {
		if err := c.validateTools(); err != nil {
			return err
		}
	}
	if c.Provision != nil {
		if c.Provision.Command.IsZero() {
			return keyErrorf("provision.command", "provision: command is required")
		}
		if _, err := parseTimeout(c.Provision.Timeout); err != nil {
			return keyErrorf("provision.timeout", "provision: %w", err)
		}
		if err := validateEnv("provision.env", c.Provision.Env); err != nil {
			return err
		}
	}
	if c.Wake != nil {
		if c.Wake.Command.IsZero() {
			return keyErrorf("wake.command", "wake: command is required")
		}
		if _, err := parseTimeout(c.Wake.Timeout); err != nil {
			return keyErrorf("wake.timeout", "wake: %w", err)
		}
		if err := validateEnv("wake.env", c.Wake.Env); err != nil {
			return err
		}
	}
	for name, svc := range c.Services {
		if !nameRule.MatchString(name) {
			return keyErrorf("services."+name, "services.%s: name must be letters, digits, '-' or '_'", name)
		}
		if svc.Command.IsZero() {
			return keyErrorf("services."+name+".command", "services.%s: command is required", name)
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return keyErrorf("services."+name+".port", "services.%s: port %d is out of range", name, svc.Port)
		}
		if err := validateEnv("services."+name+".env", svc.Env); err != nil {
			return err
		}
	}
	for name, job := range c.Jobs {
		if !nameRule.MatchString(name) {
			return keyErrorf("jobs."+name, "jobs.%s: name must be letters, digits, '-' or '_'", name)
		}
		if job.Command.IsZero() {
			return keyErrorf("jobs."+name+".command", "jobs.%s: command is required", name)
		}
		if _, err := parseTimeout(job.Timeout); err != nil {
			return keyErrorf("jobs."+name+".timeout", "jobs.%s: %w", name, err)
		}
		if err := validateEnv("jobs."+name+".env", job.Env); err != nil {
			return err
		}
	}
	if c.Events.Push != nil {
		if c.Events.Push.Job == "" {
			return keyErrorf("events.push.job", "events.push.job: job is required")
		}
		if _, ok := c.Jobs[c.Events.Push.Job]; !ok {
			return keyErrorf("events.push.job", "events.push.job: %w %q", ErrNoSuchJob, c.Events.Push.Job)
		}
	}
	if p := c.Events.PullRequest; p != nil {
		if p.Job == "" {
			return keyErrorf("events.pull_request.job", "events.pull_request.job: job is required")
		}
		if _, ok := c.Jobs[p.Job]; !ok {
			return keyErrorf("events.pull_request.job", "events.pull_request.job: %w %q", ErrNoSuchJob, p.Job)
		}
		if len(p.Actions) == 0 {
			return keyErrorf("events.pull_request.actions", "events.pull_request.actions: at least one action is required")
		}
		seen := map[string]bool{}
		for _, action := range p.Actions {
			if action == "" || seen[action] {
				return keyErrorf("events.pull_request.actions", "events.pull_request.actions: actions must be non-empty and unique")
			}
			seen[action] = true
		}
		seenCredentials := map[string]bool{}
		for _, name := range p.CredentialNames {
			if !envNameRule.MatchString(name) || strings.HasPrefix(name, "PLUTO_") || seenCredentials[name] {
				return keyErrorf("events.pull_request.credentials", "events.pull_request.credentials: names must be valid, unique environment keys")
			}
			seenCredentials[name] = true
		}
	}
	if p := c.Events.Issue; p != nil {
		if p.Job == "" {
			return keyErrorf("events.issue.job", "events.issue.job: job is required")
		}
		if _, ok := c.Jobs[p.Job]; !ok {
			return keyErrorf("events.issue.job", "events.issue.job: %w %q", ErrNoSuchJob, p.Job)
		}
		if len(p.Actions) == 0 {
			return keyErrorf("events.issue.actions", "events.issue.actions: at least one action is required")
		}
		seen := map[string]bool{}
		for _, action := range p.Actions {
			if action == "" || seen[action] {
				return keyErrorf("events.issue.actions", "events.issue.actions: actions must be non-empty and unique")
			}
			seen[action] = true
		}
		seenCredentials := map[string]bool{}
		for _, name := range p.CredentialNames {
			if !envNameRule.MatchString(name) || strings.HasPrefix(name, "PLUTO_") || seenCredentials[name] {
				return keyErrorf("events.issue.credentials", "events.issue.credentials: names must be valid, unique environment keys")
			}
			seenCredentials[name] = true
		}
	}
	for eventType, policy := range c.Events.Generic {
		if !nameRule.MatchString(eventType) {
			return keyErrorf("events.generic."+eventType, "events.generic.%s: event type must be letters, digits, '-' or '_'", eventType)
		}
		if policy.Job == "" {
			return keyErrorf("events.generic."+eventType+".job", "events.generic.%s.job: job is required", eventType)
		}
		if _, ok := c.Jobs[policy.Job]; !ok {
			return keyErrorf("events.generic."+eventType+".job", "events.generic.%s.job: %w %q", eventType, ErrNoSuchJob, policy.Job)
		}
		seen := map[string]bool{}
		for _, action := range policy.Actions {
			if action == "" || seen[action] {
				return keyErrorf("events.generic."+eventType+".actions", "events.generic.%s.actions: actions must be non-empty and unique", eventType)
			}
			seen[action] = true
		}
	}
	for name, sess := range c.Sessions {
		if !nameRule.MatchString(name) {
			return keyErrorf("sessions."+name, "sessions.%s: name must be letters, digits, '-' or '_'", name)
		}
		if sess.Command.IsZero() {
			return keyErrorf("sessions."+name+".command", "sessions.%s: command is required", name)
		}
		if err := validateEnv("sessions."+name+".env", sess.Env); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(c.Schedules))
	for _, s := range c.Schedules {
		if s.Name == "" {
			return keyErrorf("schedule", "schedule: name is required")
		}
		if seen[s.Name] {
			return keyErrorf("schedule."+s.Name, "schedule %q: duplicate name", s.Name)
		}
		seen[s.Name] = true
		if s.Cron == "" {
			return keyErrorf("schedule."+s.Name+".cron", "schedule %q: cron is required", s.Name)
		}
		if _, err := ParseCron(s.Cron); err != nil {
			return keyErrorf("schedule."+s.Name+".cron", "schedule %q: invalid cron %q: %w", s.Name, s.Cron, err)
		}
		if s.Job != "" {
			if _, ok := c.Jobs[s.Job]; !ok {
				return keyErrorf("schedule."+s.Name+".job", "schedule %q: job %q is not declared in [jobs.*]", s.Name, s.Job)
			}
		}
	}
	return nil
}

// validateTools enforces the [tools] rules: at least one package, apt-legal
// names, and apt-legal exact versions. The packages are installed by a
// generated preamble ahead of the [provision] command (#78).
func (c *Contract) validateTools() error {
	if len(c.Tools.Packages) == 0 {
		return keyErrorf("tools.packages", "tools: packages is required")
	}
	for i, p := range c.Tools.Packages {
		if !aptPackageRule.MatchString(p.Name) {
			return keyErrorf("tools.packages", "tools.packages[%d]: %q is not an apt package name", i, p.Name)
		}
		if p.Version != "" && !aptVersionRule.MatchString(p.Version) {
			return keyErrorf("tools.packages", "tools.packages[%d]: %q is not a valid apt version", i, p.Version)
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
			return keyErrorf(section, "%s: env keys must not be empty", section)
		case strings.HasPrefix(key, "PLUTO_"):
			return keyErrorf(section+"."+key, "%s.%s: the PLUTO_ prefix is reserved", section, key)
		case strings.ContainsAny(key, "=\x00"):
			return keyErrorf(section, "%s: invalid env key %q", section, key)
		case strings.ContainsAny(value, "\x00\n\r"):
			return keyErrorf(section+"."+key, "%s.%s: env values must be single-line", section, key)
		}
	}
	return nil
}

// Empty reports whether the contract declares nothing to run: no phases,
// tools, services, jobs, sessions, or schedules. Top-level env alone does not
// count.
func (c *Contract) Empty() bool {
	return !c.HasProvision() && c.Wake == nil &&
		len(c.Services) == 0 && len(c.Jobs) == 0 && len(c.Sessions) == 0 && len(c.Schedules) == 0
}

// HasProvision reports whether the contract has provision work: a declared
// [provision] phase, [tools] packages, or both.
func (c *Contract) HasProvision() bool {
	return c.Provision != nil || (c.Tools != nil && len(c.Tools.Packages) > 0)
}

// ProvisionCommand returns the effective provision command. [tools] packages
// generate an apt preamble that runs first through the box's passwordless
// sudo, then the declared [provision] command runs unprivileged (as the box
// user) in the same shell line, so a failed install stops the sequence. It
// returns the declared command unchanged when there are no [tools], so
// existing contracts behave exactly as before, and the zero Command when the
// contract declares neither. Parse has already validated the packages.
func (c *Contract) ProvisionCommand() Command {
	declared := Command{}
	if c.Provision != nil {
		declared = c.Provision.Command
	}
	var preamble string
	if c.Tools != nil && len(c.Tools.Packages) > 0 {
		preamble = aptInstallCommand(c.Tools.Packages)
	}
	switch {
	case preamble == "":
		return declared
	case declared.IsZero():
		return ShellCommand(preamble)
	default:
		return ShellCommand(preamble + " && " + shellCommandString(declared))
	}
}

// aptInstallCommand renders the generated provisioning preamble for [tools]
// packages: refresh the index, then install every package in one apt call. The
// provision hook runs as the unprivileged box user (a systemd user unit), so
// both calls elevate through the box's passwordless sudo. `-n` makes sudo fail
// fast instead of hanging on a prompt if it is ever absent.
func aptInstallCommand(pkgs []Package) string {
	specs := make([]string, len(pkgs))
	for i, p := range pkgs {
		specs[i] = shquote.Quote(p.aptSpec())
	}
	return "sudo -n apt-get update && sudo -n apt-get install -y " + strings.Join(specs, " ")
}

// aptSpec is a package's apt install argument: its name, or name=version when
// an exact version is pinned.
func (p Package) aptSpec() string {
	if p.Version != "" {
		return p.Name + "=" + p.Version
	}
	return p.Name
}

// shellCommandString renders a declared command for use inside a composed
// shell line: a shell command as-is, an argv command as an exec of its
// shell-quoted arguments so argument boundaries survive.
func shellCommandString(c Command) string {
	if c.shell != "" {
		return c.shell
	}
	quoted := make([]string, len(c.argv))
	for i, arg := range c.argv {
		quoted[i] = shquote.Quote(arg)
	}
	return "exec " + strings.Join(quoted, " ")
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

// SessionExec resolves the named session into an executable spec, merging the
// top-level env under the session's own. Sessions have no timeout. An unknown
// name errors with ErrNoSuchSession and lists the declared sessions.
func (c *Contract) SessionExec(name string) (Exec, error) {
	sess, ok := c.Sessions[name]
	if !ok {
		return Exec{}, fmt.Errorf("%w %q; %s", ErrNoSuchSession, name, describeSessions(c.Sessions))
	}
	return Exec{
		Command: sess.Command,
		Dir:     sess.Dir,
		Env:     c.EnvFor(sess.Env),
	}, nil
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

// SessionNames returns session names in stable order.
func (c *Contract) SessionNames() []string {
	names := make([]string, 0, len(c.Sessions))
	for name := range c.Sessions {
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

// describeSessions renders the declared sessions for an unknown-session
// error: sorted names, each with its description in parentheses.
func describeSessions(sessions map[string]Session) string {
	if len(sessions) == 0 {
		return "no sessions declared"
	}
	names := make([]string, 0, len(sessions))
	for name := range sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		if desc := sessions[name].Description; desc != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", name, desc))
		} else {
			parts = append(parts, name)
		}
	}
	return "declared sessions: " + strings.Join(parts, ", ")
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
