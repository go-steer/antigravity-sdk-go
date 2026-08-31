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

import "slices"

// BuiltinTool identifies a tool provided by the harness rather than by the
// caller. Builtin tools are enabled or disabled as a set via
// [CapabilitiesConfig]; disabling one strips it from the model's context
// entirely, so the model never sees it and never attempts to call it.
//
// This differs from denying a tool with a policy: a policy-denied tool remains
// visible to the model and is rejected at call time. Prefer disabling for
// tools the agent should never use, and policies for conditional restrictions
// such as blocking run_command only for dangerous arguments.
type BuiltinTool string

const (
	// ToolListDir lists directory contents.
	ToolListDir BuiltinTool = "list_directory"
	// ToolSearchDir searches within directories (grep).
	ToolSearchDir BuiltinTool = "search_directory"
	// ToolFindFile finds files by name within a directory.
	ToolFindFile BuiltinTool = "find_file"
	// ToolViewFile views file contents.
	ToolViewFile BuiltinTool = "view_file"
	// ToolCreateFile creates a new file.
	ToolCreateFile BuiltinTool = "create_file"
	// ToolEditFile edits an existing file.
	ToolEditFile BuiltinTool = "edit_file"
	// ToolRunCommand executes a shell command.
	ToolRunCommand BuiltinTool = "run_command"
	// ToolAskQuestion asks the user a clarifying question.
	ToolAskQuestion BuiltinTool = "ask_question"
	// ToolStartSubagent invokes a subagent.
	ToolStartSubagent BuiltinTool = "start_subagent"
	// ToolGenerateImage generates or edits images.
	ToolGenerateImage BuiltinTool = "generate_image"
	// ToolSearchWeb searches the web.
	ToolSearchWeb BuiltinTool = "search_web"
	// ToolReadURLContent reads content from a URL.
	ToolReadURLContent BuiltinTool = "read_url_content"
	// ToolFinish finishes the conversation and returns structured output.
	ToolFinish BuiltinTool = "finish"
)

// AllTools returns every builtin tool.
func AllTools() []BuiltinTool {
	return []BuiltinTool{
		ToolListDir,
		ToolSearchDir,
		ToolFindFile,
		ToolViewFile,
		ToolCreateFile,
		ToolEditFile,
		ToolRunCommand,
		ToolAskQuestion,
		ToolStartSubagent,
		ToolGenerateImage,
		ToolSearchWeb,
		ToolReadURLContent,
		ToolFinish,
	}
}

// ReadOnlyTools returns the tools that only read state, performing no writes,
// deletes, or command execution. This is the default tool set for a new
// [Agent].
func ReadOnlyTools() []BuiltinTool {
	return []BuiltinTool{
		ToolListDir,
		ToolSearchDir,
		ToolFindFile,
		ToolViewFile,
		ToolReadURLContent,
		ToolFinish,
	}
}

// NondestructiveTools returns the tools that cannot delete content. This is
// broader than [ReadOnlyTools]: it permits creating and editing files, but not
// running arbitrary commands.
func NondestructiveTools() []BuiltinTool {
	return []BuiltinTool{
		ToolListDir,
		ToolSearchDir,
		ToolFindFile,
		ToolViewFile,
		ToolCreateFile,
		ToolEditFile,
		ToolAskQuestion,
		ToolStartSubagent,
		ToolGenerateImage,
		ToolSearchWeb,
		ToolReadURLContent,
		ToolFinish,
	}
}

// FileTools returns the tools that perform file read, write, or create
// operations. These tools accept a file path argument and so can be scoped to
// specific workspace directories with [WorkspaceOnly].
func FileTools() []BuiltinTool {
	return []BuiltinTool{
		ToolViewFile,
		ToolCreateFile,
		ToolEditFile,
	}
}

// isWrite reports whether t can modify state outside the agent's context.
func (t BuiltinTool) isWrite() bool {
	return !slices.Contains(ReadOnlyTools(), t)
}
