package schema_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Siddhj2206/pluto/internal/schema"
)

// at walks a decoded JSON document by key.
func at(t *testing.T, v any, path ...string) any {
	t.Helper()
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("at %v: %T is not an object", path, v)
		}
		v, ok = m[key]
		if !ok {
			t.Fatalf("at %v: missing key %q", path, key)
		}
	}
	return v
}

func generated(t *testing.T) map[string]any {
	t.Helper()
	data, err := schema.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("generated schema is not JSON: %v", err)
	}
	return doc
}

func TestGeneratedSchemaDescribesTheContract(t *testing.T) {
	doc := generated(t)

	if doc["$schema"] == nil || doc["title"] == nil {
		t.Fatalf("schema = %v, want $schema and title", doc)
	}
	if got := doc["additionalProperties"]; got != false {
		t.Fatalf("additionalProperties = %v, want false (unknown keys are parse errors)", got)
	}
	for _, section := range []string{"box", "env", "provision", "wake", "services", "jobs", "sessions", "schedule"} {
		if _, ok := doc["properties"].(map[string]any)[section]; !ok {
			t.Fatalf("properties missing %q", section)
		}
	}

	// command is either a shell string or an argv array.
	oneOf, ok := at(t, doc, "properties", "jobs", "additionalProperties", "properties", "command", "oneOf").([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("jobs command schema = %v, want a string/array oneOf", oneOf)
	}

	// env is a flat map of strings with the PLUTO_ namespace reserved.
	if got := at(t, doc, "properties", "env", "additionalProperties", "type"); got != "string" {
		t.Fatalf("env value type = %v, want string", got)
	}
	if got := at(t, doc, "properties", "env", "propertyNames", "not", "pattern"); got != "^PLUTO_" {
		t.Fatalf("env propertyNames = %v, want the PLUTO_ reservation", got)
	}

	// A schedule declares name and cron; job is optional (a warm-up).
	required, ok := at(t, doc, "properties", "schedule", "items", "required").([]any)
	if !ok {
		t.Fatal("schedule items should list required fields")
	}
	hasName, hasCron, hasJob := false, false, false
	for _, r := range required {
		switch r {
		case "name":
			hasName = true
		case "cron":
			hasCron = true
		case "job":
			hasJob = true
		}
	}
	if !hasName || !hasCron {
		t.Fatalf("schedule required = %v, want name and cron", required)
	}
	if hasJob {
		t.Fatalf("schedule required = %v; job must stay optional (warm-up)", required)
	}
}

// [tools] packages is a list of apt names or { name, version } tables.
func TestGeneratedSchemaDescribesTools(t *testing.T) {
	doc := generated(t)
	if _, ok := doc["properties"].(map[string]any)["tools"]; !ok {
		t.Fatal("properties missing tools")
	}
	required, ok := at(t, doc, "properties", "tools", "required").([]any)
	if !ok || len(required) != 1 || required[0] != "packages" {
		t.Fatalf("tools required = %v, want [packages]", required)
	}
	oneOf, ok := at(t, doc, "properties", "tools", "properties", "packages", "items", "oneOf").([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("tools packages schema = %v, want a string/table oneOf", oneOf)
	}
	bare, _ := oneOf[0].(map[string]any)
	if bare["type"] != "string" || bare["minLength"] != float64(1) {
		t.Fatalf("bare package schema = %v, want a non-empty string", bare)
	}
	pinned, _ := oneOf[1].(map[string]any)
	if pinned["type"] != "object" || pinned["additionalProperties"] != false {
		t.Fatalf("pinned package schema = %v, want a closed object", pinned)
	}
	preq, _ := pinned["required"].([]any)
	if len(preq) != 1 || preq[0] != "name" {
		t.Fatalf("pinned package required = %v, want [name]", preq)
	}
}

func TestGeneratedSchemaRequiresCommands(t *testing.T) {
	doc := generated(t)
	for _, section := range []string{"provision", "wake"} {
		required, _ := at(t, doc, "properties", section, "required").([]any)
		if len(required) != 1 || required[0] != "command" {
			t.Fatalf("%s required = %v, want [command]", section, required)
		}
	}
	for _, section := range []string{"jobs", "services", "sessions"} {
		required, _ := at(t, doc, "properties", section, "additionalProperties", "required").([]any)
		if len(required) != 1 || required[0] != "command" {
			t.Fatalf("%s entries required = %v, want [command]", section, required)
		}
	}
}

func TestCommittedSchemaIsCurrent(t *testing.T) {
	want, err := schema.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	path := filepath.Join("..", "..", "pluto.schema.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed schema: %v (run 'go generate ./...')", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale; run 'go generate ./...' and commit the result", path)
	}
}

// generatedYAML decodes the pluto.yml schema.
func generatedYAML(t *testing.T) map[string]any {
	t.Helper()
	data, err := schema.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("generated yaml schema is not JSON: %v", err)
	}
	return doc
}

// inlineOrRef reports whether a schema accepts a node inline (object) or as a
// typed file reference (a non-empty string).
func inlineOrRef(t *testing.T, v any) (obj map[string]any) {
	t.Helper()
	oneOf, ok := v.(map[string]any)["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("schema = %v, want a string/object oneOf", v)
	}
	ref, _ := oneOf[0].(map[string]any)
	if ref["type"] != "string" || ref["minLength"] != float64(1) {
		t.Fatalf("reference option = %v, want a non-empty string", ref)
	}
	obj, _ = oneOf[1].(map[string]any)
	if obj["type"] != "object" {
		t.Fatalf("inline option = %v, want an object", obj)
	}
	return obj
}

// pluto.yml's schema describes the project contract, and every node that can
// be inline or a typed file reference says so: one schema validates all forms.
func TestGeneratedYAMLSchemaDescribesTheProject(t *testing.T) {
	doc := generatedYAML(t)
	props := doc["properties"].(map[string]any)
	for _, section := range []string{"version", "provision", "wake", "docker", "secrets", "mask", "tasks", "schedules"} {
		if _, ok := props[section]; !ok {
			t.Fatalf("properties missing %q", section)
		}
	}

	// provision and wake: a complex node, inline or a file reference.
	provision := inlineOrRef(t, props["provision"])
	inlineOrRef(t, props["wake"])

	// provision.base: an OCI reference, a Dockerfile, or inline steps.
	base := inlineOrRef(t, at(t, provision, "properties", "base"))
	bprops := base["properties"].(map[string]any)
	if bprops["dockerfile"] == nil || bprops["run"] == nil {
		t.Fatalf("base properties = %v, want dockerfile and run", bprops)
	}
	if base["additionalProperties"] != false {
		t.Fatalf("base additionalProperties = %v, want false", base["additionalProperties"])
	}

	// A task's work may live inline or in its own file.
	task := inlineOrRef(t, at(t, doc, "properties", "tasks", "additionalProperties"))
	run, _ := at(t, task, "properties", "run").(map[string]any)
	if oneOf, ok := run["oneOf"].([]any); !ok || len(oneOf) != 2 {
		t.Fatalf("task run schema = %v, want a string/array oneOf", run)
	}
	lock, _ := at(t, task, "properties", "lock").(map[string]any)
	if lock["type"] != "string" {
		t.Fatalf("task lock schema = %v, want a string", lock)
	}

	// A secret names a host env var or a file, never both.
	secret, _ := at(t, doc, "properties", "secrets", "additionalProperties").(map[string]any)
	if secret["additionalProperties"] != false {
		t.Fatalf("secret additionalProperties = %v, want false", secret["additionalProperties"])
	}
	for _, key := range []string{"env", "file"} {
		if _, ok := at(t, secret, "properties", key).(map[string]any); !ok {
			t.Fatalf("secret properties missing %q", key)
		}
	}

	// A schedule names a task and a cron expression.
	required, _ := at(t, doc, "properties", "schedules", "additionalProperties", "required").([]any)
	if len(required) != 2 {
		t.Fatalf("schedule required = %v, want task and cron", required)
	}

	// docker: a boolean (no engine or the rootless engine) or a {privileged}
	// mapping (the opt-in escalation).
	docker, _ := props["docker"].(map[string]any)
	opts, _ := docker["oneOf"].([]any)
	if len(opts) != 2 {
		t.Fatalf("docker schema = %v, want a boolean/mapping oneOf", docker)
	}
	if opts[0].(map[string]any)["type"] != "boolean" {
		t.Fatalf("docker option 0 = %v, want boolean", opts[0])
	}
	if _, ok := at(t, opts[1].(map[string]any), "properties", "privileged").(map[string]any); !ok {
		t.Fatalf("docker mapping = %v, want a privileged property", opts[1])
	}
}

func TestCommittedYAMLSchemaIsCurrent(t *testing.T) {
	want, err := schema.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	path := filepath.Join("..", "..", "pluto.yml.schema.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed yaml schema: %v (run 'go generate ./...')", err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s is stale; run 'go generate ./...' and commit the result", path)
	}
}
