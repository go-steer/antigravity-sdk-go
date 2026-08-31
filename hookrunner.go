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
	"log/slog"
	"sync"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// hookRunner holds the registered hooks and dispatches each lifecycle event to
// them in registration order.
//
// It also owns the context hierarchy: one session context for the whole
// session, a turn context created by each pre-turn dispatch, and an operation
// context per tool call or interaction.
type hookRunner struct {
	sessionStart []SessionHook
	sessionEnd   []SessionHook
	preTurn      []PreTurnHook
	postTurn     []PostTurnHook
	preTool      []PreToolCallHook
	postTool     []PostToolCallHook
	toolError    []OnToolErrorHook
	interaction  []OnInteractionHook
	compaction   []OnCompactionHook
	stop         []StopHook
	step         []StepObserver

	logger *slog.Logger

	session *HookContext

	mu sync.Mutex
	// turn is the context of the turn in flight, kept so later events in the
	// same turn share the state a pre-turn hook set up.
	turn *HookContext
}

// newHookRunner returns a runner that discards its diagnostics. The real
// logger arrives later: the runner is built before the caller's options are
// applied, and [WithLogger] is one of them, so the config assigns the field
// once it knows.
func newHookRunner() *hookRunner {
	return &hookRunner{logger: slog.New(slog.DiscardHandler), session: newHookContext(nil)}
}

// enabledHooks lists the lifecycle events the harness should call back on.
//
// Only these are reported: the harness skips the round trip for anything not
// listed, so an unregistered hook costs nothing. Interaction hooks are absent
// because questions reach the client as step updates rather than hook calls.
func (r *hookRunner) enabledHooks() []pb.LifecycleHook {
	var out []pb.LifecycleHook
	add := func(n int, h pb.LifecycleHook) {
		if n > 0 {
			out = append(out, h)
		}
	}
	add(len(r.sessionStart), pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_START)
	add(len(r.sessionEnd), pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_END)
	add(len(r.preTurn), pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN)
	add(len(r.postTurn), pb.LifecycleHook_LIFECYCLE_HOOK_POST_TURN)
	add(len(r.preTool), pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TOOL)
	add(len(r.postTool), pb.LifecycleHook_LIFECYCLE_HOOK_POST_TOOL)
	add(len(r.toolError), pb.LifecycleHook_LIFECYCLE_HOOK_ON_TOOL_ERROR)
	add(len(r.compaction), pb.LifecycleHook_LIFECYCLE_HOOK_ON_COMPACTION)
	add(len(r.stop), pb.LifecycleHook_LIFECYCLE_HOOK_STOP)
	return out
}

// turnContext returns the context of the turn in flight, creating a detached
// one when an event arrives outside any turn.
func (r *hookRunner) turnContext() *HookContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turn == nil {
		r.turn = newHookContext(r.session)
	}
	return r.turn
}

func (r *hookRunner) setTurnContext(hc *HookContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turn = hc
}

// operationContext returns a fresh scope for a single tool call or
// interaction, nested under the current turn.
func (r *hookRunner) operationContext() *HookContext {
	return newHookContext(r.turnContext())
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// dispatchSessionStart runs the session-start hooks. Their errors are logged
// and otherwise ignored, since an observability hook must not prevent a
// session from starting.
func (r *hookRunner) dispatchSessionStart(ctx context.Context) {
	for _, hook := range r.sessionStart {
		if err := hook(ctx, r.session); err != nil {
			r.logger.Error("a session start hook failed", "error", err)
		}
	}
}

// dispatchSessionEnd runs the session-end hooks, logging any error.
func (r *hookRunner) dispatchSessionEnd(ctx context.Context) {
	for _, hook := range r.sessionEnd {
		if err := hook(ctx, r.session); err != nil {
			r.logger.Error("a session end hook failed", "error", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// dispatchStep notifies the step observers, in the turn's scope.
//
// Observers cannot fail and cannot change anything, so there is nothing to
// collect: this only exists to keep the read loop's call site simple. A
// panicking observer is contained, since it runs on the loop that keeps the
// whole session alive.
func (r *hookRunner) dispatchStep(ctx context.Context, step Step) {
	if len(r.step) == 0 {
		return
	}
	hc := r.turnContext()
	for _, observe := range r.step {
		r.observe(ctx, hc, observe, step)
	}
}

func (r *hookRunner) observe(ctx context.Context, hc *HookContext, observe StepObserver, step Step) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("a step observer panicked", "panic", rec)
		}
	}()
	observe(ctx, hc, step)
}

// ---------------------------------------------------------------------------
// Turn
// ---------------------------------------------------------------------------

// dispatchPreTurn opens a turn scope and asks each pre-turn hook whether the
// prompt may proceed, stopping at the first denial.
//
// A hook that returns an error denies the turn: a gate that could not run is
// not a gate that passed.
func (r *hookRunner) dispatchPreTurn(ctx context.Context, prompt []Content) TurnDecision {
	hc := newHookContext(r.session)
	r.setTurnContext(hc)

	for _, hook := range r.preTurn {
		decision, err := hook(ctx, hc, prompt)
		if err != nil {
			r.logger.Error("a pre-turn hook failed", "error", err)
			return TurnDecision{Deny: true, Reason: fmt.Sprintf("pre-turn hook failed: %v", err)}
		}
		if decision.Deny {
			return decision
		}
	}
	return TurnDecision{}
}

// dispatchPostTurn runs the post-turn hooks and closes the turn scope.
func (r *hookRunner) dispatchPostTurn(ctx context.Context, response string) {
	hc := r.turnContext()
	for _, hook := range r.postTurn {
		if err := hook(ctx, hc, response); err != nil {
			r.logger.Error("a post-turn hook failed", "error", err)
		}
	}
	r.setTurnContext(nil)
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

// dispatchPreToolCall asks each pre-tool hook about a call, stopping at the
// first denial.
//
// Argument rewrites accumulate: each hook sees what the previous ones
// produced, and the returned decision carries the merged result. A hook that
// errors denies the call.
func (r *hookRunner) dispatchPreToolCall(ctx context.Context, call ToolCall) ToolDecision {
	hc := r.operationContext()
	current := call
	merged := map[string]any{}

	for _, hook := range r.preTool {
		decision, err := hook(ctx, hc, current)
		if err != nil {
			r.logger.Error("a pre-tool hook failed", "tool", call.Name, "error", err)
			return ToolDecision{Deny: true, Reason: fmt.Sprintf("pre-tool hook failed: %v", err)}
		}
		if decision.Deny {
			return decision
		}
		if len(decision.ModifiedArgs) == 0 {
			continue
		}
		args := jsonArgs(current.Args)
		if args == nil {
			args = map[string]any{}
		}
		for k, v := range decision.ModifiedArgs {
			args[k] = v
			merged[k] = v
		}
		raw, err := marshalArgsMap(args)
		if err != nil {
			r.logger.Error("a pre-tool hook produced arguments that could not be encoded",
				"tool", call.Name, "error", err)
			return ToolDecision{Deny: true, Reason: fmt.Sprintf("modified tool arguments could not be encoded: %v", err)}
		}
		current.Args = raw
	}

	if len(merged) == 0 {
		return ToolDecision{}
	}
	// Report the full argument set rather than just the edits, so the harness
	// does not have to replay the merge itself.
	return ToolDecision{ModifiedArgs: jsonArgs(current.Args)}
}

// dispatchPostToolCall runs the post-tool hooks over a completed call.
func (r *hookRunner) dispatchPostToolCall(ctx context.Context, result ToolResult) {
	hc := r.operationContext()
	for _, hook := range r.postTool {
		if err := hook(ctx, hc, result); err != nil {
			r.logger.Error("a post-tool hook failed", "tool", result.Name, "error", err)
		}
	}
}

// dispatchToolError offers each hook the chance to reword a tool failure,
// returning the first non-empty replacement.
func (r *hookRunner) dispatchToolError(ctx context.Context, toolErr *ToolError) string {
	hc := r.operationContext()
	for _, hook := range r.toolError {
		message, err := hook(ctx, hc, toolErr)
		if err != nil {
			r.logger.Error("a tool error hook failed", "tool", toolErr.ToolName, "error", err)
			return ""
		}
		if message != "" {
			return message
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Interaction, compaction, stop
// ---------------------------------------------------------------------------

// dispatchInteraction asks each interaction hook to answer the agent's
// questions, returning the first answer set produced.
func (r *hookRunner) dispatchInteraction(ctx context.Context, req QuestionRequest) *QuestionAnswers {
	hc := r.operationContext()
	for _, hook := range r.interaction {
		answers, err := hook(ctx, hc, req)
		if err != nil {
			r.logger.Error("an interaction hook failed", "error", err)
			continue
		}
		if answers != nil {
			return answers
		}
	}
	return nil
}

// dispatchCompaction runs the compaction hooks over the summary step.
func (r *hookRunner) dispatchCompaction(ctx context.Context, step Step) {
	hc := r.operationContext()
	for _, hook := range r.compaction {
		if err := hook(ctx, hc, step); err != nil {
			r.logger.Error("a compaction hook failed", "error", err)
		}
	}
}

// dispatchStop asks each stop hook whether the turn may end, returning the
// first continuation requested.
//
// A hook that errors, or that asks to continue without saying why, is ignored:
// resuming the agent with no instruction would spin the loop.
func (r *hookRunner) dispatchStop(ctx context.Context, args StopArgs) StopDecision {
	hc := r.turnContext()
	for _, hook := range r.stop {
		decision, err := hook(ctx, hc, args)
		if err != nil {
			r.logger.Error("a stop hook failed", "error", err)
			continue
		}
		if !decision.Continue {
			continue
		}
		if err := decision.validate(); err != nil {
			r.logger.Error("ignoring a stop hook's continuation", "error", err)
			continue
		}
		return decision
	}
	return StopDecision{}
}
