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
	"slices"
	"testing"
)

func TestBuiltinToolSets(t *testing.T) {
	all := AllTools()

	// Every named set is a subset of AllTools, so a tool added to the harness
	// cannot be referenced by a preset without also being declared.
	for _, tt := range []struct {
		name string
		set  []BuiltinTool
	}{
		{"ReadOnlyTools", ReadOnlyTools()},
		{"NondestructiveTools", NondestructiveTools()},
		{"FileTools", FileTools()},
	} {
		for _, tool := range tt.set {
			if !slices.Contains(all, tool) {
				t.Errorf("%s includes %q, which is not a builtin tool", tt.name, tool)
			}
		}
	}

	if len(all) != len(slices.Compact(slices.Sorted(slices.Values(all)))) {
		t.Errorf("AllTools = %v, want no duplicates", all)
	}
}

func TestNondestructiveTools(t *testing.T) {
	nondestructive := NondestructiveTools()

	// The distinguishing property: files may be created and edited, but no
	// arbitrary command may run.
	if slices.Contains(nondestructive, ToolRunCommand) {
		t.Error("NondestructiveTools permits run_command")
	}
	for _, tool := range []BuiltinTool{ToolCreateFile, ToolEditFile} {
		if !slices.Contains(nondestructive, tool) {
			t.Errorf("NondestructiveTools omits %q", tool)
		}
	}

	// It is strictly broader than the read-only set.
	for _, tool := range ReadOnlyTools() {
		if !slices.Contains(nondestructive, tool) {
			t.Errorf("NondestructiveTools omits the read-only tool %q", tool)
		}
	}
}

func TestBuiltinToolIsWrite(t *testing.T) {
	// isWrite is what decides whether a configuration needs supervision, so the
	// read-only set has to be exactly the tools it clears.
	for _, tool := range ReadOnlyTools() {
		if tool.isWrite() {
			t.Errorf("%q is treated as a write tool but is in ReadOnlyTools", tool)
		}
	}
	for _, tool := range []BuiltinTool{ToolRunCommand, ToolCreateFile, ToolEditFile} {
		if !tool.isWrite() {
			t.Errorf("%q is not treated as a write tool", tool)
		}
	}
}
