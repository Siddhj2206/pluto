// pluto.yml is a project's box contract (#139): how to provision its
// environment, what happens on every start, whether the box may run its own
// container engine, the secrets to mask, the named tasks it can run, and the
// schedules that trigger them.
//
// Every node may be written inline or reference a typed YAML file with the
// same shape. A reference is a bare string where the node's value would be a
// mapping; the loader resolves it against the referring file's directory and
// compiles both forms into one Project.
//
//go:generate go run ../../cmd/pluto-schema ../../pluto.yml.schema.json
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLFileName is the YAML box contract's path inside a workspace.
const YAMLFileName = "pluto.yml"

// ErrNoSuchTask reports a schedule that names a task no [tasks.<name>]
// declares.
var ErrNoSuchTask = errors.New("no such task")

// Project is the compiled pluto.yml contract: the one internal execution model
// every accepted form — inline nodes and typed file references alike —
// produces.
type Project struct {
	Version   int                     `yaml:"version" schema:"required"`
	Provision *Provision              `yaml:"provision"`
	Wake      *WakePhase              `yaml:"wake"`
	Docker    Docker                  `yaml:"docker"`
	Secrets   map[string]Secret       `yaml:"secrets"`
	Mask      []string                `yaml:"mask"`
	Tasks     map[string]Task         `yaml:"tasks"`
	Schedules map[string]TaskSchedule `yaml:"schedules"`
}

// Provision declares how the box's layers are built: the base (OS and heavy
// toolchains) plus the project packages layered on top, keyed on the watched
// lockfiles.
type Provision struct {
	Base     Base     `yaml:"base" schema:"required"`
	Packages []string `yaml:"packages"`
	Watch    []string `yaml:"watch"`
	Run      Command  `yaml:"run"`
}

// BaseKind discriminates the three accepted provision.base forms.
type BaseKind int

const (
	// BaseUnset is the zero value: no base was declared.
	BaseUnset BaseKind = iota
	// BaseImage is an OCI base image reference.
	BaseImage
	// BaseDockerfile is a path to a Dockerfile.
	BaseDockerfile
	// BaseRun is an inline golden command that builds the base.
	BaseRun
)

// Base is provision.base: an OCI reference, a Dockerfile, or inline steps.
// Exactly one form is set, named by Kind.
type Base struct {
	Kind       BaseKind `json:"kind"`
	Image      string   `json:"image,omitempty"`
	Dockerfile string   `json:"dockerfile,omitempty"`
	Run        Command  `json:"run,omitempty"`
}

// UnmarshalYAML accepts a scalar OCI reference or a one-key mapping naming a
// Dockerfile or inline run steps.
func (b *Base) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!str" || n.Value == "" {
			return errors.New("base must be an OCI reference, {dockerfile}, or {run}")
		}
		b.Kind, b.Image = BaseImage, n.Value
		return nil
	case yaml.MappingNode:
		seen := 0
		for i := 0; i < len(n.Content); i += 2 {
			key, val := n.Content[i].Value, n.Content[i+1]
			switch key {
			case "dockerfile":
				if val.Kind != yaml.ScalarNode || val.Tag != "!!str" || val.Value == "" {
					return errors.New("base.dockerfile must be a non-empty path")
				}
				b.Kind, b.Dockerfile = BaseDockerfile, val.Value
				seen++
			case "run":
				var c Command
				if err := val.Decode(&c); err != nil {
					return fmt.Errorf("base.run: %w", err)
				}
				b.Kind, b.Run = BaseRun, c
				seen++
			default:
				return fmt.Errorf("unknown base key %q", key)
			}
		}
		if seen != 1 {
			return errors.New("base must declare exactly one of dockerfile or run")
		}
		return nil
	}
	return errors.New("base must be an OCI reference, {dockerfile}, or {run}")
}

// WakePhase is a lifecycle phase: the command wake runs on every box start.
type WakePhase struct {
	Run Command `yaml:"run" schema:"required"`
}

// Docker is the box's own container-engine capability (#149). The zero value
// declares no engine. It accepts a boolean — `docker: true` runs the box's own
// engine, rootless and isolated from the host's — or the mapping
// `{privileged: true}` to opt into the elevated privileges some full builds
// (bootc, buildah with a native overlay) need. The privileged escalation
// widens the isolation boundary; its risk is recorded in docs/adr/0019.
type Docker struct {
	Enabled    bool `json:"enabled,omitempty"`
	Privileged bool `json:"privileged,omitempty"`
}

// UnmarshalYAML accepts a boolean or a one-key {privileged} mapping. The
// mapping form still enables the engine; it only names the escalation.
func (d *Docker) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!bool" {
			return errors.New("docker must be true, false, or {privileged: true}")
		}
		var on bool
		if err := n.Decode(&on); err != nil {
			return fmt.Errorf("docker: %w", err)
		}
		d.Enabled, d.Privileged = on, false
		return nil
	case yaml.MappingNode:
		seen := 0
		for i := 0; i < len(n.Content); i += 2 {
			key, val := n.Content[i].Value, n.Content[i+1]
			switch key {
			case "privileged":
				var p bool
				if err := val.Decode(&p); err != nil {
					return fmt.Errorf("docker.privileged: %w", err)
				}
				d.Privileged = p
				seen++
			default:
				return fmt.Errorf("unknown docker key %q", key)
			}
		}
		if seen != 1 {
			return errors.New("docker mapping must declare privileged")
		}
		d.Enabled = true
		return nil
	}
	return errors.New("docker must be true, false, or {privileged: true}")
}

// Lock is a task's lock policy: how its runs share the workspace's box.
type Lock string

const (
	// LockShared runs alongside other shared work and attach sessions.
	LockShared Lock = "shared"
	// LockExclusive runs alone and waits for shared work to drain.
	LockExclusive Lock = "exclusive"
	// LockService is the effective policy of a service task: it stays running
	// for the box's life. It is not written in the contract, which uses
	// service: true instead.
	LockService Lock = "service"
)

// Task is one named, durable piece of work.
type Task struct {
	Run     Command `yaml:"run" schema:"required"`
	Lock    Lock    `yaml:"lock"`
	Service bool    `yaml:"service"`
}

// EffectiveLock resolves the task's lock policy: service tasks stay running,
// an unset policy is shared, and otherwise the declared policy applies.
func (t Task) EffectiveLock() Lock {
	switch {
	case t.Service:
		return LockService
	case t.Lock == "":
		return LockShared
	default:
		return t.Lock
	}
}

// Secret is one declared secret: a host environment variable or a file whose
// value is registered with the masker.
type Secret struct {
	Env  string `yaml:"env"`
	File string `yaml:"file"`
}

// TaskSchedule is a recurring trigger that targets a named task.
type TaskSchedule struct {
	Task string `yaml:"task" schema:"required"`
	Cron string `yaml:"cron" schema:"required"`
}

// UnmarshalYAML accepts a shell string or an argv array, matching the TOML
// contract's command shape.
func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag != "!!str" || n.Value == "" {
			return errors.New("command must be a non-empty string or an array of strings")
		}
		c.shell, c.argv = n.Value, nil
		return nil
	case yaml.SequenceNode:
		argv := make([]string, len(n.Content))
		for i, item := range n.Content {
			if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return fmt.Errorf("command array items must be strings, item %d is not", i+1)
			}
			argv[i] = item.Value
		}
		if len(argv) == 0 {
			return errors.New("command must not be empty")
		}
		c.shell, c.argv = "", argv
		return nil
	}
	return errors.New("command must be a string or an array of strings")
}

// MarshalYAML renders the command in its declared shape.
func (c Command) MarshalYAML() (any, error) {
	if c.shell != "" {
		return c.shell, nil
	}
	return c.argv, nil
}

// LoadProject reads pluto.yml from a workspace. A missing file is not an
// error: it yields an empty project, a box with no declared work.
func LoadProject(worktree string) (*Project, error) {
	path := filepath.Join(worktree, YAMLFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Project{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	p, err := parseProject(data, path)
	if err != nil {
		return nil, &InvalidError{err: err}
	}
	return p, nil
}

// ParseProject parses and validates a pluto.yml from a string. Relative
// references resolve against the current directory; LoadProject is the usual
// entry point.
func ParseProject(data string) (*Project, error) {
	return parseProject([]byte(data), YAMLFileName)
}

// parseProject is the single loader: it resolves typed file references inline,
// decodes the one schema over the resulting tree, and validates it. Errors
// already carry the source file and, where known, the line.
func parseProject(data []byte, path string) (*Project, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %s", path, yamlMessage(err))
	}
	if len(doc.Content) == 0 {
		p := &Project{}
		if err := p.validate(path); err != nil {
			return nil, err
		}
		return p, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s:%d: contract must be a mapping", path, root.Line)
	}
	l := &loader{path: path}
	p := &Project{}
	seen := map[string]bool{}
	for i := 0; i < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		name := key.Value
		if seen[name] {
			return nil, fmt.Errorf("%s:%d: duplicate field %q", path, key.Line, name)
		}
		seen[name] = true
		var err error
		switch name {
		case "version":
			err = decodeInto(val, &p.Version, path, "version")
		case "provision":
			p.Provision, err = decodeRef[Provision](l, val, "provision")
		case "wake":
			p.Wake, err = decodeRef[WakePhase](l, val, "wake")
		case "docker":
			err = decodeInto(val, &p.Docker, path, "docker")
		case "secrets":
			err = decodeInto(val, &p.Secrets, path, "secrets")
		case "mask":
			err = decodeInto(val, &p.Mask, path, "mask")
		case "tasks":
			p.Tasks, err = decodeTasks(l, val, path)
		case "schedules":
			err = decodeInto(val, &p.Schedules, path, "schedules")
		default:
			return nil, fmt.Errorf("%s:%d: unknown field %q", path, key.Line, name)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := p.validate(path); err != nil {
		return nil, err
	}
	return p, nil
}

// loader resolves typed file references while parsing one document.
type loader struct {
	path  string
	stack []string
}

// resolve turns a node that is a bare string into the mapping it references,
// relative to the referring file's directory. A non-scalar node passes through
// unchanged. It returns the resolved node and the file it came from, so decode
// errors name the right file.
func (l *loader) resolve(n *yaml.Node, field string) (*yaml.Node, string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return n, l.path, nil
	}
	ref := n.Value
	full := ref
	if !filepath.IsAbs(full) {
		full = filepath.Join(filepath.Dir(l.path), ref)
	}
	full = filepath.Clean(full)
	for _, seen := range l.stack {
		if seen == full {
			return nil, "", fmt.Errorf("%s:%d: %s: reference cycle on %s", l.path, n.Line, field, ref)
		}
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, "", fmt.Errorf("%s:%d: %s: %w", l.path, n.Line, field, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, "", fmt.Errorf("%s: %s", full, yamlMessage(err))
	}
	if len(doc.Content) == 0 {
		return nil, "", fmt.Errorf("%s: %s: referenced file is empty", full, field)
	}
	inner := doc.Content[0]
	if inner.Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("%s:%d: %s: expected a mapping", full, inner.Line, field)
	}
	return inner, full, nil
}

// decodeRef resolves an inline-or-reference node and decodes it into T.
func decodeRef[T any](l *loader, n *yaml.Node, field string) (*T, error) {
	resolved, src, err := l.resolve(n, field)
	if err != nil {
		return nil, err
	}
	var out T
	if err := decodeInto(resolved, &out, src, field); err != nil {
		return nil, err
	}
	return &out, nil
}

// decodeTasks decodes the tasks mapping, resolving each task's inline or
// referenced work.
func decodeTasks(l *loader, n *yaml.Node, path string) (map[string]Task, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s:%d: tasks: expected a mapping", path, n.Line)
	}
	tasks := make(map[string]Task, len(n.Content)/2)
	for i := 0; i < len(n.Content); i += 2 {
		name := n.Content[i].Value
		if _, dup := tasks[name]; dup {
			return nil, fmt.Errorf("%s:%d: duplicate task %q", path, n.Content[i].Line, name)
		}
		t, err := decodeRef[Task](l, n.Content[i+1], "tasks."+name)
		if err != nil {
			return nil, err
		}
		tasks[name] = *t
	}
	return tasks, nil
}

// decodeInto decodes one node into out, first rejecting unknown mapping keys
// so a typo fails at load time. src labels the file for the error.
func decodeInto(n *yaml.Node, out any, src, field string) error {
	if err := checkKnown(n, reflect.TypeOf(out).Elem(), field); err != nil {
		return fmt.Errorf("%s:%d: %w", src, n.Line, err)
	}
	if err := n.Decode(out); err != nil {
		return fmt.Errorf("%s: %s: %s", src, field, yamlMessage(err))
	}
	return nil
}

// yamlUnmarshalerType is the interface a type implements to decode itself; its
// UnmarshalYAML owns key validation, so the reflection walker leaves it alone.
var yamlUnmarshalerType = reflect.TypeOf((*yaml.Unmarshaler)(nil)).Elem()

// checkKnown walks a node against a Go type and reports the first mapping key
// that has no corresponding yaml-tagged field.
func checkKnown(n *yaml.Node, t reflect.Type, field string) error {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if reflect.PtrTo(t).Implements(yamlUnmarshalerType) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		fields := yamlFields(t)
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("%s.%s: unknown field %q", field, k, k)
			}
			if err := checkKnown(n.Content[i+1], ft, field+"."+k); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := checkKnown(n.Content[i+1], t.Elem(), field+"."+n.Content[i].Value); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for _, item := range n.Content {
			if err := checkKnown(item, t.Elem(), field); err != nil {
				return err
			}
		}
	}
	return nil
}

// yamlFields maps a struct's yaml tags to their field types.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if j := strings.IndexByte(tag, ','); j >= 0 {
			tag = tag[:j]
		}
		if tag == "" || tag == "-" {
			continue
		}
		fields[tag] = f.Type
	}
	return fields
}

// yamlMessage strips yaml.v3's package prefix so errors render as
// "<file>:<line>: ...", matching the TOML contract's convention.
func yamlMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

// validate enforces the pluto.yml rules. Errors name the offending field.
func (p *Project) validate(path string) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
	}
	if p.Version != 1 {
		return fail("version: must be 1 (got %d)", p.Version)
	}
	if p.Provision != nil {
		if p.Provision.Base.Kind == BaseUnset {
			return fail("provision.base: is required")
		}
		if err := validateNames("provision.packages", p.Provision.Packages); err != nil {
			return fail("%s", err)
		}
		if err := validateNames("provision.watch", p.Provision.Watch); err != nil {
			return fail("%s", err)
		}
	}
	if p.Wake != nil && p.Wake.Run.IsZero() {
		return fail("wake.run: command is required")
	}
	for name, s := range p.Secrets {
		if !envNameRule.MatchString(name) || strings.HasPrefix(name, "PLUTO_") {
			return fail("secrets.%s: name must be a valid environment key not starting with PLUTO_", name)
		}
		switch {
		case s.Env == "" && s.File == "":
			return fail("secrets.%s: declare env or file", name)
		case s.Env != "" && s.File != "":
			return fail("secrets.%s: declare env or file, not both", name)
		}
	}
	for i, m := range p.Mask {
		if m == "" {
			return fail("mask[%d]: entries must not be empty", i)
		}
	}
	for name, task := range p.Tasks {
		if !nameRule.MatchString(name) {
			return fail("tasks.%s: name must be letters, digits, '-' or '_'", name)
		}
		if task.Run.IsZero() {
			return fail("tasks.%s.run: command is required", name)
		}
		switch task.Lock {
		case "", LockShared, LockExclusive:
		default:
			return fail("tasks.%s.lock: %q is not shared or exclusive", name, task.Lock)
		}
		if task.Service && task.Lock != "" {
			return fail("tasks.%s: a service task does not declare lock", name)
		}
	}
	for name, s := range p.Schedules {
		if !nameRule.MatchString(name) {
			return fail("schedules.%s: name must be letters, digits, '-' or '_'", name)
		}
		if s.Task == "" {
			return fail("schedules.%s.task: task is required", name)
		}
		if _, ok := p.Tasks[s.Task]; !ok {
			return fmt.Errorf("%s: schedules.%s.task: %w %q", path, name, ErrNoSuchTask, s.Task)
		}
		if s.Cron == "" {
			return fail("schedules.%s.cron: cron is required", name)
		}
		if _, err := ParseCron(s.Cron); err != nil {
			return fail("schedules.%s.cron: invalid cron %q: %v", name, s.Cron, err)
		}
	}
	return nil
}

func validateNames(field string, names []string) error {
	seen := make(map[string]bool, len(names))
	for i, name := range names {
		if name == "" {
			return fmt.Errorf("%s[%d]: entries must not be empty", field, i)
		}
		if seen[name] {
			return fmt.Errorf("%s: duplicate %q", field, name)
		}
		seen[name] = true
	}
	return nil
}

// Hash returns the fingerprint of the compiled project: the value a box
// records when it applies a contract. Inline and referenced forms that compile
// to the same model hash the same; any edit that survives parsing changes it.
func (p *Project) Hash() string {
	data, err := json.Marshal(p)
	if err != nil {
		// The model is plain strings, numbers, slices, and maps; Marshal
		// cannot fail. Silence would lose staleness tracking, so fail loudly.
		panic(fmt.Sprintf("project hash: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TaskExec resolves a declared task into an executable spec. A task declares a
// single command, which runs as one implicit step. An unknown name errors with
// ErrNoSuchTask and lists the declared tasks.
func (p *Project) TaskExec(name string) (Exec, error) {
	t, ok := p.Tasks[name]
	if !ok {
		return Exec{}, noSuchTask(p, name)
	}
	return Exec{Command: t.Run}, nil
}

// TaskLock resolves a declared task's effective lock policy: how its runs share
// the workspace's box. An unknown name errors with ErrNoSuchTask.
func (p *Project) TaskLock(name string) (Lock, error) {
	t, ok := p.Tasks[name]
	if !ok {
		return "", noSuchTask(p, name)
	}
	return t.EffectiveLock(), nil
}

// ServiceTask is a declared service task and its executable spec. A service
// task stays running for its box's life and is restarted on every start.
type ServiceTask struct {
	Name string
	Exec Exec
}

// ServiceTasks returns the declared service tasks in stable name order.
func (p *Project) ServiceTasks() []ServiceTask {
	out := make([]ServiceTask, 0)
	for _, name := range p.TaskNames() {
		t := p.Tasks[name]
		if t.EffectiveLock() == LockService {
			out = append(out, ServiceTask{Name: name, Exec: Exec{Command: t.Run}})
		}
	}
	return out
}

// noSuchTask reports an unknown task name with the declared names to choose
// from, so the error names the next step.
func noSuchTask(p *Project, name string) error {
	declared := strings.Join(p.TaskNames(), ", ")
	if declared == "" {
		return fmt.Errorf("%w %q; no tasks declared", ErrNoSuchTask, name)
	}
	return fmt.Errorf("%w %q; declared: %s", ErrNoSuchTask, name, declared)
}

// TaskNames returns declared task names in stable order.
func (p *Project) TaskNames() []string {
	names := make([]string, 0, len(p.Tasks))
	for name := range p.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ScheduleNames returns declared schedule names in stable order.
func (p *Project) ScheduleNames() []string {
	names := make([]string, 0, len(p.Schedules))
	for name := range p.Schedules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
