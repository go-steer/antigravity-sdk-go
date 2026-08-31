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

package antigravity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type weatherArgs struct {
	City string `json:"city" jsonschema:"description=The city to look up"`
}

func TestNewTool(t *testing.T) {
	tool, err := NewTool("get_weather", "Looks up the weather.",
		func(_ context.Context, a weatherArgs) (string, error) { return "sunny in " + a.City, nil })
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	if tool.Name() != "get_weather" || tool.Description() != "Looks up the weather." {
		t.Errorf("tool = %q / %q", tool.Name(), tool.Description())
	}
	// The schema is what the model sees, so the field name and its tag
	// description both have to be in it.
	schema := string(tool.ParametersSchema())
	if !strings.Contains(schema, `"city"`) || !strings.Contains(schema, "The city to look up") {
		t.Errorf("schema = %s, want the tagged parameter", schema)
	}

	got, err := tool.Call(t.Context(), json.RawMessage(`{"city":"Lisbon"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got != "sunny in Lisbon" {
		t.Errorf("Call = %v", got)
	}
}

func TestToolCallWithoutArguments(t *testing.T) {
	// A tool taking no parameters is called with nothing, or with a literal
	// null, depending on the model. Neither is an error.
	tool, err := NewTool("ping", "Answers.",
		func(context.Context, struct{}) (string, error) { return "pong", nil })
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	for _, args := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("{}")} {
		got, err := tool.Call(t.Context(), args)
		if err != nil {
			t.Fatalf("Call(%s): %v", args, err)
		}
		if got != "pong" {
			t.Errorf("Call(%s) = %v", args, got)
		}
	}
}

func TestToolCallRejectsMalformedArguments(t *testing.T) {
	tool, err := NewTool("get_weather", "Looks up the weather.",
		func(context.Context, weatherArgs) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	_, err = tool.Call(t.Context(), json.RawMessage(`{"city":42}`))
	if err == nil {
		t.Fatal("a malformed argument object was accepted")
	}
	// The error goes back to the model, so it has to say which tool failed.
	if !strings.Contains(err.Error(), `"get_weather"`) {
		t.Errorf("error = %v, want the tool named", err)
	}
}

func TestNewToolRejectsBadDeclarations(t *testing.T) {
	if _, err := NewTool("", "", func(context.Context, struct{}) (string, error) { return "", nil }); err == nil {
		t.Error("a tool with no name was accepted")
	}
	if _, err := NewTool[struct{}, string]("named", "", nil); err == nil {
		t.Error("a tool with no function was accepted")
	}
	// A type with no JSON Schema representation cannot describe parameters.
	if _, err := NewTool("chan", "", func(context.Context, chan int) (string, error) { return "", nil }); err == nil {
		t.Error("a tool with an unrepresentable argument type was accepted")
	}
}

func TestMustNewTool(t *testing.T) {
	tool := MustNewTool("ping", "Answers.",
		func(context.Context, struct{}) (string, error) { return "pong", nil })
	if tool.Name() != "ping" {
		t.Errorf("Name = %q", tool.Name())
	}
}

func TestMustNewToolPanics(t *testing.T) {
	// Package level declarations have nowhere to return an error to, so a bad
	// one has to fail loudly at init instead of yielding a broken tool.
	defer func() {
		if recover() == nil {
			t.Error("MustNewTool returned normally for an unrepresentable argument type")
		}
	}()

	MustNewTool("chan", "", func(context.Context, chan int) (string, error) { return "", nil })
}

func TestToolCallUnmarshalArgs(t *testing.T) {
	call := ToolCall{Name: "get_weather", Args: json.RawMessage(`{"city":"Lisbon"}`)}

	var args weatherArgs
	if err := call.UnmarshalArgs(&args); err != nil {
		t.Fatalf("UnmarshalArgs: %v", err)
	}
	if args.City != "Lisbon" {
		t.Errorf("args = %+v", args)
	}

	// A call with no arguments leaves the target alone rather than failing on
	// empty input.
	before := weatherArgs{City: "untouched"}
	if err := (&ToolCall{}).UnmarshalArgs(&before); err != nil {
		t.Fatalf("UnmarshalArgs on an empty call: %v", err)
	}
	if before.City != "untouched" {
		t.Errorf("args = %+v, want them left alone", before)
	}

	if err := (&ToolCall{Args: json.RawMessage(`{`)}).UnmarshalArgs(&args); err == nil {
		t.Error("malformed arguments decoded without error")
	}
}
