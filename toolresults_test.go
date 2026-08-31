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

import "testing"

func TestParseBuiltinToolResult(t *testing.T) {
	tests := []struct {
		name string
		tool BuiltinTool
		raw  string
		want any
	}{
		{
			name: "run_command",
			tool: ToolRunCommand,
			raw:  `{"output":"ok\n"}`,
			want: RunCommandResult{Output: "ok\n"},
		},
		{
			// The harness has used both spellings; a caller should not have to
			// know which one it got.
			name: "run_command with the older key",
			tool: ToolRunCommand,
			raw:  `{"combined_output":"ok\n"}`,
			want: RunCommandResult{Output: "ok\n"},
		},
		{
			name: "search_directory",
			tool: ToolSearchDir,
			raw:  `{"num_results":3}`,
			want: SearchDirectoryResult{NumResults: 3},
		},
		{
			name: "generate_image",
			tool: ToolGenerateImage,
			raw:  `{"image_name":"chart","aspect_ratio":"16:9","output_path":"/tmp/chart.png"}`,
			want: GenerateImageResult{ImageName: "chart", AspectRatio: "16:9", OutputPath: "/tmp/chart.png"},
		},
		{
			name: "search_web",
			tool: ToolSearchWeb,
			raw:  `{"summary":"found it"}`,
			want: SearchWebResult{Summary: "found it"},
		},
		{
			name: "read_url_content",
			tool: ToolReadURLContent,
			raw:  `{"title":"Go","summary":"a language","content_path":"/tmp/go.md"}`,
			want: ReadURLContentResult{Title: "Go", Summary: "a language", ContentPath: "/tmp/go.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBuiltinToolResult(string(tt.tool), tt.raw)
			if !ok {
				t.Fatalf("parseBuiltinToolResult(%q) reported no structured result", tt.tool)
			}
			if got != tt.want {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseBuiltinToolResultListDirectory(t *testing.T) {
	for _, raw := range []string{
		`{"entries":[{"name":"src","is_directory":true},{"name":"go.mod","file_size":42}]}`,
		`{"results":[{"name":"src","is_directory":true},{"name":"go.mod","file_size":42}]}`,
	} {
		got, ok := parseBuiltinToolResult(string(ToolListDir), raw)
		if !ok {
			t.Fatalf("parseBuiltinToolResult(%s) reported no structured result", raw)
		}
		listing, ok := got.(ListDirectoryResult)
		if !ok {
			t.Fatalf("got %T, want ListDirectoryResult", got)
		}
		if len(listing.Entries) != 2 {
			t.Fatalf("got %d entries, want 2", len(listing.Entries))
		}
		if want := "src/ (dir)\ngo.mod (42 bytes)"; listing.String() != want {
			t.Errorf("String() = %q, want %q", listing.String(), want)
		}
	}
}

func TestParseBuiltinToolResultFallsBackToProse(t *testing.T) {
	// edit_file and find_file report plain text often enough that keeping it
	// beats discarding the result.
	got, ok := parseBuiltinToolResult(string(ToolEditFile), "Applied 3 edits")
	if !ok || got != (EditFileResult{Summary: "Applied 3 edits"}) {
		t.Errorf("edit_file = %#v, %v; want the prose kept", got, ok)
	}

	got, ok = parseBuiltinToolResult(string(ToolFindFile), "src/main.go")
	if !ok || got != (FindFileResult{Output: "src/main.go"}) {
		t.Errorf("find_file = %#v, %v; want the prose kept", got, ok)
	}
}

func TestParseBuiltinToolResultWithoutAStructuredForm(t *testing.T) {
	tests := []struct {
		name string
		tool string
		raw  string
	}{
		{"empty payload", string(ToolRunCommand), ""},
		{"no parser", string(ToolViewFile), `{"content":"x"}`},
		{"custom tool", "my_tool", `{"a":1}`},
		{"unparseable payload", string(ToolRunCommand), "not json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBuiltinToolResult(tt.tool, tt.raw)
			if ok {
				t.Errorf("parseBuiltinToolResult = %#v, want no structured result", got)
			}
		})
	}
}

func TestToolResultStrings(t *testing.T) {
	tests := []struct {
		name string
		val  interface{ String() string }
		want string
	}{
		{"run_command", RunCommandResult{Output: "hi"}, "hi"},
		{"search_directory", SearchDirectoryResult{NumResults: 2}, "2 results"},
		{"find_file", FindFileResult{Output: "a.go"}, "a.go"},
		{"edit_file", EditFileResult{Summary: "done"}, "done"},
		{"empty listing", ListDirectoryResult{}, ""},
		{"image with a path", GenerateImageResult{ImageName: "c", OutputPath: "/tmp/c.png"}, "/tmp/c.png"},
		{"image without one", GenerateImageResult{ImageName: "c"}, "c"},
		{"search_web", SearchWebResult{Summary: "three articles"}, "three articles"},
		{"url summary wins", ReadURLContentResult{Title: "T", Summary: "S", ContentPath: "P"}, "S"},
		{"url title next", ReadURLContentResult{Title: "T", ContentPath: "P"}, "T"},
		{"url path last", ReadURLContentResult{ContentPath: "P"}, "P"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.val.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
