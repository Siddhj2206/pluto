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
