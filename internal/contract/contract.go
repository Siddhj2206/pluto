// Package contract parses a repository's .pluto.toml: the declaration of
// provision, wake, services, and schedules that a box applies (ADR 0007).
//
// The daemon parses the contract on the host, where the worktree lives, and
// sends it to the guest agent. The box itself never needs to parse TOML.
package contract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

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

var serviceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Contract is a parsed .pluto.toml.
type Contract struct {
	Box       Box                `toml:"box"`
	Provision *Phase             `toml:"provision"`
	Wake      *Phase             `toml:"wake"`
	Services  map[string]Service `toml:"services"`
	Schedules []Schedule         `toml:"schedule"` // M1; parsed so typos still fail
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

// Phase is a provision or wake hook: a shell command and its timebox.
type Phase struct {
	Command string `toml:"command"`
	Timeout string `toml:"timeout"`
}

// Service is a long-lived declared process.
type Service struct {
	Command string `toml:"command"`
	Port    int    `toml:"port"`
}

// Schedule is a recurring wake (M1).
type Schedule struct {
	Name    string `toml:"name"`
	Cron    string `toml:"cron"`
	Command string `toml:"command"`
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
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
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
	if c.Box.AutoPause != "" && c.Box.AutoPause != "off" {
		if _, err := parseTimeout(c.Box.AutoPause); err != nil {
			return fmt.Errorf("box.auto_pause: %w", err)
		}
	}
	if c.Provision != nil {
		if c.Provision.Command == "" {
			return errors.New("provision: command is required")
		}
		if _, err := parseTimeout(c.Provision.Timeout); err != nil {
			return fmt.Errorf("provision: %w", err)
		}
	}
	if c.Wake != nil {
		if c.Wake.Command == "" {
			return errors.New("wake: command is required")
		}
		if _, err := parseTimeout(c.Wake.Timeout); err != nil {
			return fmt.Errorf("wake: %w", err)
		}
	}
	for name, svc := range c.Services {
		if !serviceName.MatchString(name) {
			return fmt.Errorf("services.%s: name must be letters, digits, '-' or '_'", name)
		}
		if svc.Command == "" {
			return fmt.Errorf("services.%s: command is required", name)
		}
		if svc.Port < 0 || svc.Port > 65535 {
			return fmt.Errorf("services.%s: port %d is out of range", name, svc.Port)
		}
	}
	return nil
}

// Empty reports whether the contract declares no phases or services.
func (c *Contract) Empty() bool {
	return c.Provision == nil && c.Wake == nil && len(c.Services) == 0
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
	switch c.Box.AutoPause {
	case "":
		return DefaultAutoPause
	case "off":
		return 0
	default:
		d, err := time.ParseDuration(c.Box.AutoPause)
		if err != nil || d <= 0 {
			return DefaultAutoPause
		}
		return d
	}
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
