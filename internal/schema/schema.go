// Package schema derives the editor-facing JSON Schema for the box contract
// from the contract's Go types, so the parser and the schema cannot drift
// (ADR 0007). JSON describes the legacy .pluto.toml; YAML describes pluto.yml
// and its typed file references. The artifacts are generated, not hand-written;
// run 'go generate ./...' after changing internal/contract.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Siddhj2206/pluto/internal/contract"
)

// URL is where the committed artifact lives; editors point at it with a
// '#:schema' comment in .pluto.toml.
const URL = "https://raw.githubusercontent.com/Siddhj2206/pluto/main/pluto.schema.json"

// YAMLURL is where the committed pluto.yml schema lives; editors point at it
// with a 'yaml-language-server: $schema' comment.
const YAMLURL = "https://raw.githubusercontent.com/Siddhj2206/pluto/main/pluto.yml.schema.json"

var (
	commandType = reflect.TypeOf(contract.Command{})
	packageType = reflect.TypeOf(contract.Package{})

	baseType      = reflect.TypeOf(contract.Base{})
	secretType    = reflect.TypeOf(contract.Secret{})
	dockerType    = reflect.TypeOf(contract.Docker{})
	provisionType = reflect.TypeOf(contract.Provision{})
	wakeType      = reflect.TypeOf(contract.WakePhase{})
	taskType      = reflect.TypeOf(contract.Task{})
)

// JSON generates the JSON Schema for the box contract.
func JSON() ([]byte, error) {
	root, err := schemaFor(reflect.TypeOf(contract.Contract{}))
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         URL,
		"title":       "pluto box contract",
		"description": "A .pluto.toml: the box's image and resources, its tools, provision and wake phases, services, named jobs, sessions, and schedules (ADR 0007).",
		"$comment":    "Generated from internal/contract; run 'go generate ./...' after changing the Go types.",
	}
	for key, value := range root.(map[string]any) {
		doc[key] = value
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// schemaFor describes one Go type in JSON Schema terms. The contract's types
// use only structs, pointers, slices, string maps, strings, and ints.
func schemaFor(t reflect.Type) (any, error) {
	if t == commandType {
		return map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string", "minLength": 1},
				map[string]any{
					"type":     "array",
					"items":    map[string]any{"type": "string"},
					"minItems": 1,
				},
			},
		}, nil
	}
	if t == packageType {
		return map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string", "minLength": 1},
				map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"name":    map[string]any{"type": "string", "minLength": 1},
						"version": map[string]any{"type": "string", "minLength": 1},
					},
					"required": []string{"name"},
				},
			},
		}, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaFor(t.Elem())
	case reflect.Struct:
		properties := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name, ok := tomlName(field)
			if !ok {
				continue
			}
			fieldSchema, err := schemaFor(field.Type)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", t.Name(), field.Name, err)
			}
			properties[name] = fieldSchema
			if field.Tag.Get("schema") == "required" {
				required = append(required, name)
			}
		}
		object := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           properties,
		}
		if len(required) > 0 {
			sort.Strings(required)
			object["required"] = required
		}
		return object, nil
	case reflect.Slice:
		items, err := schemaFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("unsupported map key type %s", t.Key())
		}
		values, err := schemaFor(t.Elem())
		if err != nil {
			return nil, err
		}
		object := map[string]any{"type": "object", "additionalProperties": values}
		// String maps are env tables; PLUTO_ is reserved (ADR 0007).
		if t.Elem().Kind() == reflect.String {
			object["propertyNames"] = map[string]any{
				"not": map[string]any{"pattern": "^PLUTO_"},
			}
		}
		return object, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int:
		return map[string]any{"type": "integer"}, nil
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

// tomlName returns a field's schema property name; fields without a toml tag
// are skipped.
func tomlName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("toml")
	if tag == "" || tag == "-" {
		return "", false
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	return tag, tag != ""
}

// YAML generates the JSON Schema for the pluto.yml box contract. Every node
// that may be written inline or reference a typed YAML file is a oneOf of a
// non-empty string (the reference path) and the node's inline object, so one
// schema covers all accepted forms.
func YAML() ([]byte, error) {
	root, err := yamlSchemaFor(reflect.TypeOf(contract.Project{}))
	if err != nil {
		return nil, err
	}
	doc := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         YAMLURL,
		"title":       "pluto box contract",
		"description": "A pluto.yml: the box's provision and wake phases, container-engine capability, secrets and masking, named tasks, and schedules. Any node may be inline or a path to a typed YAML file with the same shape.",
		"$comment":    "Generated from internal/contract; run 'go generate ./...' after changing the Go types.",
	}
	for key, value := range root.(map[string]any) {
		doc[key] = value
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// yamlSchemaFor describes one Go type in pluto.yml's JSON Schema terms. Types
// that may be a typed file reference return the reference-or-inline union.
func yamlSchemaFor(t reflect.Type) (any, error) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch {
	case t == commandType:
		return commandSchema(), nil
	case t == baseType:
		return baseSchema(), nil
	case t == secretType:
		return secretSchema(), nil
	case t == dockerType:
		return dockerSchema(), nil
	case t == provisionType, t == wakeType, t == taskType:
		obj, err := yamlObjectFor(t)
		if err != nil {
			return nil, err
		}
		return map[string]any{"oneOf": []any{referenceSchema(), obj}}, nil
	}
	return yamlObjectFor(t)
}

// yamlObjectFor describes a type inline: structs as closed objects with their
// yaml-tagged fields, maps by their value schema, slices as arrays, and
// scalars as their primitive type.
func yamlObjectFor(t reflect.Type) (any, error) {
	switch t.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name, ok := yamlName(field)
			if !ok {
				continue
			}
			fieldSchema, err := yamlSchemaFor(field.Type)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", t.Name(), field.Name, err)
			}
			properties[name] = fieldSchema
			if field.Tag.Get("schema") == "required" {
				required = append(required, name)
			}
		}
		object := map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           properties,
		}
		if len(required) > 0 {
			sort.Strings(required)
			object["required"] = required
		}
		return object, nil
	case reflect.Map:
		values, err := yamlSchemaFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Slice:
		items, err := yamlSchemaFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int:
		return map[string]any{"type": "integer"}, nil
	default:
		return nil, fmt.Errorf("unsupported type %s", t)
	}
}

// commandSchema is the string-or-argv command shape shared with the TOML
// contract.
func commandSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string", "minLength": 1},
			map[string]any{
				"type":     "array",
				"items":    map[string]any{"type": "string"},
				"minItems": 1,
			},
		},
	}
}

// referenceSchema is a typed file reference: a non-empty path.
func referenceSchema() map[string]any {
	return map[string]any{"type": "string", "minLength": 1}
}

// baseSchema is provision.base: an OCI reference, {dockerfile}, or {run}.
func baseSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string", "minLength": 1},
			map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"dockerfile": map[string]any{"type": "string", "minLength": 1},
					"run":        commandSchema(),
				},
				"oneOf": []any{
					map[string]any{"required": []string{"dockerfile"}},
					map[string]any{"required": []string{"run"}},
				},
			},
		},
	}
}

// dockerSchema is the box's in-box container-engine capability: a boolean
// (no engine, or the rootless engine) or a {privileged} mapping that opts into
// the escalation for full in-box builds.
func dockerSchema() map[string]any {
	return map[string]any{
		"oneOf": []any{
			map[string]any{"type": "boolean"},
			map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"privileged": map[string]any{"type": "boolean"},
				},
				"required": []string{"privileged"},
			},
		},
	}
}

// secretSchema is one declared secret: a host env var or a file, never both.
func secretSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"env":  map[string]any{"type": "string", "minLength": 1},
			"file": map[string]any{"type": "string", "minLength": 1},
		},
		"oneOf": []any{
			map[string]any{"required": []string{"env"}},
			map[string]any{"required": []string{"file"}},
		},
	}
}

// yamlName returns a field's schema property name; fields without a yaml tag
// are skipped.
func yamlName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("yaml")
	if tag == "" || tag == "-" {
		return "", false
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	return tag, tag != ""
}
