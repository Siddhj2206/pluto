// Package task reads a repo's .pluto directory (ADR 0007).
package task

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"
)

const (
	// Dir is where a repo keeps Pluto's config.
	Dir = ".pluto"
	// DefaultTimeout limits a task that doesn't set its own. Long builds should
	// set one, and a hung task shouldn't hold the host's slot for days.
	DefaultTimeout = 12 * time.Hour
	// Privileged is the one privilege a repo can request so far.
	Privileged = "privileged"
)

var (
	taskName   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// ValidSecretName reports whether name can be used as an environment variable.
// Names starting PLUTO_ are Pluto's own, which it sets on every run.
func ValidSecretName(name string) bool {
	return secretName.MatchString(name) && !strings.HasPrefix(name, "PLUTO_")
}

// Task is one named piece of work.
type Task struct {
	Name     string        `json:"name"`
	Run      []string      `json:"run"`
	Schedule string        `json:"schedule,omitempty"`
	Timeout  time.Duration `json:"timeout"`
	Secrets  []string      `json:"secrets,omitempty"`
}

// Config is a repo's parsed .pluto directory. Image and Requires describe the
// repo's box, so they aren't per task.
type Config struct {
	Image    string          `json:"image,omitempty"`
	Requires []string        `json:"requires,omitempty"`
	Tasks    map[string]Task `json:"tasks"`
}

type rawTask struct {
	Run      []string `yaml:"run"`
	Schedule string   `yaml:"schedule"`
	Timeout  string   `yaml:"timeout"`
	Secrets  []string `yaml:"secrets"`
}

type rawConfig struct {
	Image    string             `yaml:"image"`
	Requires []string           `yaml:"requires"`
	Tasks    map[string]rawTask `yaml:"tasks"`
}

func decode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Parse builds a Config from file contents, keyed by their path inside .pluto,
// such as "pluto.yml" or "tasks/nightly.yml". Files elsewhere are ignored.
func Parse(files map[string][]byte) (*Config, error) {
	cfg := &Config{Tasks: map[string]Task{}}
	origin := map[string]string{}
	add := func(name, file string, raw rawTask) error {
		if other, dup := origin[name]; dup {
			return fmt.Errorf("task %q is defined in both %s and %s", name, other, file)
		}
		t, err := build(name, raw)
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		origin[name], cfg.Tasks[name] = file, t
		return nil
	}
	for rel := range files {
		dir, _ := path.Split(rel)
		if strings.HasSuffix(rel, ".yaml") && (dir == "" && rel == "pluto.yaml" || dir == "tasks/") {
			return nil, fmt.Errorf("%s/%s: Pluto only reads .yml files, so rename it", Dir, rel)
		}
	}
	if data, ok := files["pluto.yml"]; ok {
		const file = Dir + "/pluto.yml"
		var raw rawConfig
		if err := decode(data, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, r := range raw.Requires {
			if r != Privileged {
				return nil, fmt.Errorf("%s: unknown requirement %q (only %q exists)", file, r, Privileged)
			}
		}
		cfg.Image, cfg.Requires = raw.Image, raw.Requires
		for _, name := range slices.Sorted(maps.Keys(raw.Tasks)) {
			if err := add(name, file, raw.Tasks[name]); err != nil {
				return nil, err
			}
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		dir, base := path.Split(rel)
		name, isYAML := strings.CutSuffix(base, ".yml")
		if dir != "tasks/" || !isYAML {
			continue
		}
		file := Dir + "/" + rel
		var raw rawTask
		if err := decode(files[rel], &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := add(name, file, raw); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func build(name string, raw rawTask) (Task, error) {
	if !taskName.MatchString(name) {
		return Task{}, fmt.Errorf("%q isn't a valid task name: use letters, digits, - and _", name)
	}
	if len(raw.Run) == 0 {
		return Task{}, fmt.Errorf("task %q has nothing to run: add a run: list", name)
	}
	t := Task{Name: name, Run: raw.Run, Schedule: raw.Schedule, Timeout: DefaultTimeout, Secrets: raw.Secrets}
	if raw.Schedule != "" {
		if _, err := ParseSchedule(raw.Schedule); err != nil {
			return Task{}, fmt.Errorf("task %q: %w", name, err)
		}
	}
	if raw.Timeout != "" {
		d, err := time.ParseDuration(raw.Timeout)
		if err != nil || d <= 0 {
			return Task{}, fmt.Errorf("task %q: timeout %q should look like 90m or 6h", name, raw.Timeout)
		}
		t.Timeout = d
	}
	for _, s := range raw.Secrets {
		if !ValidSecretName(s) {
			return Task{}, fmt.Errorf("task %q: secret %q should look like MY_SECRET", name, s)
		}
	}
	return t, nil
}

// ParseSchedule reads a five-field cron expression in the host's local time.
func ParseSchedule(expr string) (cron.Schedule, error) {
	if strings.Contains(expr, "TZ=") {
		return nil, fmt.Errorf("schedule %q: schedules always use the host's local time", expr)
	}
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", expr, err)
	}
	// A date that never happens, such as February 31, has no next time.
	if s.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("schedule %q never comes round", expr)
	}
	return s, nil
}

// LoadDir reads a repo's .pluto directory from disk. A repo without one gets an
// empty Config, since the directory is optional.
func LoadDir(repoRoot string) (*Config, error) {
	root := filepath.Join(repoRoot, Dir)
	files := map[string][]byte{}
	read := func(rel string) error {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	}
	for _, name := range []string{"pluto.yml", "pluto.yaml"} {
		if err := read(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "tasks"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			if err := read("tasks/" + e.Name()); err != nil {
				return nil, err
			}
		}
	}
	return Parse(files)
}
