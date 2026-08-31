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

// Package tracing traces an Antigravity agent session with OpenTelemetry.
//
// It lives in its own module so that the SDK itself depends on nothing but the
// standard library, protobuf, and a WebSocket client. Adding tracing is one
// call:
//
//	agent, err := antigravity.New(ctx, append(
//		tracing.Options(),
//		antigravity.WithWorkspaces("."),
//	)...)
//
// The spans form a tree that mirrors the agent's structure:
//
//	antigravity.session
//	└── invoke_agent <name>              (one per turn)
//	    ├── antigravity.step.<n>         (one per trajectory step)
//	    │   └── execute_tool <tool>
//	    └── invoke_agent <subagent>      (one per delegated trajectory)
//	        └── antigravity.step.<n>
//
// Turn, subagent, and tool spans follow the OpenTelemetry semantic conventions
// for generative AI, so gen_ai.operation.name is invoke_agent or execute_tool.
// Step spans are specific to this SDK and are named for it.
package tracing

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

// scopeName identifies this instrumentation to a tracer provider.
const scopeName = "github.com/go-steer/antigravity-sdk-go/tracing"

// defaultAgentName labels the root agent when the caller does not name it.
const defaultAgentName = "antigravity"

// Option configures the tracing integration.
type Option func(*tracer)

// WithTracerProvider traces to a specific provider. The default is the global
// one, [otel.GetTracerProvider].
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(t *tracer) {
		if tp != nil {
			t.provider = tp
		}
	}
}

// WithAgentName labels the root agent's spans. It defaults to "antigravity",
// and is worth setting when several agents report to the same backend.
func WithAgentName(name string) Option {
	return func(t *tracer) {
		if name != "" {
			t.agentName = name
		}
	}
}

// Options returns the SDK options that install tracing on a session.
//
// Pass them to [antigravity.New] alongside your own. Each option registers one
// hook, and hooks cost a round trip to the harness, so a traced session is
// measurably chattier than an untraced one.
//
// Deliberately absent is a pre-tool hook: registering one would satisfy the
// SDK's requirement that an agent with write tools be supervised, and tracing
// supervises nothing. Tool spans therefore open when a step announces the call
// rather than when the harness begins executing it.
func Options(opts ...Option) []antigravity.Option {
	t := newTracer(opts...)
	return []antigravity.Option{
		antigravity.WithSessionStartHook(t.sessionStart),
		antigravity.WithSessionEndHook(t.sessionEnd),
		antigravity.WithPreTurnHook(t.preTurn),
		antigravity.WithPostTurnHook(t.postTurn),
		antigravity.WithStepObserver(t.observeStep),
		antigravity.WithPostToolCallHook(t.postToolCall),
		antigravity.WithToolErrorHook(t.toolError),
	}
}

// span is a span together with the context that parents its children.
type span struct {
	ctx  context.Context
	span trace.Span
}

// end closes the span if it is open.
func (s *span) end() {
	if s.span != nil {
		s.span.End()
		s.span = nil
		s.ctx = nil
	}
}

// live reports whether the span is open.
func (s *span) live() bool { return s.span != nil }

// stepSpan is the open span of the step currently running in a trajectory.
type stepSpan struct {
	span
	index int
}

// tracer holds the spans of a session in flight.
//
// The SDK calls hooks from several goroutines — the read loop, and one per
// tool call — so every field is guarded. The state is deliberately a plain
// struct rather than values stashed in a [antigravity.HookContext]: the
// relationships here span scopes, and a step observer and a tool hook must see
// the same map.
type tracer struct {
	provider  trace.TracerProvider
	tracer    trace.Tracer
	agentName string

	mu      sync.Mutex
	session span
	turn    span

	// mainTrajectory is the root agent's trajectory for the current turn.
	// Anything else is a subagent.
	mainTrajectory string

	// steps holds the open step span of each trajectory, and closed records
	// the last step index closed there, so a late update cannot reopen it.
	steps  map[string]*stepSpan
	closed map[string]int

	// subagents holds one span per delegated trajectory, and trajectoryOfCall
	// maps the start_subagent call that produced it back to that trajectory,
	// which is what lets the call's result close the span.
	subagents        map[string]*span
	trajectoryOfCall map[string]string
	pendingSubagents []pendingSubagent

	// tools holds the open span of each tool call, keyed by call id.
	tools map[string]trace.Span
}

// pendingSubagent is a delegation that has been requested but whose trajectory
// has not yet appeared.
type pendingSubagent struct {
	callID string
	name   string
}

func newTracer(opts ...Option) *tracer {
	t := &tracer{
		provider:         otel.GetTracerProvider(),
		agentName:        defaultAgentName,
		steps:            map[string]*stepSpan{},
		closed:           map[string]int{},
		subagents:        map[string]*span{},
		trajectoryOfCall: map[string]string{},
		tools:            map[string]trace.Span{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	t.tracer = t.provider.Tracer(scopeName)
	return t
}

// ---------------------------------------------------------------------------
// Session and turn
// ---------------------------------------------------------------------------

func (t *tracer) sessionStart(ctx context.Context, _ *antigravity.HookContext) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// The session outlives the context that started it, so the span is
	// re-rooted on a background context: keeping the caller's would hand every
	// later span a deadline that has nothing to do with it.
	_, s := t.tracer.Start(ctx, "antigravity.session",
		trace.WithAttributes(attribute.String("gen_ai.agent.name", t.agentName)))
	t.session = span{ctx: trace.ContextWithSpan(context.Background(), s), span: s}
	return nil
}

func (t *tracer) sessionEnd(context.Context, *antigravity.HookContext) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.endTurnLocked()
	t.session.end()
	return nil
}

func (t *tracer) preTurn(ctx context.Context, _ *antigravity.HookContext, _ []antigravity.Content) (antigravity.TurnDecision, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// A turn left open by a missed post-turn hook would otherwise nest the new
	// one inside it.
	t.endTurnLocked()

	parent := t.session.ctx
	if parent == nil {
		parent = ctx
	}
	spanCtx, s := t.tracer.Start(parent, "invoke_agent "+t.agentName,
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", t.agentName),
		))
	t.turn = span{ctx: spanCtx, span: s}
	return antigravity.TurnDecision{}, nil
}

func (t *tracer) postTurn(_ context.Context, _ *antigravity.HookContext, _ string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.endTurnLocked()
	return nil
}

// endTurnLocked closes the turn and everything still open beneath it.
//
// A step or tool span can outlive its natural end when a turn is cancelled or
// the harness stops reporting, and an unclosed span is never exported at all,
// so the turn sweeps up after itself.
func (t *tracer) endTurnLocked() {
	if !t.turn.live() {
		return
	}
	for traj := range t.steps {
		t.endStepLocked(traj)
	}
	for _, s := range t.subagents {
		s.end()
	}
	for id, s := range t.tools {
		s.End()
		delete(t.tools, id)
	}

	clear(t.subagents)
	clear(t.trajectoryOfCall)
	clear(t.closed)
	t.pendingSubagents = nil
	t.mainTrajectory = ""
	t.turn.end()
}

// ---------------------------------------------------------------------------
// Steps
// ---------------------------------------------------------------------------

// observeStep opens, updates, and closes step spans as a trajectory advances.
//
// A step arrives many times over: once when it starts, again for each delta,
// and a last time when it settles. The first sighting opens the span and the
// terminal one closes it; a step index that moves on without settling closes
// the previous span anyway, since the harness has clearly finished with it.
func (t *tracer) observeStep(_ context.Context, _ *antigravity.HookContext, step antigravity.Step) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Steps outside a turn — replayed history, or a trigger-driven turn the
	// SDK did not initiate — have nothing to hang from.
	if !t.turn.live() {
		return
	}
	traj := step.TrajectoryID
	if t.mainTrajectory == "" {
		t.mainTrajectory = traj
	}

	current := t.steps[traj]
	if current != nil && current.index != step.Index {
		t.endStepLocked(traj)
		current = nil
	}
	if current == nil {
		if last, ok := t.closed[traj]; ok && step.Index <= last {
			// The step already ended; a trailing update must not resurrect it.
			return
		}
		current = t.startStepLocked(traj, step)
	}

	for _, call := range step.ToolCalls {
		t.startToolLocked(current, call)
	}

	switch step.Status {
	case antigravity.StatusDone, antigravity.StatusError, antigravity.StatusCanceled:
		current.span.span.SetAttributes(
			attribute.String("antigravity.step.type", string(step.Type)),
			attribute.String("antigravity.step.status", string(step.Status)),
		)
		if step.Status == antigravity.StatusError {
			current.span.span.SetStatus(codes.Error, step.Error)
		}
		t.endStepLocked(traj)
	}
}

// startStepLocked opens a step span under its trajectory's parent.
func (t *tracer) startStepLocked(traj string, step antigravity.Step) *stepSpan {
	parent := t.turn
	if traj != t.mainTrajectory && traj != "" {
		parent = *t.subagentLocked(traj)
	}

	ctx, s := t.tracer.Start(parent.ctx, fmt.Sprintf("antigravity.step.%d", step.Index),
		trace.WithAttributes(
			attribute.Int("antigravity.step.index", step.Index),
			attribute.String("antigravity.step.trajectory_id", traj),
		))
	current := &stepSpan{span: span{ctx: ctx, span: s}, index: step.Index}
	t.steps[traj] = current
	return current
}

// endStepLocked closes a trajectory's open step span and remembers its index.
func (t *tracer) endStepLocked(traj string) {
	current := t.steps[traj]
	if current == nil {
		return
	}
	current.end()
	t.closed[traj] = current.index
	delete(t.steps, traj)
}

// subagentLocked returns the span of a delegated trajectory, opening one the
// first time that trajectory is seen.
//
// The subagent's name comes from the start_subagent call that must have
// preceded it. Delegations are matched to trajectories in the order they were
// requested, which is the best the harness's reporting allows; a trajectory
// with no request waiting is simply called "subagent".
func (t *tracer) subagentLocked(traj string) *span {
	if s, ok := t.subagents[traj]; ok {
		return s
	}

	name := "subagent"
	if len(t.pendingSubagents) > 0 {
		pending := t.pendingSubagents[0]
		t.pendingSubagents = t.pendingSubagents[1:]
		name = pending.name
		t.trajectoryOfCall[pending.callID] = traj
	}

	ctx, sp := t.tracer.Start(t.turn.ctx, "invoke_agent "+name,
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", name),
			attribute.String("antigravity.subagent.trajectory_id", traj),
		))
	s := &span{ctx: ctx, span: sp}
	t.subagents[traj] = s
	return s
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

// startToolLocked opens a span for a tool call the step announced, ignoring
// one that is already open: the same call is reported on every update of the
// step that owns it.
func (t *tracer) startToolLocked(parent *stepSpan, call antigravity.ToolCall) {
	if call.ID == "" || t.tools[call.ID] != nil {
		return
	}

	_, s := t.tracer.Start(parent.ctx, "execute_tool "+call.Name,
		trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", call.Name),
			attribute.String("gen_ai.tool.call.id", call.ID),
		))
	t.tools[call.ID] = s

	if call.Name == string(antigravity.ToolStartSubagent) {
		t.pendingSubagents = append(t.pendingSubagents, pendingSubagent{
			callID: call.ID,
			name:   subagentName(call),
		})
	}
}

// subagentName reads the delegate's name out of a start_subagent call.
func subagentName(call antigravity.ToolCall) string {
	var args struct {
		TypeName string `json:"TypeName"`
		Role     string `json:"Role"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return "subagent"
	}
	switch {
	case args.TypeName != "":
		return args.TypeName
	case args.Role != "":
		return args.Role
	default:
		return "subagent"
	}
}

func (t *tracer) postToolCall(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if s := t.tools[result.ID]; s != nil {
		if result.Err != nil {
			s.RecordError(result.Err)
			s.SetStatus(codes.Error, result.Err.Error())
		}
		s.End()
		delete(t.tools, result.ID)
	}

	// A finished start_subagent means the delegate is done, which is the only
	// notice the harness gives that its trajectory has ended.
	if traj, ok := t.trajectoryOfCall[result.ID]; ok {
		t.endStepLocked(traj)
		if s := t.subagents[traj]; s != nil {
			s.end()
			delete(t.subagents, traj)
		}
		delete(t.trajectoryOfCall, result.ID)
	}
	return nil
}

// toolError records a failure on the tool's span. It never rewords the
// message the model sees: reporting is not the same as intervening.
func (t *tracer) toolError(_ context.Context, _ *antigravity.HookContext, toolErr *antigravity.ToolError) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if s := t.tools[toolErr.CallID]; s != nil {
		s.RecordError(toolErr)
		s.SetStatus(codes.Error, toolErr.Error())
		s.End()
		delete(t.tools, toolErr.CallID)
	}
	return "", nil
}
