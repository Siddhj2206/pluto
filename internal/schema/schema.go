// Package schema derives the editor-facing JSON Schema for .pluto.toml from
// the contract's Go types, so the parser and the schema cannot drift
// (ADR 0007). The artifact is generated, not hand-written; run
// 'go generate ./...' after changing internal/contract.
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

var commandType = reflect.TypeOf(contract.Command{})

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
		"description": "A .pluto.toml: the box's image and resources, its provision and wake phases, services, named jobs, and schedules (ADR 0007).",
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
