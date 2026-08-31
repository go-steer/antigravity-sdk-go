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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Built-in tools report their results as JSON, and a [PostToolCallHook]
// receives them decoded into the types below rather than as raw text. A tool
// with no structured shape, or one whose payload does not parse, is passed
// through as the original string.

// RunCommandResult is the outcome of the run_command tool.
type RunCommandResult struct {
	// Output is the command's combined stdout and stderr.
	Output string `json:"output"`
}

func (r RunCommandResult) String() string { return r.Output }

// UnmarshalJSON accepts either "output" or the harness's older
// "combined_output" spelling.
func (r *RunCommandResult) UnmarshalJSON(data []byte) error {
	var raw struct {
		Output         *string `json:"output"`
		CombinedOutput *string `json:"combined_output"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch {
	case raw.Output != nil:
		r.Output = *raw.Output
	case raw.CombinedOutput != nil:
		r.Output = *raw.CombinedOutput
	}
	return nil
}

// ListDirectoryEntry is one item in a directory listing.
type ListDirectoryEntry struct {
	Name        string `json:"name"`
	IsDirectory bool   `json:"is_directory"`
	FileSize    int64  `json:"file_size"`
}

// ListDirectoryResult is the outcome of the list_directory tool.
type ListDirectoryResult struct {
	Entries []ListDirectoryEntry `json:"entries"`
}

func (r ListDirectoryResult) String() string {
	lines := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		if e.IsDirectory {
			lines = append(lines, e.Name+"/ (dir)")
			continue
		}
		lines = append(lines, e.Name+" ("+strconv.FormatInt(e.FileSize, 10)+" bytes)")
	}
	return strings.Join(lines, "\n")
}

// UnmarshalJSON accepts either "entries" or the harness's older "results"
// spelling.
func (r *ListDirectoryResult) UnmarshalJSON(data []byte) error {
	var raw struct {
		Entries []ListDirectoryEntry `json:"entries"`
		Results []ListDirectoryEntry `json:"results"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.Entries != nil {
		r.Entries = raw.Entries
	} else {
		r.Entries = raw.Results
	}
	return nil
}

// SearchDirectoryResult is the outcome of the search_directory tool.
type SearchDirectoryResult struct {
	NumResults int `json:"num_results"`
}

func (r SearchDirectoryResult) String() string {
	return fmt.Sprintf("%d results", r.NumResults)
}

// FindFileResult is the outcome of the find_file tool.
type FindFileResult struct {
	Output string `json:"output"`
}

func (r FindFileResult) String() string { return r.Output }

// EditFileResult is the outcome of the edit_file tool.
type EditFileResult struct {
	Summary string `json:"summary"`
}

func (r EditFileResult) String() string { return r.Summary }

// GenerateImageResult is the outcome of the generate_image tool.
type GenerateImageResult struct {
	// ImageName is the requested filename prefix for the artifact.
	ImageName string `json:"image_name"`
	// AspectRatio is the requested aspect ratio, such as "16:9".
	AspectRatio string `json:"aspect_ratio"`
	// OutputPath is where the image was written, and is empty when generation
	// produced no file.
	OutputPath string `json:"output_path"`
}

func (r GenerateImageResult) String() string {
	if r.OutputPath != "" {
		return r.OutputPath
	}
	return r.ImageName
}

// SearchWebResult is the outcome of the search_web tool.
type SearchWebResult struct {
	Summary string `json:"summary"`
}

func (r SearchWebResult) String() string { return r.Summary }

// ReadURLContentResult is the outcome of the read_url_content tool.
type ReadURLContentResult struct {
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	ContentPath string `json:"content_path"`
}

func (r ReadURLContentResult) String() string {
	switch {
	case r.Summary != "":
		return r.Summary
	case r.Title != "":
		return r.Title
	default:
		return r.ContentPath
	}
}

// builtinResultParsers decodes a built-in tool's JSON result into its typed
// form. Tools absent from the map have no structured result.
var builtinResultParsers = map[BuiltinTool]func([]byte) (any, error){
	ToolRunCommand:     unmarshalResult[RunCommandResult],
	ToolListDir:        unmarshalResult[ListDirectoryResult],
	ToolFindFile:       unmarshalResult[FindFileResult],
	ToolSearchDir:      unmarshalResult[SearchDirectoryResult],
	ToolEditFile:       unmarshalResult[EditFileResult],
	ToolGenerateImage:  unmarshalResult[GenerateImageResult],
	ToolSearchWeb:      unmarshalResult[SearchWebResult],
	ToolReadURLContent: unmarshalResult[ReadURLContentResult],
}

func unmarshalResult[T any](data []byte) (any, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// parseBuiltinToolResult decodes a built-in tool's result, reporting false when
// the tool has no structured form or its payload will not parse.
//
// Two tools report plain prose often enough that the raw text is worth keeping
// rather than discarding: edit_file and find_file fall back to it.
func parseBuiltinToolResult(tool string, raw string) (any, bool) {
	if raw == "" {
		return nil, false
	}
	parse, ok := builtinResultParsers[BuiltinTool(tool)]
	if !ok {
		return nil, false
	}
	if v, err := parse([]byte(raw)); err == nil {
		return v, true
	}
	switch BuiltinTool(tool) {
	case ToolEditFile:
		return EditFileResult{Summary: raw}, true
	case ToolFindFile:
		return FindFileResult{Output: raw}, true
	default:
		return nil, false
	}
}
