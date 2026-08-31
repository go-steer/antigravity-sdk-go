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
	"fmt"
	"sync"
)

// Hooks observe and steer the agent at fixed points in its lifecycle: before a
// turn, around every tool call, when the context is compacted, and when the
// agent wants to stop.
//
// Register them with the With* options ([WithPreTurnHook],
// [WithPreToolCallHook], and so on). Several hooks of the same kind run in
// registration order.
//
// A hook that blocks holds up the agent, so keep them quick and respect the
// context they are given.

// ---------------------------------------------------------------------------
// Hook context
// ---------------------------------------------------------------------------

// HookContext is a key/value store hooks use to carry state between the points
// where they run.
//
// Contexts nest: an operation context falls back to its turn, and a turn to
// the session. Reads walk up the chain, writes always land locally, so a turn
// cannot clobber session state by accident.
//
// A HookContext is safe for concurrent use.
type HookContext struct {
	parent *HookContext

	mu     sync.Mutex
	values map[string]any
}

// newHookContext returns a context nested under parent, which may be nil for a
// session-scoped one.
func newHookContext(parent *HookContext) *HookContext {
	return &HookContext{parent: parent, values: map[string]any{}}
}

// Get returns the value stored under key, searching this context and then its
// ancestors. It reports false when no scope holds the key.
func (c *HookContext) Get(key string) (any, bool) {
	for scope := c; scope != nil; scope = scope.parent {
		scope.mu.Lock()
		v, ok := scope.values[key]
		scope.mu.Unlock()
		if ok {
			return v, true
		}
	}
	return nil, false
}

// Set stores a value in this context's own scope, shadowing any ancestor's
// value for the same key.
func (c *HookContext) Set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[key] = value
}

// Update replaces the value under key with the result of applying fn to it,
// atomically with respect to other Updates on the same context.
//
// fn receives nil when the key is unset anywhere in the chain, and the
// inherited value when only an ancestor has it — the result is still stored
// locally, so the ancestor is left untouched. Keep fn fast: it runs while the
// context is locked.
func (c *HookContext) Update(key string, fn func(current any) any) any {
	c.mu.Lock()
	defer c.mu.Unlock()

	current, ok := c.values[key]
	if !ok && c.parent != nil {
		current, _ = c.parent.Get(key)
	}
	next := fn(current)
	if c.values == nil {
		c.values = map[string]any{}
	}
	c.values[key] = next
	return next
}

// ---------------------------------------------------------------------------
// Decisions
// ---------------------------------------------------------------------------

// TurnDecision is a pre-turn hook's verdict on a prompt. Its zero value allows
// the turn, so a hook that only observes can return it unchanged.
type TurnDecision struct {
	// Deny stops the turn before the model sees the prompt.
	Deny bool
	// Reason explains a denial to the caller. It is ignored when Deny is false.
	Reason string
}

// ToolDecision is a pre-tool hook's verdict on a call. Its zero value allows
// the call to run with the arguments the model chose.
type ToolDecision struct {
	// Deny blocks the call and reports Reason to the model in its place.
	Deny bool
	// Reason explains a denial. It is ignored when Deny is false.
	Reason string
	// ModifiedArgs are shallow-merged over the call's arguments, replacing the
	// keys named and leaving the rest alone. Ignored when Deny is true.
	ModifiedArgs map[string]any
}

// StopArgs describes a turn that is about to end, and is what a stop hook
// decides on.
type StopArgs struct {
	// Response is the agent's most recent response text in the turn.
	Response string
	// TrajectoryID identifies the trajectory that is stopping.
	TrajectoryID string
	// ContinuationCount is how many times a stop hook has already resumed this
	// turn, starting at 0.
	ContinuationCount int
	// StopReason is why the trajectory stopped.
	StopReason StopReason
	// Error is set when the turn stopped because of a fatal failure.
	Error string
}

// StopDecision is a stop hook's verdict. Its zero value lets the turn end.
type StopDecision struct {
	// Continue resumes the agent loop instead of ending the turn.
	Continue bool
	// Reason is injected as a system message when the loop resumes, telling the
	// agent what is left to do. It is required when Continue is set.
	Reason string
}

// validate rejects a continuation with nothing to say, which would resume the
// agent with no instruction and likely loop forever.
func (d StopDecision) validate() error {
	if d.Continue && d.Reason == "" {
		return fmt.Errorf("a StopDecision with Continue set requires a Reason")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Interaction
// ---------------------------------------------------------------------------

// QuestionOption is one choice offered by a [Question].
type QuestionOption struct {
	// Text is the choice as shown to the user.
	Text string
}

// Question is a single multiple-choice question from the agent.
type Question struct {
	// Text is the question itself.
	Text string
	// Options are the choices offered, in the order the agent listed them.
	Options []QuestionOption
	// MultiSelect reports whether more than one option may be chosen.
	MultiSelect bool
}

// QuestionRequest is a batch of questions the agent wants answered before it
// continues.
type QuestionRequest struct {
	Questions []Question
}

// Answer responds to one [Question].
//
// Its zero value answers nothing, which is not the same as skipping: use
// Skipped to tell the agent the question was declined.
type Answer struct {
	// SelectedOptions are indices into the question's Options.
	SelectedOptions []int
	// Text is a freeform reply, which may accompany or replace a selection.
	Text string
	// Skipped marks the question as deliberately unanswered.
	Skipped bool
}

// QuestionAnswers responds to a [QuestionRequest]. Answers correspond
// positionally to the questions asked; a short slice leaves the rest
// unanswered.
type QuestionAnswers struct {
	Answers []Answer
}

// ---------------------------------------------------------------------------
// Hook signatures
// ---------------------------------------------------------------------------

// SessionHook runs when the session starts or ends. It is for observability;
// its error is logged and does not stop the session.
type SessionHook func(ctx context.Context, hc *HookContext) error

// StepObserver is notified of every step the harness reports, including the
// intermediate updates a step passes through and the steps of subagent
// trajectories.
//
// Unlike the other hooks it decides nothing and returns nothing: it exists for
// tracing and progress reporting. It runs on the read loop, before the step
// reaches the conversation, so an observer that blocks stalls the session.
// Hand slow work to a goroutine.
type StepObserver func(ctx context.Context, hc *HookContext, step Step)

// PreTurnHook runs before a turn begins and can refuse the prompt.
type PreTurnHook func(ctx context.Context, hc *HookContext, prompt []Content) (TurnDecision, error)

// PostTurnHook runs after a turn ends, receiving the agent's response text.
type PostTurnHook func(ctx context.Context, hc *HookContext, response string) error

// PreToolCallHook runs before a tool executes and can deny it or rewrite its
// arguments.
//
// It fires for every tool the agent invokes — built-in, MCP, and custom —
// which makes it the single place to gate side effects.
type PreToolCallHook func(ctx context.Context, hc *HookContext, call ToolCall) (ToolDecision, error)

// PostToolCallHook runs after a tool finishes, successfully or not.
type PostToolCallHook func(ctx context.Context, hc *HookContext, result ToolResult) error

// OnToolErrorHook shapes the failure message a tool reports to the model.
//
// Returning a non-empty string replaces the default message; returning ""
// leaves it alone and passes the failure to the next hook. The hook cannot
// retry the call, but it can steer the agent toward a recovery.
type OnToolErrorHook func(ctx context.Context, hc *HookContext, err *ToolError) (string, error)

// OnInteractionHook answers questions the agent asks the user.
//
// The first hook to return a non-nil answer set wins. With no hook registered,
// every question is reported unanswered so the agent can move on.
type OnInteractionHook func(ctx context.Context, hc *HookContext, req QuestionRequest) (*QuestionAnswers, error)

// OnCompactionHook runs when the harness compacts the model's context, which
// happens once the conversation outgrows the context window.
type OnCompactionHook func(ctx context.Context, hc *HookContext, step Step) error

// StopHook runs when the agent is about to go idle and can send it back to
// work.
//
// Use it to enforce a completion criterion: check the response, and return
// [StopDecision] with Continue set to resume the loop with further
// instructions.
type StopHook func(ctx context.Context, hc *HookContext, args StopArgs) (StopDecision, error)
