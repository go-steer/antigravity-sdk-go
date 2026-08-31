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

import "encoding/json"

// StepType is the high-level kind of a trajectory step.
type StepType string

const (
	StepTextResponse  StepType = "TEXT_RESPONSE"
	StepToolCall      StepType = "TOOL_CALL"
	StepSystemMessage StepType = "SYSTEM_MESSAGE"
	StepCompaction    StepType = "COMPACTION"
	StepFinish        StepType = "FINISH"
	StepThinking      StepType = "THINKING"
	StepUnknown       StepType = "UNKNOWN"
)

// StepSource identifies what produced a step.
type StepSource string

const (
	SourceSystem  StepSource = "SYSTEM"
	SourceUser    StepSource = "USER"
	SourceModel   StepSource = "MODEL"
	SourceUnknown StepSource = "UNKNOWN"
)

// StepTarget identifies who or what a step is directed at.
type StepTarget string

const (
	TargetUser        StepTarget = "TARGET_USER"
	TargetEnvironment StepTarget = "TARGET_ENVIRONMENT"
	TargetUnspecified StepTarget = "TARGET_UNSPECIFIED"
	TargetUnknown     StepTarget = "UNKNOWN"
)

// StepStatus is the lifecycle state of a step.
type StepStatus string

const (
	StatusActive         StepStatus = "ACTIVE"
	StatusDone           StepStatus = "DONE"
	StatusWaitingForUser StepStatus = "WAITING_FOR_USER"
	StatusError          StepStatus = "ERROR"
	StatusCanceled       StepStatus = "CANCELED"
	StatusUnknown        StepStatus = "UNKNOWN"
)

// SessionContinuationMode selects how a session is established.
type SessionContinuationMode string

const (
	// ResumeSession resumes an existing session and fails if there is none.
	ResumeSession SessionContinuationMode = "resume"
	// CreateOrResumeSession resumes when possible and otherwise creates.
	CreateOrResumeSession SessionContinuationMode = "create_or_resume"
	// CreateOnlySession creates a new session and fails if one already exists.
	CreateOnlySession SessionContinuationMode = "create_only"
)

// Step is one action in the agent's trajectory.
//
// Steps arrive incrementally. A single logical response may be delivered as
// many steps carrying successive deltas, so treat [Step.ContentDelta] as the
// streaming increment and [Step.Content] as the accumulated text so far.
type Step struct {
	// ID uniquely identifies the step.
	ID string
	// Index is the step's position in its trajectory.
	Index int
	// TrajectoryID identifies the trajectory that owns this step.
	TrajectoryID string
	// ParentTrajectoryID identifies the trajectory that spawned this one, and
	// is empty for the root conversation.
	ParentTrajectoryID string
	// Depth is the nesting level, 0 for the root conversation. The harness
	// reports depth per trajectory rather than per step, so this is filled in
	// from the owning trajectory's most recent state update.
	Depth int

	// Type is the high-level kind of step.
	Type StepType
	// Source is what produced the step.
	Source StepSource
	// Target is who the step is directed at.
	Target StepTarget
	// Status is the step's lifecycle state.
	Status StepStatus

	// Content is the accumulated output of the step.
	Content string
	// ContentDelta is the text added since the previous update.
	ContentDelta string
	// Thinking is the accumulated model reasoning.
	Thinking string
	// ThinkingDelta is the reasoning added since the previous update.
	ThinkingDelta string

	// ToolCalls are the tool invocations belonging to this step.
	ToolCalls []ToolCall

	// Error is a short message when the step failed, otherwise empty.
	Error string

	// HTTPCode is the status code of the failed model call behind Error, or 0
	// when the failure was not an HTTP error.
	HTTPCode int

	// IsCompleteResponse marks a finished model response aimed at the user, as
	// opposed to a partial streaming chunk. More than one step per turn may
	// set it, so a consumer wanting only the final answer must iterate to the
	// end rather than stopping at the first.
	IsCompleteResponse bool

	// StructuredOutput holds the payload extracted from a finish step, as raw
	// JSON. Use [Step.UnmarshalStructuredOutput] to decode it.
	StructuredOutput json.RawMessage

	// Usage reports tokens consumed by this step's model call. Nil when the
	// step involved no model call.
	Usage *UsageMetadata
}

// UnmarshalStructuredOutput decodes the step's structured output into v. It
// reports false when the step carries no structured output, leaving v
// untouched.
func (s *Step) UnmarshalStructuredOutput(v any) (bool, error) {
	if len(s.StructuredOutput) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(s.StructuredOutput, v); err != nil {
		return false, err
	}
	return true, nil
}
