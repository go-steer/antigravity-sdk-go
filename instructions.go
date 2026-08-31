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

// SystemInstructions configures the agent's system prompt. Exactly two
// implementations exist: [TemplatedInstructions], which augments the harness
// defaults, and [CustomInstructions], which replaces them wholesale.
//
// The interface is closed to implementations outside this package.
type SystemInstructions interface {
	isSystemInstructions()
}

// InstructionSection is a named block appended to the default system
// instructions.
type InstructionSection struct {
	// Content is the body of the section.
	Content string
	// Title names the section. Defaults to "user_system_instructions" when
	// empty.
	Title string
}

// TemplatedInstructions overrides the agent's identity and appends sections to
// the harness's default system instructions. This is the recommended way to
// shape an agent's behavior, because the defaults carry safety and tool-usage
// guidance that is easy to omit by accident.
type TemplatedInstructions struct {
	// Identity replaces the agent's description of who it is. Optional.
	Identity string
	// Sections are appended to the default instructions in order.
	Sections []InstructionSection
}

func (TemplatedInstructions) isSystemInstructions() {}

// CustomInstructions completely replaces the system instructions.
//
// For advanced use only. This discards ALL default instructions, making you
// responsible for supplying everything the agent needs, including core safety
// mandates such as credential protection, engineering standards, and tool
// usage protocols. Most callers want [TemplatedInstructions] instead.
type CustomInstructions struct {
	// Text is the complete system prompt.
	Text string
}

func (CustomInstructions) isSystemInstructions() {}

// AgentBehavior selects an agent's operational execution mode.
type AgentBehavior string

const (
	// BehaviorAutonomous runs the agent non-interactively: it is expected to
	// accomplish the task start to finish on its own. This is the default.
	BehaviorAutonomous AgentBehavior = "autonomous"
	// BehaviorInteractive makes the agent work collaboratively with a human,
	// asking for clarification and keeping them in the loop. Required for
	// slash commands and planning mode.
	BehaviorInteractive AgentBehavior = "interactive"
)
