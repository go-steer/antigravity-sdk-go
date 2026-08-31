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
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// The harness drives hooks: it pauses the agent, sends a CallHookRequest, and
// waits for the matching CallHookResponse. Every path through this file
// therefore sends exactly one response, including the failure paths — a
// dropped reply would hang the turn.

// handleHookRequest dispatches one hook callback and answers it.
func (p *eventProcessor) handleHookRequest(ctx context.Context, req *pb.CallHookRequest) {
	resp := p.runHook(ctx, req)
	resp.SetRequestId(req.GetRequestId())
	p.send(ctx, pb.InputEvent_builder{CallHookResponse: resp}.Build())
}

// runHook invokes the handler for a request, converting a panic in user code
// into an error the harness can report rather than a crashed session.
func (p *eventProcessor) runHook(ctx context.Context, req *pb.CallHookRequest) (resp *pb.CallHookResponse) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("a hook panicked", "hook", req.GetName(), "panic", r)
			resp = pb.CallHookResponse_builder{
				ErrorMessage: proto.String(fmt.Sprintf("hook %q panicked: %v", req.GetName(), r)),
			}.Build()
		}
	}()

	if p.hooks == nil {
		return emptyHookResult()
	}

	switch req.GetType() {
	case pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_START:
		p.hooks.dispatchSessionStart(ctx)
		return emptyHookResult()

	case pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_END:
		p.hooks.dispatchSessionEnd(ctx)
		return emptyHookResult()

	case pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN:
		return p.hookPreTurn(ctx, req.GetPreTurnArgs())

	case pb.LifecycleHook_LIFECYCLE_HOOK_POST_TURN:
		p.hooks.dispatchPostTurn(ctx, req.GetPostTurnArgs().GetResponseText())
		return emptyHookResult()

	case pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TOOL:
		return p.hookPreTool(ctx, req.GetPreToolArgs())

	case pb.LifecycleHook_LIFECYCLE_HOOK_POST_TOOL:
		p.hooks.dispatchPostToolCall(ctx, toolResultFromArgs(req.GetPostToolArgs()))
		return emptyHookResult()

	case pb.LifecycleHook_LIFECYCLE_HOOK_ON_TOOL_ERROR:
		return p.hookToolError(ctx, req.GetOnToolErrorArgs())

	case pb.LifecycleHook_LIFECYCLE_HOOK_ON_COMPACTION:
		p.hooks.dispatchCompaction(ctx, compactionStep(req.GetOnCompactionArgs()))
		return emptyHookResult()

	case pb.LifecycleHook_LIFECYCLE_HOOK_STOP:
		return p.hookStop(ctx, req.GetStopArgs())

	default:
		p.logger.Warn("ignoring an unrecognized hook request",
			"type", req.GetType(), "hook", req.GetName())
		return emptyHookResult()
	}
}

func emptyHookResult() *pb.CallHookResponse {
	return pb.CallHookResponse_builder{EmptyResult: pb.EmptyResult_builder{}.Build()}.Build()
}

// ---------------------------------------------------------------------------
// Per-hook handlers
// ---------------------------------------------------------------------------

func (p *eventProcessor) hookPreTurn(ctx context.Context, args *pb.PreTurnArgs) *pb.CallHookResponse {
	decision := p.hooks.dispatchPreTurn(ctx, contentFromUserInput(args.GetUserInput()))

	result := pb.PreTurnResult_builder{Decision: pb.PreTurnResult_ALLOW.Enum()}.Build()
	if decision.Deny {
		result = pb.PreTurnResult_builder{
			Decision: pb.PreTurnResult_DENY.Enum(),
			Reason:   proto.String(decision.Reason),
		}.Build()
	}
	return pb.CallHookResponse_builder{PreTurnResult: result}.Build()
}

func (p *eventProcessor) hookPreTool(ctx context.Context, args *pb.PreToolArgs) *pb.CallHookResponse {
	call := toolCallFromArgs(args)
	decision := p.hooks.dispatchPreToolCall(ctx, call)

	if decision.Deny {
		return pb.CallHookResponse_builder{
			PreToolResult: pb.PreToolResult_builder{
				Decision: pb.PreToolResult_DENY.Enum(),
				Reason:   proto.String(decision.Reason),
			}.Build(),
		}.Build()
	}

	allow := pb.PreToolResult_builder{Decision: pb.PreToolResult_ALLOW.Enum()}
	if len(decision.ModifiedArgs) > 0 {
		encoded, err := marshalArgsMap(decision.ModifiedArgs)
		if err != nil {
			// The call is still allowed; only the rewrite is lost, which is
			// better than blocking a tool over an encoding slip.
			p.logger.Error("dropping modified tool arguments that could not be encoded",
				"tool", call.Name, "error", err)
		} else {
			allow.ModifiedArgumentsJson = proto.String(string(encoded))
		}
	}
	return pb.CallHookResponse_builder{PreToolResult: allow.Build()}.Build()
}

func (p *eventProcessor) hookToolError(ctx context.Context, args *pb.OnToolErrorArgs) *pb.CallHookResponse {
	message := args.GetErrorMessage()
	if message == "" {
		message = "the tool failed"
	}
	toolErr := &ToolError{
		ToolName:   sdkToolName(args.GetToolName()),
		ServerName: args.GetServerName(),
		CallID:     args.GetCallId(),
		StepID:     stepIDFrom(args.GetTrajectoryId(), args.GetStepIndex()),
		Err:        errors.New(message),
	}

	if replacement := p.hooks.dispatchToolError(ctx, toolErr); replacement != "" {
		return pb.CallHookResponse_builder{
			OnToolErrorResult: pb.OnToolErrorResult_builder{
				CustomErrorMessage: proto.String(replacement),
			}.Build(),
		}.Build()
	}
	return emptyHookResult()
}

func (p *eventProcessor) hookStop(ctx context.Context, args *pb.StopArgs) *pb.CallHookResponse {
	decision := p.hooks.dispatchStop(ctx, StopArgs{
		Response:          args.GetResponseText(),
		TrajectoryID:      args.GetTrajectoryId(),
		ContinuationCount: int(args.GetContinuationCount()),
		StopReason:        stopReasonFromProto(args.GetStopReason()),
		Error:             args.GetErrorMessage(),
	})

	if decision.Continue {
		return pb.CallHookResponse_builder{
			StopResult: pb.StopResult_builder{
				Decision: pb.StopResult_CONTINUE.Enum(),
				Reason:   proto.String(decision.Reason),
			}.Build(),
		}.Build()
	}
	return pb.CallHookResponse_builder{
		StopResult: pb.StopResult_builder{Decision: pb.StopResult_ALLOW_STOP.Enum()}.Build(),
	}.Build()
}

// ---------------------------------------------------------------------------
// Wire conversions
// ---------------------------------------------------------------------------

// toolCallFromArgs rebuilds a [ToolCall] from a pre-tool request, translating
// wire URIs so hooks see native paths.
func toolCallFromArgs(args *pb.PreToolArgs) ToolCall {
	raw := rawArgsOrEmpty(args.GetArgumentsJson())
	normalized, canonical := normalizeArgPaths(raw)
	return ToolCall{
		Name:          sdkToolName(args.GetToolName()),
		Args:          normalized,
		ID:            args.GetCallId(),
		StepID:        stepIDFrom(args.GetTrajectoryId(), args.GetStepIndex()),
		CanonicalPath: canonical,
		ServerName:    args.GetServerName(),
	}
}

// toolResultFromArgs rebuilds a [ToolResult] from a post-tool request,
// decoding a built-in tool's payload into its typed form where one exists.
func toolResultFromArgs(args *pb.PostToolArgs) ToolResult {
	name := sdkToolName(args.GetToolName())

	result := ToolResult{
		Name:       name,
		ID:         args.GetCallId(),
		StepID:     stepIDFrom(args.GetTrajectoryId(), args.GetStepIndex()),
		ServerName: args.GetServerName(),
	}
	if msg := args.GetError(); msg != "" {
		result.Err = &ToolError{
			ToolName:   name,
			ServerName: result.ServerName,
			CallID:     result.ID,
			StepID:     result.StepID,
			Err:        errors.New(msg),
		}
		return result
	}

	raw := args.GetResult()
	if parsed, ok := parseBuiltinToolResult(name, raw); ok {
		result.Result = parsed
	} else if raw != "" {
		result.Result = raw
	}
	return result
}

// compactionStep synthesizes the [Step] a compaction hook receives. The
// harness reports compaction as hook arguments rather than a step update, so
// the step is assembled here.
func compactionStep(args *pb.OnCompactionArgs) Step {
	summary := args.GetSummary()
	if summary == "" {
		summary = "Context compaction"
	}
	return Step{
		ID:           makeStepID(args.GetTrajectoryId(), args.GetStepIndex()),
		Index:        int(args.GetStepIndex()),
		TrajectoryID: args.GetTrajectoryId(),
		Type:         StepCompaction,
		Source:       SourceSystem,
		Target:       TargetUser,
		Status:       StatusDone,
		Content:      summary,
	}
}

// contentFromUserInput converts wire parts back into public content, dropping
// any part it cannot represent rather than failing the hook.
func contentFromUserInput(ui *pb.UserInput) []Content {
	var out []Content
	for _, part := range ui.GetParts() {
		switch {
		case part.HasText():
			out = append(out, Text(part.GetText()))

		case part.HasSlashCommand():
			out = append(out, SlashCommand{Name: BuiltinSlashCommand(part.GetSlashCommand().GetName())})

		case part.HasMedia():
			m := part.GetMedia()
			media, err := FromBytes(m.GetData(), m.GetMimeType(), m.GetDescription())
			if err != nil {
				continue
			}
			out = append(out, media)
		}
	}
	return out
}

// stepIDFrom builds a step id, returning empty when the request identified no
// step at all.
func stepIDFrom(trajectoryID string, index uint32) string {
	if trajectoryID == "" && index == 0 {
		return ""
	}
	return makeStepID(trajectoryID, index)
}

// sdkToolName translates the harness's proto field name for a built-in action
// into the SDK's tool name. The two agree everywhere except the subagent tool.
func sdkToolName(wireName string) string {
	if wireName == "invoke_subagent" {
		return string(ToolStartSubagent)
	}
	return wireName
}

// marshalArgsMap encodes a hook's rewritten tool arguments.
func marshalArgsMap(args map[string]any) (json.RawMessage, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return data, nil
}
