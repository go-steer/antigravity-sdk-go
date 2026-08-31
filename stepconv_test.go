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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

func TestMakeStepID(t *testing.T) {
	if got := makeStepID("traj", 7); got != "traj:7" {
		t.Errorf("makeStepID = %q, want traj:7", got)
	}
	// A step with no trajectory still needs a stable id.
	if got := makeStepID("", 7); got != "7" {
		t.Errorf("makeStepID = %q, want 7", got)
	}
}

func TestStepFromProtoConvertsTheCommonFields(t *testing.T) {
	su := pb.StepUpdate_builder{
		TrajectoryId:       proto.String("main"),
		ParentTrajectoryId: proto.String("root"),
		StepIndex:          proto.Uint32(3),
		Source:             pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:             pb.StepUpdate_TARGET_USER.Enum(),
		State:              pb.StepUpdate_STATE_DONE.Enum(),
		Text:               proto.String("all done"),
		TextDelta:          proto.String("done"),
		Thinking:           proto.String("full reasoning"),
		ThinkingDelta:      proto.String("reasoning"),
	}.Build()

	step := stepFromProto(su)

	if step.ID != "main:3" || step.Index != 3 {
		t.Errorf("id/index = %q/%d, want main:3/3", step.ID, step.Index)
	}
	if step.TrajectoryID != "main" || step.ParentTrajectoryID != "root" {
		t.Errorf("trajectory = %q, parent = %q", step.TrajectoryID, step.ParentTrajectoryID)
	}
	if step.Source != SourceModel || step.Target != TargetUser || step.Status != StatusDone {
		t.Errorf("source/target/status = %q/%q/%q", step.Source, step.Target, step.Status)
	}
	if step.Content != "all done" || step.ContentDelta != "done" {
		t.Errorf("content = %q, delta = %q", step.Content, step.ContentDelta)
	}
	if step.Thinking != "full reasoning" || step.ThinkingDelta != "reasoning" {
		t.Errorf("thinking = %q, delta = %q", step.Thinking, step.ThinkingDelta)
	}
	if step.Type != StepTextResponse {
		t.Errorf("type = %q, want %q", step.Type, StepTextResponse)
	}
	if !step.IsCompleteResponse {
		t.Error("a finished model step aimed at the user is not a complete response")
	}
}

func TestStepFromProtoUnsetEnumsBecomeUnknown(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
	}.Build())

	if step.Source != SourceUnknown || step.Status != StatusUnknown {
		t.Errorf("source/status = %q/%q, want both unknown", step.Source, step.Status)
	}
	// TARGET_UNSPECIFIED is a value the harness sends, distinct from an enum
	// the SDK does not recognize.
	if step.Target != TargetUnspecified {
		t.Errorf("target = %q, want %q", step.Target, TargetUnspecified)
	}
	if step.Type != StepUnknown {
		t.Errorf("type = %q, want %q", step.Type, StepUnknown)
	}
}

func TestStepFromProtoCompleteResponseRequires(t *testing.T) {
	base := func(mutate func(*pb.StepUpdate_builder)) Step {
		b := pb.StepUpdate_builder{
			TrajectoryId: proto.String("main"),
			StepIndex:    proto.Uint32(0),
			Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
			Target:       pb.StepUpdate_TARGET_USER.Enum(),
			State:        pb.StepUpdate_STATE_DONE.Enum(),
			Text:         proto.String("answer"),
		}
		mutate(&b)
		return stepFromProto(b.Build())
	}

	if !base(func(*pb.StepUpdate_builder) {}).IsCompleteResponse {
		t.Fatal("the baseline step is not a complete response")
	}

	tests := map[string]func(*pb.StepUpdate_builder){
		"still active":  func(b *pb.StepUpdate_builder) { b.State = pb.StepUpdate_STATE_ACTIVE.Enum() },
		"from the user": func(b *pb.StepUpdate_builder) { b.Source = pb.StepUpdate_SOURCE_USER.Enum() },
		"to the environment": func(b *pb.StepUpdate_builder) {
			b.Target = pb.StepUpdate_TARGET_ENVIRONMENT.Enum()
		},
		"no text": func(b *pb.StepUpdate_builder) { b.Text = proto.String("") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if base(mutate).IsCompleteResponse {
				t.Error("the step is reported as a complete response")
			}
		})
	}
}

func TestStepFromProtoBuiltinToolCall(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(4),
		RunCommand: pb.ActionRunCommand_builder{
			CommandLine: proto.String("go test ./..."),
			WorkingDir:  proto.String("/repo"),
		}.Build(),
	}.Build())

	if step.Type != StepToolCall {
		t.Errorf("type = %q, want %q", step.Type, StepToolCall)
	}
	if len(step.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(step.ToolCalls))
	}
	call := step.ToolCalls[0]
	if call.Name != string(ToolRunCommand) {
		t.Errorf("name = %q, want %q", call.Name, ToolRunCommand)
	}
	// A built-in action carries no id of its own, so the step's stands in.
	if call.ID != "main:4" || call.StepID != "main:4" {
		t.Errorf("id = %q, step id = %q, want both main:4", call.ID, call.StepID)
	}

	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatalf("arguments are not an object: %v", err)
	}
	// Proto field names, matching what the Python SDK exposes.
	if args["command_line"] != "go test ./..." {
		t.Errorf("arguments = %v, want proto field names", args)
	}
}

func TestStepFromProtoMcpToolCall(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
		McpTool: pb.ActionMcpTool_builder{
			ServerName:    proto.String("weather"),
			ToolName:      proto.String("forecast"),
			ArgumentsJson: proto.String(`{"city":"Oslo"}`),
		}.Build(),
	}.Build())

	if len(step.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(step.ToolCalls))
	}
	call := step.ToolCalls[0]
	if call.Name != "forecast" || call.ServerName != "weather" {
		t.Errorf("call = %q on %q, want forecast on weather", call.Name, call.ServerName)
	}
	if string(call.Args) != `{"city":"Oslo"}` {
		t.Errorf("arguments = %s, want them passed through", call.Args)
	}
}

func TestStepFromProtoCustomToolCallKeepsItsID(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
		CustomTool: pb.ActionCustomTool_builder{
			ToolCall: pb.ToolCall_builder{
				Id:            proto.String("call-7"),
				Name:          proto.String("lookup"),
				ArgumentsJson: proto.String(`{"q":"go"}`),
			}.Build(),
		}.Build(),
	}.Build())

	if len(step.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(step.ToolCalls))
	}
	// The harness's own id is what the tool response has to be keyed by.
	if got := step.ToolCalls[0].ID; got != "call-7" {
		t.Errorf("id = %q, want call-7", got)
	}
}

func TestStepFromProtoMalformedArgumentsBecomeAnEmptyObject(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
		McpTool: pb.ActionMcpTool_builder{
			ToolName:      proto.String("broken"),
			ArgumentsJson: proto.String("not json"),
		}.Build(),
	}.Build())

	// A tool whose arguments will not parse still has to produce a usable
	// call rather than fail the step.
	if got := string(step.ToolCalls[0].Args); got != `{}` {
		t.Errorf("arguments = %s, want {}", got)
	}
}

func TestStepFromProtoNormalizesPathArguments(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
		McpTool: pb.ActionMcpTool_builder{
			ToolName:      proto.String("read"),
			ArgumentsJson: proto.String(`{"path":"file:///tmp/x.txt","other":1}`),
		}.Build(),
	}.Build())

	call := step.ToolCalls[0]
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "/tmp/x.txt" {
		t.Errorf("path = %v, want the native path", args["path"])
	}
	if args["other"] == nil {
		t.Error("rewriting the path dropped the other arguments")
	}
	if call.CanonicalPath != "/tmp/x.txt" {
		t.Errorf("CanonicalPath = %q, want /tmp/x.txt", call.CanonicalPath)
	}
}

func TestNormalizeArgPathsLeavesUnrelatedArgumentsAlone(t *testing.T) {
	in := json.RawMessage(`{"query":"file:///not/a/path/arg"}`)
	got, canonical := normalizeArgPaths(in)

	if string(got) != string(in) {
		t.Errorf("arguments = %s, want them untouched", got)
	}
	if canonical != "" {
		t.Errorf("CanonicalPath = %q, want none", canonical)
	}
}

func TestNormalizeArgPathsIgnoresNonObjects(t *testing.T) {
	in := json.RawMessage(`"just a string"`)
	got, canonical := normalizeArgPaths(in)

	if string(got) != string(in) || canonical != "" {
		t.Errorf("normalizeArgPaths = %s, %q; want the input unchanged", got, canonical)
	}
}

func TestStepTypePrecedence(t *testing.T) {
	// A finish step also carries text, and a compaction step may carry both;
	// the more specific classification has to win.
	finish := stepFromProto(pb.StepUpdate_builder{
		Text:   proto.String("summary"),
		Finish: pb.ActionFinish_builder{OutputString: proto.String(`{"a":1}`)}.Build(),
	}.Build())
	if finish.Type != StepFinish {
		t.Errorf("type = %q, want %q", finish.Type, StepFinish)
	}
	if string(finish.StructuredOutput) != `{"a":1}` {
		t.Errorf("structured output = %s", finish.StructuredOutput)
	}

	compaction := stepFromProto(pb.StepUpdate_builder{
		Text:       proto.String("summary"),
		Compaction: pb.ActionCompaction_builder{}.Build(),
	}.Build())
	if compaction.Type != StepCompaction {
		t.Errorf("type = %q, want %q", compaction.Type, StepCompaction)
	}

	thinking := stepFromProto(pb.StepUpdate_builder{Thinking: proto.String("hmm")}.Build())
	if thinking.Type != StepThinking {
		t.Errorf("type = %q, want %q", thinking.Type, StepThinking)
	}
}

func TestStructuredOutputRejectsNonJSON(t *testing.T) {
	// The harness may finish with a plain string; that is not structured
	// output, and it must not fail the step.
	step := stepFromProto(pb.StepUpdate_builder{
		Finish: pb.ActionFinish_builder{OutputString: proto.String("all done")}.Build(),
	}.Build())

	if step.StructuredOutput != nil {
		t.Errorf("StructuredOutput = %s, want nil", step.StructuredOutput)
	}
}

func TestStepFromProtoCarriesAnError(t *testing.T) {
	step := stepFromProto(pb.StepUpdate_builder{
		State: pb.StepUpdate_STATE_ERROR.Enum(),
		Error: pb.ActionError_builder{
			ErrorMessage: proto.String("upstream refused"),
			HttpCode:     proto.Uint32(429),
		}.Build(),
	}.Build())

	if step.Error != "upstream refused" || step.HTTPCode != 429 {
		t.Errorf("error = %q (%d), want the reported failure", step.Error, step.HTTPCode)
	}
	if step.Status != StatusError {
		t.Errorf("status = %q, want %q", step.Status, StatusError)
	}
}

func TestUsageFromProtoDistinguishesZeroFromAbsent(t *testing.T) {
	if got := usageFromProto(nil); got != nil {
		t.Errorf("usageFromProto(nil) = %v, want nil", got)
	}

	// An empty message reports nothing, not zeros with meaning behind them.
	empty := usageFromProto(pb.UsageMetadata_builder{}.Build())
	if empty == nil || *empty != (UsageMetadata{}) {
		t.Errorf("usage = %v, want the zero value", empty)
	}

	full := usageFromProto(pb.UsageMetadata_builder{
		PromptTokenCount:        proto.Uint64(10),
		CachedContentTokenCount: proto.Uint64(4),
		CandidatesTokenCount:    proto.Uint64(20),
		ThoughtsTokenCount:      proto.Uint64(5),
		TotalTokenCount:         proto.Uint64(35),
		ServiceTier:             proto.String("priority"),
	}.Build())
	want := UsageMetadata{
		PromptTokenCount:        10,
		CachedContentTokenCount: 4,
		CandidatesTokenCount:    20,
		ThoughtsTokenCount:      5,
		TotalTokenCount:         35,
		ServiceTier:             ServiceTierPriority,
	}
	if *full != want {
		t.Errorf("usage = %+v, want %+v", *full, want)
	}
}

func TestStepsFromHistory(t *testing.T) {
	if got := stepsFromHistory(nil); got != nil {
		t.Errorf("stepsFromHistory(nil) = %v, want nil", got)
	}

	steps := stepsFromHistory([]*pb.StepUpdate{
		pb.StepUpdate_builder{TrajectoryId: proto.String("main"), StepIndex: proto.Uint32(0)}.Build(),
		pb.StepUpdate_builder{TrajectoryId: proto.String("main"), StepIndex: proto.Uint32(1)}.Build(),
	})
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[1].ID != "main:1" {
		t.Errorf("second step id = %q, want main:1", steps[1].ID)
	}
}
