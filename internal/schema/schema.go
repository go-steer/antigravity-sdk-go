// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package schema derives JSON Schema documents from Go types by reflection.
//
// It exists to give custom tools a parameter schema without making callers
// hand-write JSON. The Python SDK reads parameter descriptions from function
// docstrings; Go has no runtime access to comments, so descriptions come from
// `jsonschema` struct tags instead.
//
// The output targets the subset of JSON Schema that the Gemini function
// calling API accepts: object types with named properties, scalars, arrays,
// enums, and nested objects.
package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Schema is a JSON Schema document. Field order in the marshaled output
// follows the struct definition below, which keeps generated schemas stable
// and diffable.
type Schema struct {
	Type        string             `json:"type,omitempty"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Items       *Schema            `json:"items,omitempty"`
	Enum        []string           `json:"enum,omitempty"`
	Format      string             `json:"format,omitempty"`

	// AdditionalProperties describes values of a map type. It is left nil for
	// structs, where the property set is closed.
	AdditionalProperties *Schema `json:"additionalProperties,omitempty"`
}

// For returns the JSON Schema for the dynamic type of v, marshaled to JSON.
//
// v is only used for its type; its value is ignored, so a zero value is fine.
func For(v any) (json.RawMessage, error) {
	s, err := Build(reflect.TypeOf(v))
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// Build returns the JSON Schema for t.
//
// A nil type, which arises from an interface holding no value, yields an empty
// object schema rather than an error, so that a tool taking no arguments works
// without special-casing.
func Build(t reflect.Type) (*Schema, error) {
	if t == nil {
		return &Schema{Type: "object", Properties: map[string]*Schema{}}, nil
	}
	// seen guards against infinite recursion on self-referential types.
	return build(t, map[reflect.Type]bool{})
}

func build(t reflect.Type, seen map[reflect.Type]bool) (*Schema, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}, nil

	case reflect.Bool:
		return &Schema{Type: "boolean"}, nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}, nil

	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}, nil

	case reflect.Slice, reflect.Array:
		// []byte is conventionally base64-encoded JSON string, not an array.
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return &Schema{Type: "string", Format: "byte"}, nil
		}
		items, err := build(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "array", Items: items}, nil

	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map keys must be strings, got %s", t.Key())
		}
		values, err := build(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return &Schema{Type: "object", AdditionalProperties: values}, nil

	case reflect.Struct:
		return buildStruct(t, seen)

	case reflect.Interface:
		// An `any` field accepts any JSON value; an empty schema says so.
		return &Schema{}, nil

	default:
		return nil, fmt.Errorf("unsupported type %s for JSON Schema generation", t)
	}
}

func buildStruct(t reflect.Type, seen map[reflect.Type]bool) (*Schema, error) {
	if seen[t] {
		// Break the cycle with an open object rather than recursing forever.
		return &Schema{Type: "object"}, nil
	}
	seen[t] = true
	defer delete(seen, t)

	out := &Schema{Type: "object", Properties: map[string]*Schema{}}

	for i := range t.NumField() {
		f := t.Field(i)

		// Embedded structs are flattened, matching encoding/json.
		if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
			embedded, err := buildStruct(f.Type, seen)
			if err != nil {
				return nil, err
			}
			for name, prop := range embedded.Properties {
				out.Properties[name] = prop
			}
			out.Required = append(out.Required, embedded.Required...)
			continue
		}

		if !f.IsExported() {
			continue
		}

		name, omitempty, skip := jsonName(f)
		if skip {
			continue
		}

		prop, err := build(f.Type, seen)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		applyTag(prop, f.Tag.Get("jsonschema"))
		out.Properties[name] = prop

		// A field is required unless it opts out via omitempty or a pointer
		// type, both of which signal that absence is meaningful.
		if !omitempty && f.Type.Kind() != reflect.Pointer {
			out.Required = append(out.Required, name)
		}
	}

	return out, nil
}

// jsonName resolves a field's wire name from its json tag, reporting whether
// omitempty was set and whether the field is skipped entirely.
func jsonName(f reflect.StructField) (name string, omitempty, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name = f.Name
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

// applyTag layers `jsonschema` tag directives onto a property schema.
//
// Supported directives are `description=...`, `enum=...` which may repeat,
// `format=...`, and a bare `required`. Commas inside a description are not
// escapable; use a single description directive at the end if the text needs
// them.
func applyTag(s *Schema, tag string) {
	if tag == "" {
		return
	}
	// inDescription tracks whether the previous part set the description, so a
	// comma in prose splits off a fragment that is joined back on rather than
	// dropped.
	inDescription := false

	for _, part := range strings.Split(tag, ",") {
		key, value, hasValue := strings.Cut(part, "=")
		key = strings.TrimSpace(key)

		if !hasValue || !isDirective(key) {
			if inDescription {
				s.Description += "," + part
			}
			continue
		}

		switch key {
		case "description":
			if s.Description == "" {
				s.Description = value
			} else {
				s.Description += "," + part
			}
			inDescription = true
		case "enum":
			s.Enum = append(s.Enum, value)
			inDescription = false
		case "format":
			s.Format = value
			inDescription = false
		}
	}
}

// isDirective reports whether key names a supported directive. Anything else
// is text, which matters because a description may contain an equals sign.
func isDirective(key string) bool {
	switch key {
	case "description", "enum", "format":
		return true
	default:
		return false
	}
}
