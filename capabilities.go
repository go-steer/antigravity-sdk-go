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
	"fmt"
	"slices"
	"time"
)

// RunCommandConfig configures the builtin run_command tool.
type RunCommandConfig struct {
	// EnableDaemons authorizes the agent to start long-running daemon
	// commands, such as background dev servers or watchers, without blocking
	// session completion. When true, an IsDaemon argument is exposed on the
	// run_command tool schema.
	EnableDaemons bool

	// Timeout caps how long a single command may run. Zero means the harness
	// default of ten minutes.
	Timeout time.Duration

	// EnableSandbox runs terminal commands inside the OS-level sandbox. This
	// is forwarded to the harness, which enforces it at execution time, and
	// has no effect where the sandbox is unavailable.
	EnableSandbox bool
}

func (c *RunCommandConfig) validate() error {
	if c == nil {
		return nil
	}
	if c.Timeout < 0 {
		return fmt.Errorf("RunCommandConfig.Timeout must be positive, got %s", c.Timeout)
	}
	return nil
}

// CapabilitiesConfig controls which tools the harness exposes to the model and
// how the agent is permitted to operate.
//
// EnabledTools and DisabledTools are mutually exclusive; setting both is an
// error. Leaving both nil uses the harness defaults, which enable every tool.
type CapabilitiesConfig struct {
	// EnableSubagents allows the agent to spawn and delegate to subagents.
	// Defaults to true via [DefaultCapabilities].
	EnableSubagents bool

	// Behavior selects autonomous or interactive execution.
	Behavior AgentBehavior

	// EnabledTools is an explicit allowlist of builtin tools. Mutually
	// exclusive with DisabledTools.
	EnabledTools []BuiltinTool

	// DisabledTools is an explicit denylist of builtin tools. Mutually
	// exclusive with EnabledTools.
	DisabledTools []BuiltinTool

	// CompactionThreshold is the token count above which the context window
	// may be compacted. Zero uses the backend default.
	CompactionThreshold uint32

	// FinishToolSchemaJSON is a JSON schema for the finish tool, used to
	// constrain structured output. Usually set indirectly by
	// [WithResponseSchema].
	FinishToolSchemaJSON string

	// MaxSubagentDepth caps subagent recursion for the session. Zero means the
	// default of 1, a single flat level of delegation. Must be at least 1 when
	// set, and may only be set when subagents are enabled.
	MaxSubagentDepth int

	// AllowedSubagents restricts which registered subagents the root agent may
	// invoke directly. Nil makes all registered subagents discoverable.
	AllowedSubagents []string

	// RunCommand configures the builtin run_command tool.
	RunCommand *RunCommandConfig
}

// DefaultCapabilities returns the capability set used when a caller does not
// supply one: every builtin tool enabled, autonomous behavior, and subagents
// permitted.
//
// Note that this is permissive by design; the safety boundary for a default
// [Agent] is the policy list, not the capability set. See [ConfirmRunCommand].
func DefaultCapabilities() CapabilitiesConfig {
	return CapabilitiesConfig{
		EnableSubagents: true,
		Behavior:        BehaviorAutonomous,
	}
}

// ReadOnlyCapabilities returns a capability set restricted to tools that
// cannot modify state.
func ReadOnlyCapabilities() CapabilitiesConfig {
	c := DefaultCapabilities()
	c.EnabledTools = ReadOnlyTools()
	return c
}

// activeTools resolves the effective set of builtin tools implied by the
// allowlist/denylist pair.
func (c *CapabilitiesConfig) activeTools() []BuiltinTool {
	switch {
	case c.EnabledTools != nil:
		return slices.Clone(c.EnabledTools)
	case c.DisabledTools != nil:
		var out []BuiltinTool
		for _, t := range AllTools() {
			if !slices.Contains(c.DisabledTools, t) {
				out = append(out, t)
			}
		}
		return out
	default:
		return AllTools()
	}
}

// hasWriteTools reports whether any active tool can modify state.
func (c *CapabilitiesConfig) hasWriteTools() bool {
	return slices.ContainsFunc(c.activeTools(), BuiltinTool.isWrite)
}

// subagentsDisabled reports whether subagent delegation is unavailable, either
// because it was switched off or because start_subagent is not an active tool.
func (c *CapabilitiesConfig) subagentsDisabled() bool {
	if !c.EnableSubagents {
		return true
	}
	return !slices.Contains(c.activeTools(), ToolStartSubagent)
}

func (c *CapabilitiesConfig) validate() error {
	if c.EnabledTools != nil && c.DisabledTools != nil {
		return fmt.Errorf("EnabledTools and DisabledTools are mutually exclusive")
	}
	if c.MaxSubagentDepth < 0 {
		return fmt.Errorf("MaxSubagentDepth must be at least 1, got %d", c.MaxSubagentDepth)
	}
	if c.subagentsDisabled() {
		if c.MaxSubagentDepth != 0 {
			return fmt.Errorf(
				"MaxSubagentDepth cannot be configured when subagents are disabled " +
					"(EnableSubagents is false, or start_subagent is not an active tool)")
		}
		if c.AllowedSubagents != nil {
			return fmt.Errorf("AllowedSubagents cannot be specified when subagents are disabled")
		}
	}
	return c.RunCommand.validate()
}

// interactiveToolWarning returns a non-fatal advisory when the configuration
// enables ask_question without interactive behavior, which leaves the tool
// unable to actually reach a human. Empty when there is nothing to report.
func (c *CapabilitiesConfig) interactiveToolWarning() string {
	if c.EnabledTools == nil || !slices.Contains(c.EnabledTools, ToolAskQuestion) {
		return ""
	}
	if c.Behavior == BehaviorInteractive {
		return ""
	}
	return "ask_question is enabled but Behavior is not BehaviorInteractive; " +
		"set Behavior to BehaviorInteractive if interactive question-and-answer is desired"
}

// SubagentCapabilities configures a subagent's permitted tools and behavior.
// It mirrors [CapabilitiesConfig] but omits session-wide settings.
type SubagentCapabilities struct {
	// Behavior selects autonomous or interactive execution. Subagents default
	// to autonomous.
	Behavior AgentBehavior

	// AllowedSubagents restricts which subagents this subagent may invoke.
	// Nil makes all registered subagents discoverable.
	AllowedSubagents []string

	// EnabledTools is an explicit allowlist. Mutually exclusive with
	// DisabledTools.
	EnabledTools []BuiltinTool

	// DisabledTools is an explicit denylist. Mutually exclusive with
	// EnabledTools.
	DisabledTools []BuiltinTool

	// RunCommand configures the builtin run_command tool for this subagent.
	RunCommand *RunCommandConfig
}

// activeTools resolves the effective set of builtin tools for a subagent. A
// nil configuration means read-only, which is the safe default for delegated
// work.
func (c *SubagentCapabilities) activeTools() []BuiltinTool {
	switch {
	case c == nil:
		return ReadOnlyTools()
	case c.EnabledTools != nil:
		return slices.Clone(c.EnabledTools)
	case c.DisabledTools != nil:
		var out []BuiltinTool
		for _, t := range AllTools() {
			if !slices.Contains(c.DisabledTools, t) {
				out = append(out, t)
			}
		}
		return out
	default:
		return AllTools()
	}
}

func (c *SubagentCapabilities) validate() error {
	if c == nil {
		return nil
	}
	if c.EnabledTools != nil && c.DisabledTools != nil {
		return fmt.Errorf("EnabledTools and DisabledTools are mutually exclusive")
	}
	disabled := (c.DisabledTools != nil && slices.Contains(c.DisabledTools, ToolStartSubagent)) ||
		(c.EnabledTools != nil && !slices.Contains(c.EnabledTools, ToolStartSubagent))
	if disabled && c.AllowedSubagents != nil {
		return fmt.Errorf(
			"AllowedSubagents cannot be specified when start_subagent is disabled " +
				"or omitted from EnabledTools")
	}
	return c.RunCommand.validate()
}

// SubagentConfig declares a named subagent the root agent may delegate to.
type SubagentConfig struct {
	// Name uniquely identifies the subagent.
	Name string

	// Description tells the model what this subagent is for.
	Description string

	// Instructions optionally shapes the subagent's system prompt. A
	// [TemplatedInstructions] value is appended to the subagent's defaults; a
	// [CustomInstructions] value replaces them entirely.
	Instructions SystemInstructions

	// Capabilities controls the subagent's tools. Nil defaults to read-only.
	Capabilities *SubagentCapabilities

	// Tools are additional custom tools available to this subagent.
	Tools []Tool
}

func (s *SubagentConfig) validate() error {
	if s.Name == "" {
		return fmt.Errorf("SubagentConfig.Name must not be empty")
	}
	if err := s.Capabilities.validate(); err != nil {
		return fmt.Errorf("subagent %q: %w", s.Name, err)
	}
	return nil
}
