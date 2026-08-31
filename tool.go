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
	"fmt"

	"github.com/go-steer/antigravity-sdk-go/internal/schema"
)

// Tool is a function the agent can invoke. Implementations are usually
// produced by [NewTool] rather than written by hand.
type Tool interface {
	// Name is the identifier the model uses to call the tool. It must be
	// unique within a session.
	Name() string
	// Description tells the model what the tool does and when to use it. The
	// model relies on this heavily, so it is worth writing carefully.
	Description() string
	// ParametersSchema is a JSON Schema describing the tool's arguments.
	ParametersSchema() json.RawMessage
	// Call executes the tool. args is the model-supplied argument object,
	// encoded as JSON. The returned value is marshaled back to the model.
	Call(ctx context.Context, args json.RawMessage) (any, error)
}

// typedTool adapts a strongly typed Go function to the [Tool] interface.
type typedTool[A, R any] struct {
	name        string
	description string
	schema      json.RawMessage
	fn          func(context.Context, A) (R, error)
}

func (t *typedTool[A, R]) Name() string                      { return t.name }
func (t *typedTool[A, R]) Description() string               { return t.description }
func (t *typedTool[A, R]) ParametersSchema() json.RawMessage { return t.schema }

func (t *typedTool[A, R]) Call(ctx context.Context, raw json.RawMessage) (any, error) {
	var args A
	// An absent or null argument object is normal for a tool that takes no
	// parameters; leave the zero value in place rather than failing.
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("tool %q: decoding arguments: %w", t.name, err)
		}
	}
	return t.fn(ctx, args)
}

// NewTool builds a [Tool] from a typed Go function.
//
// The argument type A must be a struct; its exported fields become the tool's
// parameters. Field names, types, and requiredness are derived by reflection,
// honoring `json` tags for naming and `jsonschema` tags for descriptions and
// constraints:
//
//	type WeatherArgs struct {
//		City  string `json:"city" jsonschema:"description=The city to look up"`
//		Units string `json:"units,omitempty" jsonschema:"enum=celsius,enum=fahrenheit"`
//	}
//
//	tool, err := antigravity.NewTool("get_weather",
//		"Returns the current weather for a city.",
//		func(ctx context.Context, a WeatherArgs) (string, error) {
//			return fmt.Sprintf("It's sunny in %s.", a.City), nil
//		})
//
// Unlike the Python SDK, which reads parameter descriptions from docstrings,
// Go has no runtime access to comments, so descriptions come from struct tags.
//
// Use a struct with no fields for a tool that takes no arguments.
func NewTool[A, R any](name, description string, fn func(context.Context, A) (R, error)) (Tool, error) {
	if name == "" {
		return nil, fmt.Errorf("tool name must not be empty")
	}
	if fn == nil {
		return nil, fmt.Errorf("tool %q: function must not be nil", name)
	}
	var zero A
	s, err := schema.For(zero)
	if err != nil {
		return nil, fmt.Errorf("tool %q: building parameter schema: %w", name, err)
	}
	return &typedTool[A, R]{
		name:        name,
		description: description,
		schema:      s,
		fn:          fn,
	}, nil
}

// MustNewTool is [NewTool] but panics on error. It is intended for package
// level tool declarations, where a schema failure is a programming error that
// should surface immediately.
func MustNewTool[A, R any](name, description string, fn func(context.Context, A) (R, error)) Tool {
	t, err := NewTool(name, description, fn)
	if err != nil {
		panic(err)
	}
	return t
}

// ToolCall is a request from the model to invoke a tool.
type ToolCall struct {
	// Name identifies the tool. It is a [BuiltinTool] value for
	// harness-provided tools, or an arbitrary string for custom and MCP tools.
	Name string
	// Args are the model-supplied arguments, as a JSON object.
	Args json.RawMessage
	// ID uniquely identifies the call, assigned by the backend.
	ID string
	// StepID correlates the call with its step in the trajectory.
	StepID string
	// CanonicalPath is the normalized filesystem path for file-related tools.
	// The connection layer populates it from the wire URI so that policies can
	// match on a platform-native absolute path.
	CanonicalPath string
	// ServerName names the MCP server owning this tool, when applicable.
	ServerName string
}

// UnmarshalArgs decodes the call's arguments into v.
func (c *ToolCall) UnmarshalArgs(v any) error {
	if len(c.Args) == 0 {
		return nil
	}
	return json.Unmarshal(c.Args, v)
}

// ToolResult is the outcome of a single tool execution.
type ToolResult struct {
	// Name is the tool that ran.
	Name string
	// ID correlates the result with a [ToolCall].
	ID string
	// StepID correlates the result with a step.
	StepID string
	// Result is the tool's return value, which must be JSON-serializable.
	Result any
	// Err is the failure, or nil on success.
	Err error
	// ServerName names the MCP server owning this tool, when applicable.
	ServerName string
}
