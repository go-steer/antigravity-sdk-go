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

package tracing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

// newTestTracer returns a tracer writing to an in-memory exporter, along with
// the recorder holding whatever it finished.
func newTestTracer(t *testing.T) (*tracer, *tracetest.SpanRecorder) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	return newTracer(WithTracerProvider(provider), WithAgentName("tester")), recorder
}

// spanNames lists the recorded spans in the order they finished.
func spanNames(recorder *tracetest.SpanRecorder) []string {
	ended := recorder.Ended()
	names := make([]string, 0, len(ended))
	for _, s := range ended {
		names = append(names, s.Name())
	}
	return names
}

// findSpan returns the single recorded span with the given name.
func findSpan(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()

	var found sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() != name {
			continue
		}
		if found != nil {
			t.Fatalf("more than one span is named %q", name)
		}
		found = s
	}
	if found == nil {
		t.Fatalf("no span named %q; recorded %v", name, spanNames(recorder))
	}
	return found
}

// attr reads a span attribute, failing the test when it is absent.
func attr(t *testing.T, s sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q has no attribute %q", s.Name(), key)
	return attribute.Value{}
}

// step builds a step update for a trajectory.
func step(traj string, index int, status antigravity.StepStatus) antigravity.Step {
	return antigravity.Step{
		TrajectoryID: traj,
		Index:        index,
		Type:         antigravity.StepTextResponse,
		Status:       status,
	}
}

// runTurn drives one complete turn through the tracer's hooks.
func runTurn(t *testing.T, tr *tracer, body func()) {
	t.Helper()
	ctx := t.Context()

	if _, err := tr.preTurn(ctx, nil, nil); err != nil {
		t.Fatalf("preTurn: %v", err)
	}
	body()
	if err := tr.postTurn(ctx, nil, ""); err != nil {
		t.Fatalf("postTurn: %v", err)
	}
}

func TestSessionSpanBracketsTheSession(t *testing.T) {
	tr, recorder := newTestTracer(t)
	ctx := t.Context()

	if err := tr.sessionStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.Ended()) != 0 {
		t.Error("the session span was exported before the session ended")
	}
	if err := tr.sessionEnd(ctx, nil); err != nil {
		t.Fatal(err)
	}

	s := findSpan(t, recorder, "antigravity.session")
	if got := attr(t, s, "gen_ai.agent.name").AsString(); got != "tester" {
		t.Errorf("gen_ai.agent.name = %q, want %q", got, "tester")
	}
}

func TestTurnSpanNestsUnderTheSession(t *testing.T) {
	tr, recorder := newTestTracer(t)
	ctx := t.Context()

	if err := tr.sessionStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	runTurn(t, tr, func() {})
	if err := tr.sessionEnd(ctx, nil); err != nil {
		t.Fatal(err)
	}

	session := findSpan(t, recorder, "antigravity.session")
	turn := findSpan(t, recorder, "invoke_agent tester")
	if turn.Parent().SpanID() != session.SpanContext().SpanID() {
		t.Error("the turn span is not a child of the session span")
	}
	if got := attr(t, turn, "gen_ai.operation.name").AsString(); got != "invoke_agent" {
		t.Errorf("gen_ai.operation.name = %q, want invoke_agent", got)
	}
}

func TestStepSpanOpensOnceAndClosesWhenDone(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		// The same step is reported repeatedly as it streams.
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusActive))
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusActive))
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusDone))
	})

	s := findSpan(t, recorder, "antigravity.step.0")
	if got := attr(t, s, "antigravity.step.status").AsString(); got != string(antigravity.StatusDone) {
		t.Errorf("antigravity.step.status = %q, want DONE", got)
	}
	if got := attr(t, s, "antigravity.step.trajectory_id").AsString(); got != "main" {
		t.Errorf("antigravity.step.trajectory_id = %q, want main", got)
	}
	if parent := findSpan(t, recorder, "invoke_agent tester"); s.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("the step span is not a child of the turn span")
	}
}

func TestLateUpdateDoesNotReopenAStep(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusDone))
		// Arriving after the step settled, this must be ignored rather than
		// starting a second span for the same index.
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusActive))
	})

	var count int
	for _, name := range spanNames(recorder) {
		if name == "antigravity.step.0" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("recorded %d spans for step 0, want 1: %v", count, spanNames(recorder))
	}
}

func TestUnsettledStepIsClosedByTheNextOne(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusActive))
		// Step 0 never settles; the harness has moved on.
		tr.observeStep(t.Context(), nil, step("main", 1, antigravity.StatusDone))
	})

	findSpan(t, recorder, "antigravity.step.0")
	findSpan(t, recorder, "antigravity.step.1")
}

func TestErroredStepIsMarkedAsFailed(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		failed := step("main", 0, antigravity.StatusError)
		failed.Error = "the model call failed"
		tr.observeStep(t.Context(), nil, failed)
	})

	s := findSpan(t, recorder, "antigravity.step.0")
	if s.Status().Code != codes.Error {
		t.Errorf("status code = %v, want Error", s.Status().Code)
	}
	if !strings.Contains(s.Status().Description, "the model call failed") {
		t.Errorf("status description = %q, want the step's error", s.Status().Description)
	}
}

func TestStepsOutsideATurnAreIgnored(t *testing.T) {
	tr, recorder := newTestTracer(t)

	// Replayed history arrives before any turn has started.
	tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusDone))

	if names := spanNames(recorder); len(names) != 0 {
		t.Errorf("recorded %v, want nothing", names)
	}
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

func TestToolSpanNestsUnderItsStep(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		s := step("main", 0, antigravity.StatusActive)
		s.ToolCalls = []antigravity.ToolCall{{ID: "call-1", Name: "view_file"}}
		tr.observeStep(t.Context(), nil, s)
		// Reported again on the next update: still one span.
		tr.observeStep(t.Context(), nil, s)

		if err := tr.postToolCall(t.Context(), nil, antigravity.ToolResult{ID: "call-1", Name: "view_file"}); err != nil {
			t.Fatal(err)
		}
	})

	tool := findSpan(t, recorder, "execute_tool view_file")
	stepSpan := findSpan(t, recorder, "antigravity.step.0")
	if tool.Parent().SpanID() != stepSpan.SpanContext().SpanID() {
		t.Error("the tool span is not a child of the step span")
	}
	if got := attr(t, tool, "gen_ai.tool.name").AsString(); got != "view_file" {
		t.Errorf("gen_ai.tool.name = %q, want view_file", got)
	}
}

func TestToolSpanRecordsAFailedResult(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		s := step("main", 0, antigravity.StatusActive)
		s.ToolCalls = []antigravity.ToolCall{{ID: "call-1", Name: "run_command"}}
		tr.observeStep(t.Context(), nil, s)

		result := antigravity.ToolResult{ID: "call-1", Name: "run_command", Err: errors.New("exit status 1")}
		if err := tr.postToolCall(t.Context(), nil, result); err != nil {
			t.Fatal(err)
		}
	})

	tool := findSpan(t, recorder, "execute_tool run_command")
	if tool.Status().Code != codes.Error {
		t.Errorf("status code = %v, want Error", tool.Status().Code)
	}
	if len(tool.Events()) == 0 {
		t.Error("the failure was not recorded as a span event")
	}
}

func TestToolErrorHookClosesTheSpanWithoutRewording(t *testing.T) {
	tr, recorder := newTestTracer(t)

	var message string
	runTurn(t, tr, func() {
		s := step("main", 0, antigravity.StatusActive)
		s.ToolCalls = []antigravity.ToolCall{{ID: "call-1", Name: "custom"}}
		tr.observeStep(t.Context(), nil, s)

		toolErr := &antigravity.ToolError{ToolName: "custom", CallID: "call-1", Err: errors.New("boom")}
		var err error
		message, err = tr.toolError(t.Context(), nil, toolErr)
		if err != nil {
			t.Fatal(err)
		}
	})

	if message != "" {
		t.Errorf("the hook reworded the error as %q; tracing must not change what the model sees", message)
	}
	if tool := findSpan(t, recorder, "execute_tool custom"); tool.Status().Code != codes.Error {
		t.Errorf("status code = %v, want Error", tool.Status().Code)
	}
}

func TestToolCallWithoutAnIDIsNotTraced(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		s := step("main", 0, antigravity.StatusDone)
		// Without an id there is no result to match, so a span would never end.
		s.ToolCalls = []antigravity.ToolCall{{Name: "view_file"}}
		tr.observeStep(t.Context(), nil, s)
	})

	for _, name := range spanNames(recorder) {
		if strings.HasPrefix(name, "execute_tool") {
			t.Errorf("recorded %q, want no tool span", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Subagents
// ---------------------------------------------------------------------------

func TestSubagentSpanWrapsItsTrajectory(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		main := step("main", 0, antigravity.StatusActive)
		main.ToolCalls = []antigravity.ToolCall{{
			ID:   "call-1",
			Name: string(antigravity.ToolStartSubagent),
			Args: []byte(`{"TypeName":"reviewer"}`),
		}}
		tr.observeStep(t.Context(), nil, main)

		// The delegate's own steps arrive on a separate trajectory.
		tr.observeStep(t.Context(), nil, step("sub", 0, antigravity.StatusDone))

		result := antigravity.ToolResult{ID: "call-1", Name: string(antigravity.ToolStartSubagent)}
		if err := tr.postToolCall(t.Context(), nil, result); err != nil {
			t.Fatal(err)
		}
	})

	subagent := findSpan(t, recorder, "invoke_agent reviewer")
	turn := findSpan(t, recorder, "invoke_agent tester")
	if subagent.Parent().SpanID() != turn.SpanContext().SpanID() {
		t.Error("the subagent span is not a child of the turn span")
	}
	if got := attr(t, subagent, "antigravity.subagent.trajectory_id").AsString(); got != "sub" {
		t.Errorf("antigravity.subagent.trajectory_id = %q, want sub", got)
	}

	// The delegate's step must hang from the delegate, not from the root turn.
	for _, s := range recorder.Ended() {
		if s.Name() == "antigravity.step.0" && s.Parent().SpanID() == subagent.SpanContext().SpanID() {
			return
		}
	}
	t.Error("no step span is a child of the subagent span")
}

func TestSubagentNameFallsBackWhenUnannounced(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		tr.observeStep(t.Context(), nil, step("main", 0, antigravity.StatusDone))
		// A trajectory with no start_subagent call waiting for it.
		tr.observeStep(t.Context(), nil, step("sub", 0, antigravity.StatusDone))
	})

	findSpan(t, recorder, "invoke_agent subagent")
}

func TestSubagentName(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"type name", `{"TypeName":"reviewer"}`, "reviewer"},
		{"role", `{"Role":"critic"}`, "critic"},
		{"type name wins", `{"TypeName":"reviewer","Role":"critic"}`, "reviewer"},
		{"neither", `{"Prompt":"do the thing"}`, "subagent"},
		{"unparseable", `not json`, "subagent"},
		{"absent", ``, "subagent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subagentName(antigravity.ToolCall{Args: []byte(tt.args)})
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cleanup
// ---------------------------------------------------------------------------

func TestTurnEndClosesEverythingLeftOpen(t *testing.T) {
	tr, recorder := newTestTracer(t)

	runTurn(t, tr, func() {
		s := step("main", 0, antigravity.StatusActive)
		s.ToolCalls = []antigravity.ToolCall{{ID: "call-1", Name: "run_command"}}
		tr.observeStep(t.Context(), nil, s)
		// A cancelled turn: no result, no terminal step, no subagent result.
		tr.observeStep(t.Context(), nil, step("sub", 0, antigravity.StatusActive))
	})

	// An unclosed span is never exported at all, so each of these being
	// present is the assertion.
	for _, name := range []string{
		"execute_tool run_command",
		"antigravity.step.0",
		"invoke_agent subagent",
		"invoke_agent tester",
	} {
		if !slicesContains(spanNames(recorder), name) {
			t.Errorf("span %q was left open; recorded %v", name, spanNames(recorder))
		}
	}
}

func TestASecondTurnDoesNotNestInsideTheFirst(t *testing.T) {
	tr, recorder := newTestTracer(t)
	ctx := t.Context()

	if err := tr.sessionStart(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// A turn that never gets its post-turn hook, followed by another.
	if _, err := tr.preTurn(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.preTurn(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.postTurn(ctx, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := tr.sessionEnd(ctx, nil); err != nil {
		t.Fatal(err)
	}

	session := findSpan(t, recorder, "antigravity.session")
	var turns int
	for _, s := range recorder.Ended() {
		if s.Name() != "invoke_agent tester" {
			continue
		}
		turns++
		if s.Parent().SpanID() != session.SpanContext().SpanID() {
			t.Error("a turn span is nested inside another turn")
		}
	}
	if turns != 2 {
		t.Errorf("recorded %d turn spans, want 2", turns)
	}
}

func TestOptionsRegistersEveryHook(t *testing.T) {
	if got := len(Options()); got != 7 {
		t.Errorf("Options returned %d SDK options, want 7", got)
	}
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
