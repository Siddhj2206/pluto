package contract

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// nameRule is the shared rule for task and schedule names.
var nameRule = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// envNameRule constrains a secret name to a valid environment key.
var envNameRule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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

// Command is a declared command. A string runs via '/bin/sh -c'; an array is
// exec'd directly, with no shell. It is opaque: build one with ShellCommand or
// ArgvCommand and run it with Argv. pluto.yml decodes it from either declared
// shape through UnmarshalYAML.
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

// String renders the command for display and run records.
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

// MarshalJSON renders the command in its declared shape, so Project.Hash — a
// canonical JSON encoding of the compiled contract — distinguishes a shell
// string from an argv array.
func (c Command) MarshalJSON() ([]byte, error) {
	if c.shell != "" {
		return json.Marshal(c.shell)
	}
	return json.Marshal(c.argv)
}

// UnmarshalJSON accepts either declared shape.
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

// Exec is a command resolved for execution: the command, the working directory
// (relative to the in-box workspace unless absolute), the environment already
// merged with the contract's top level, and the timebox (zero is unlimited).
type Exec struct {
	Command      Command           `json:"command"`
	Dir          string            `json:"dir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	SensitiveEnv []string          `json:"sensitive_env,omitempty"`
	Timeout      time.Duration     `json:"timeout,omitempty"`
}
