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

package schema

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestFor(t *testing.T) {
	type Nested struct {
		Depth int `json:"depth"`
	}
	type Args struct {
		City    string            `json:"city" jsonschema:"description=The city to look up"`
		Units   string            `json:"units,omitempty" jsonschema:"enum=celsius,enum=fahrenheit"`
		Days    int               `json:"days"`
		Ratio   float64           `json:"ratio"`
		Verbose bool              `json:"verbose,omitempty"`
		Tags    []string          `json:"tags,omitempty"`
		Meta    map[string]string `json:"meta,omitempty"`
		Opt     *Nested           `json:"opt,omitempty"`
		Blob    []byte            `json:"blob,omitempty"`
		Hidden  string            `json:"-"`
		//nolint:unused // Present precisely to prove unexported fields are skipped.
		private string
	}

	raw, err := For(Args{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	var got Schema
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Type != "object" {
		t.Errorf("Type = %q, want object", got.Type)
	}

	tests := []struct {
		prop     string
		wantType string
	}{
		{"city", "string"},
		{"units", "string"},
		{"days", "integer"},
		{"ratio", "number"},
		{"verbose", "boolean"},
		{"tags", "array"},
		{"meta", "object"},
		{"opt", "object"},
		{"blob", "string"},
	}
	for _, tc := range tests {
		p, ok := got.Properties[tc.prop]
		if !ok {
			t.Errorf("missing property %q", tc.prop)
			continue
		}
		if p.Type != tc.wantType {
			t.Errorf("%s.Type = %q, want %q", tc.prop, p.Type, tc.wantType)
		}
	}

	if _, ok := got.Properties["Hidden"]; ok {
		t.Error(`json:"-" field was included`)
	}
	if _, ok := got.Properties["private"]; ok {
		t.Error("unexported field was included")
	}

	if d := got.Properties["city"].Description; d != "The city to look up" {
		t.Errorf("city description = %q", d)
	}
	if e := got.Properties["units"].Enum; len(e) != 2 || e[0] != "celsius" || e[1] != "fahrenheit" {
		t.Errorf("units enum = %v, want [celsius fahrenheit]", e)
	}
	if f := got.Properties["blob"].Format; f != "byte" {
		t.Errorf("blob format = %q, want byte", f)
	}
	if it := got.Properties["tags"].Items; it == nil || it.Type != "string" {
		t.Errorf("tags items = %+v, want string", it)
	}

	// Required covers exactly the fields that are neither omitempty nor
	// pointers.
	wantRequired := map[string]bool{"city": true, "days": true, "ratio": true}
	if len(got.Required) != len(wantRequired) {
		t.Errorf("Required = %v, want keys %v", got.Required, wantRequired)
	}
	for _, r := range got.Required {
		if !wantRequired[r] {
			t.Errorf("unexpected required field %q", r)
		}
	}
}

func TestForEmptyStruct(t *testing.T) {
	type NoArgs struct{}
	raw, err := For(NoArgs{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	var got Schema
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "object" {
		t.Errorf("Type = %q, want object", got.Type)
	}
	if len(got.Required) != 0 {
		t.Errorf("Required = %v, want empty", got.Required)
	}
}

// A self-referential type must terminate rather than recurse forever.
func TestForRecursiveType(t *testing.T) {
	type Node struct {
		Name     string  `json:"name"`
		Children []*Node `json:"children,omitempty"`
	}
	if _, err := For(Node{}); err != nil {
		t.Fatalf("For: %v", err)
	}
}

func TestForEmbeddedStruct(t *testing.T) {
	type Base struct {
		ID string `json:"id"`
	}
	type Derived struct {
		Base
		Extra string `json:"extra"`
	}
	raw, err := For(Derived{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	var got Schema
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, want := range []string{"id", "extra"} {
		if _, ok := got.Properties[want]; !ok {
			t.Errorf("missing flattened property %q", want)
		}
	}
}

func TestForUnsupportedMapKey(t *testing.T) {
	type Bad struct {
		M map[int]string `json:"m"`
	}
	if _, err := For(Bad{}); err == nil {
		t.Fatal("expected an error for non-string map keys")
	}
}

func TestApplyTag(t *testing.T) {
	tests := []struct {
		name string
		tag  string
		want Schema
	}{
		{"nothing", "", Schema{}},
		// A bare word is not a setting, and must not be mistaken for one.
		{"a valueless part", "required", Schema{}},
		{"an unknown key", "minimum=3", Schema{}},
		{"a format", "format=date-time", Schema{Format: "date-time"}},
		{"enums accumulate", "enum=a,enum=b", Schema{Enum: []string{"a", "b"}}},
		// Splitting on commas breaks a prose description apart, so the pieces
		// are put back together.
		{
			"a description with commas",
			"description=City, state, and country",
			Schema{Description: "City, state, and country"},
		},
		// A trailing description swallows the rest of the tag, which is what
		// the directive syntax promises for text that needs commas.
		{
			"a description after another directive",
			"format=date-time,description=When, in UTC",
			Schema{Format: "date-time", Description: "When, in UTC"},
		},
		{
			"a description containing an equals sign",
			"description=Rate, in kg=1000g",
			Schema{Description: "Rate, in kg=1000g"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Schema
			applyTag(&got, tt.tag)

			if got.Description != tt.want.Description {
				t.Errorf("Description = %q, want %q", got.Description, tt.want.Description)
			}
			if got.Format != tt.want.Format {
				t.Errorf("Format = %q, want %q", got.Format, tt.want.Format)
			}
			if !slices.Equal(got.Enum, tt.want.Enum) {
				t.Errorf("Enum = %v, want %v", got.Enum, tt.want.Enum)
			}
		})
	}
}

func TestBuildRejectsAnUnrepresentableType(t *testing.T) {
	// A channel has no JSON form, and saying so at schema time is what keeps a
	// broken tool from reaching the model.
	if _, err := Build(reflect.TypeOf(make(chan int))); err == nil {
		t.Fatal("a channel was given a schema")
	}
}

func TestForAnyField(t *testing.T) {
	// An `any` accepts any JSON value, which an empty schema expresses.
	type payload struct {
		Value any `json:"value"`
	}

	got, err := Build(reflect.TypeOf(payload{}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	value := got.Properties["value"]
	if value == nil {
		t.Fatal("the any field is missing from the schema")
	}
	if value.Type != "" || value.Properties != nil {
		t.Errorf("value schema = %+v, want an empty one", value)
	}
}
