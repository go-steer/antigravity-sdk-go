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
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// callHook pushes a hook request and returns the response the SDK sends back.
//
// The harness pauses the agent until that response arrives, so every path has
// to produce exactly one.
func callHook(t *testing.T, s *session, req *pb.CallHookRequest) *pb.CallHookResponse {
	t.Helper()

	if req.GetRequestId() == "" {
		req.SetRequestId("req-1")
	}
	s.fake.Push(pb.OutputEvent_builder{CallHookRequest: req}.Build())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ev, err := s.fake.WaitSent(ctx)
	if err != nil {
		t.Fatalf("the hook was never answered: %v", err)
	}
	if !ev.HasCallHookResponse() {
		t.Fatalf("the SDK sent %v, want a hook response", ev)
	}
	resp := ev.GetCallHookResponse()
	if got := resp.GetRequestId(); got != req.GetRequestId() {
		t.Errorf("request id = %q, want %q", got, req.GetRequestId())
	}
	return resp
}

func TestHookSessionLifecycle(t *testing.T) {
	var started, ended bool
	s := newSession(t, func(h *hookRunner) {
		h.sessionStart = []SessionHook{
			func(context.Context, *HookContext) error { started = true; return nil },
		}
		h.sessionEnd = []SessionHook{
			func(context.Context, *HookContext) error { ended = true; return nil },
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_START.Enum(),
	}.Build())
	if !resp.HasEmptyResult() {
		t.Errorf("session start answered with %v, want an empty result", resp)
	}
	if !started {
		t.Error("the session start hook did not run")
	}

	callHook(t, s, pb.CallHookRequest_builder{
		RequestId: proto.String("req-2"),
		Type:      pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_END.Enum(),
	}.Build())
	if !ended {
		t.Error("the session end hook did not run")
	}
}

func TestHookPreTurnAllows(t *testing.T) {
	var seen []Content
	s := newSession(t, func(h *hookRunner) {
		h.preTurn = []PreTurnHook{
			func(_ context.Context, _ *HookContext, prompt []Content) (TurnDecision, error) {
				seen = prompt
				return TurnDecision{}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN.Enum(),
		PreTurnArgs: pb.PreTurnArgs_builder{
			UserInput: pb.UserInput_builder{Parts: []*pb.UserInput_Part{
				pb.UserInput_Part_builder{Text: proto.String("hello")}.Build(),
			}}.Build(),
		}.Build(),
	}.Build())

	if got := resp.GetPreTurnResult().GetDecision(); got != pb.PreTurnResult_ALLOW {
		t.Errorf("decision = %v, want ALLOW", got)
	}
	if len(seen) != 1 || seen[0] != Text("hello") {
		t.Errorf("the hook saw %v, want the user's text", seen)
	}
}

func TestHookPreTurnDenies(t *testing.T) {
	s := newSession(t, func(h *hookRunner) {
		h.preTurn = []PreTurnHook{
			func(context.Context, *HookContext, []Content) (TurnDecision, error) {
				return TurnDecision{Deny: true, Reason: "out of scope"}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type:        pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN.Enum(),
		PreTurnArgs: pb.PreTurnArgs_builder{}.Build(),
	}.Build())

	result := resp.GetPreTurnResult()
	if result.GetDecision() != pb.PreTurnResult_DENY {
		t.Errorf("decision = %v, want DENY", result.GetDecision())
	}
	if result.GetReason() != "out of scope" {
		t.Errorf("reason = %q, want the hook's", result.GetReason())
	}
}

func TestHookPreTurnConvertsEveryContentKind(t *testing.T) {
	var seen []Content
	s := newSession(t, func(h *hookRunner) {
		h.preTurn = []PreTurnHook{
			func(_ context.Context, _ *HookContext, prompt []Content) (TurnDecision, error) {
				seen = prompt
				return TurnDecision{}, nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN.Enum(),
		PreTurnArgs: pb.PreTurnArgs_builder{
			UserInput: pb.UserInput_builder{Parts: []*pb.UserInput_Part{
				pb.UserInput_Part_builder{Text: proto.String("look")}.Build(),
				pb.UserInput_Part_builder{
					SlashCommand: pb.UserInput_SlashCommand_builder{Name: proto.String("plan")}.Build(),
				}.Build(),
				pb.UserInput_Part_builder{
					Media: pb.UserInput_Media_builder{
						MimeType: proto.String("image/png"),
						Data:     []byte{0x89, 'P', 'N', 'G'},
					}.Build(),
				}.Build(),
				// An empty part represents nothing the SDK can hand a hook.
				pb.UserInput_Part_builder{}.Build(),
			}}.Build(),
		}.Build(),
	}.Build())

	if len(seen) != 3 {
		t.Fatalf("the hook saw %d parts, want 3 with the unrepresentable one dropped: %v", len(seen), seen)
	}
	if _, ok := seen[1].(SlashCommand); !ok {
		t.Errorf("part 1 is %T, want a SlashCommand", seen[1])
	}
	if _, ok := seen[2].(Media); !ok {
		t.Errorf("part 2 is %T, want Media", seen[2])
	}
}

func TestHookPostTurnReceivesTheResponse(t *testing.T) {
	var got string
	s := newSession(t, func(h *hookRunner) {
		h.postTurn = []PostTurnHook{
			func(_ context.Context, _ *HookContext, response string) error {
				got = response
				return nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type:         pb.LifecycleHook_LIFECYCLE_HOOK_POST_TURN.Enum(),
		PostTurnArgs: pb.PostTurnArgs_builder{ResponseText: proto.String("the answer")}.Build(),
	}.Build())

	if !resp.HasEmptyResult() {
		t.Errorf("answered with %v, want an empty result", resp)
	}
	if got != "the answer" {
		t.Errorf("the hook saw %q, want the response text", got)
	}
}

func TestHookPreToolAllowsAndRewritesArguments(t *testing.T) {
	var seen ToolCall
	s := newSession(t, func(h *hookRunner) {
		h.preTool = []PreToolCallHook{
			func(_ context.Context, _ *HookContext, call ToolCall) (ToolDecision, error) {
				seen = call
				return ToolDecision{ModifiedArgs: map[string]any{"command_line": "go test ./..."}}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TOOL.Enum(),
		PreToolArgs: pb.PreToolArgs_builder{
			ToolName:      proto.String("run_command"),
			ArgumentsJson: proto.String(`{"command_line":"rm -rf /","path":"file:///tmp/x"}`),
			CallId:        proto.String("call-1"),
			TrajectoryId:  proto.String("main"),
			StepIndex:     proto.Uint32(2),
		}.Build(),
	}.Build())

	result := resp.GetPreToolResult()
	if result.GetDecision() != pb.PreToolResult_ALLOW {
		t.Fatalf("decision = %v, want ALLOW", result.GetDecision())
	}

	var rewritten map[string]any
	if err := json.Unmarshal([]byte(result.GetModifiedArgumentsJson()), &rewritten); err != nil {
		t.Fatalf("the rewritten arguments do not parse: %v", err)
	}
	if rewritten["command_line"] != "go test ./..." {
		t.Errorf("arguments = %v, want the hook's rewrite", rewritten)
	}

	// The hook sees native paths, not wire URIs.
	if seen.CanonicalPath != "/tmp/x" {
		t.Errorf("CanonicalPath = %q, want /tmp/x", seen.CanonicalPath)
	}
	if seen.ID != "call-1" || seen.StepID != "main:2" {
		t.Errorf("call id = %q, step id = %q", seen.ID, seen.StepID)
	}
}

func TestHookPreToolDenies(t *testing.T) {
	s := newSession(t, func(h *hookRunner) {
		h.preTool = []PreToolCallHook{
			func(context.Context, *HookContext, ToolCall) (ToolDecision, error) {
				return ToolDecision{Deny: true, Reason: "too destructive"}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TOOL.Enum(),
		PreToolArgs: pb.PreToolArgs_builder{
			ToolName: proto.String("run_command"),
		}.Build(),
	}.Build())

	result := resp.GetPreToolResult()
	if result.GetDecision() != pb.PreToolResult_DENY {
		t.Errorf("decision = %v, want DENY", result.GetDecision())
	}
	if result.GetReason() != "too destructive" {
		t.Errorf("reason = %q, want the hook's", result.GetReason())
	}
}

func TestHookPreToolTranslatesTheSubagentToolName(t *testing.T) {
	var got string
	s := newSession(t, func(h *hookRunner) {
		h.preTool = []PreToolCallHook{
			func(_ context.Context, _ *HookContext, call ToolCall) (ToolDecision, error) {
				got = call.Name
				return ToolDecision{}, nil
			},
		}
	})

	// The wire calls it invoke_subagent; the SDK's own name is what policies
	// and hooks are written against.
	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TOOL.Enum(),
		PreToolArgs: pb.PreToolArgs_builder{
			ToolName: proto.String("invoke_subagent"),
		}.Build(),
	}.Build())

	if got != string(ToolStartSubagent) {
		t.Errorf("tool = %q, want %q", got, ToolStartSubagent)
	}
}

func TestHookPostToolDecodesTheResult(t *testing.T) {
	var got ToolResult
	s := newSession(t, func(h *hookRunner) {
		h.postTool = []PostToolCallHook{
			func(_ context.Context, _ *HookContext, result ToolResult) error {
				got = result
				return nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_POST_TOOL.Enum(),
		PostToolArgs: pb.PostToolArgs_builder{
			ToolName:     proto.String("run_command"),
			Result:       proto.String(`{"output":"ok\n"}`),
			CallId:       proto.String("call-1"),
			TrajectoryId: proto.String("main"),
			StepIndex:    proto.Uint32(1),
		}.Build(),
	}.Build())

	if got.Err != nil {
		t.Fatalf("the result carries an error: %v", got.Err)
	}
	if want := (RunCommandResult{Output: "ok\n"}); got.Result != want {
		t.Errorf("result = %#v, want %#v", got.Result, want)
	}
	if got.ID != "call-1" || got.StepID != "main:1" {
		t.Errorf("call id = %q, step id = %q", got.ID, got.StepID)
	}
}

func TestHookPostToolPassesUnstructuredResultsThrough(t *testing.T) {
	var got ToolResult
	s := newSession(t, func(h *hookRunner) {
		h.postTool = []PostToolCallHook{
			func(_ context.Context, _ *HookContext, result ToolResult) error {
				got = result
				return nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_POST_TOOL.Enum(),
		PostToolArgs: pb.PostToolArgs_builder{
			ToolName:   proto.String("weather"),
			ServerName: proto.String("mcp-weather"),
			Result:     proto.String("sunny"),
		}.Build(),
	}.Build())

	if got.Result != "sunny" {
		t.Errorf("result = %#v, want the raw text", got.Result)
	}
	if got.ServerName != "mcp-weather" {
		t.Errorf("server = %q, want mcp-weather", got.ServerName)
	}
}

func TestHookPostToolReportsAFailure(t *testing.T) {
	var got ToolResult
	s := newSession(t, func(h *hookRunner) {
		h.postTool = []PostToolCallHook{
			func(_ context.Context, _ *HookContext, result ToolResult) error {
				got = result
				return nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_POST_TOOL.Enum(),
		PostToolArgs: pb.PostToolArgs_builder{
			ToolName: proto.String("run_command"),
			Result:   proto.String(`{"output":"partial"}`),
			Error:    proto.String("exit status 1"),
		}.Build(),
	}.Build())

	if got.Err == nil {
		t.Fatal("a failed tool reported no error")
	}
	var toolErr *ToolError
	if !errors.As(got.Err, &toolErr) {
		t.Fatalf("error = %T, want *ToolError", got.Err)
	}
	if toolErr.ToolName != "run_command" {
		t.Errorf("error = %v on %q, want it attributed to run_command", toolErr, toolErr.ToolName)
	}
	// A failure has no usable result, even when the harness sends one.
	if got.Result != nil {
		t.Errorf("result = %#v, want none alongside the error", got.Result)
	}
}

func TestHookToolErrorRewordsTheMessage(t *testing.T) {
	var seen *ToolError
	s := newSession(t, func(h *hookRunner) {
		h.toolError = []OnToolErrorHook{
			func(_ context.Context, _ *HookContext, err *ToolError) (string, error) {
				seen = err
				return "try a narrower search", nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_ON_TOOL_ERROR.Enum(),
		OnToolErrorArgs: pb.OnToolErrorArgs_builder{
			ToolName:     proto.String("search_directory"),
			ErrorMessage: proto.String("too many results"),
			CallId:       proto.String("call-9"),
		}.Build(),
	}.Build())

	if got := resp.GetOnToolErrorResult().GetCustomErrorMessage(); got != "try a narrower search" {
		t.Errorf("custom message = %q, want the hook's", got)
	}
	if seen == nil || seen.Err.Error() != "too many results" {
		t.Errorf("the hook saw %v, want the harness's message", seen)
	}
}

func TestHookToolErrorSuppliesADefaultMessage(t *testing.T) {
	var seen *ToolError
	s := newSession(t, func(h *hookRunner) {
		h.toolError = []OnToolErrorHook{
			func(_ context.Context, _ *HookContext, err *ToolError) (string, error) {
				seen = err
				return "", nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type:            pb.LifecycleHook_LIFECYCLE_HOOK_ON_TOOL_ERROR.Enum(),
		OnToolErrorArgs: pb.OnToolErrorArgs_builder{ToolName: proto.String("run_command")}.Build(),
	}.Build())

	// Returning nothing leaves the harness's own message in place.
	if !resp.HasEmptyResult() {
		t.Errorf("answered with %v, want an empty result", resp)
	}
	if seen == nil || seen.Err == nil || seen.Err.Error() == "" {
		t.Error("the hook received an error with no message at all")
	}
}

func TestHookCompactionSynthesizesAStep(t *testing.T) {
	var got Step
	s := newSession(t, func(h *hookRunner) {
		h.compaction = []OnCompactionHook{
			func(_ context.Context, _ *HookContext, step Step) error {
				got = step
				return nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_ON_COMPACTION.Enum(),
		OnCompactionArgs: pb.OnCompactionArgs_builder{
			TrajectoryId: proto.String("main"),
			StepIndex:    proto.Uint32(12),
			Summary:      proto.String("we discussed the parser"),
		}.Build(),
	}.Build())

	if got.Type != StepCompaction || got.ID != "main:12" || got.Index != 12 {
		t.Errorf("step = %+v, want a compaction at main:12", got)
	}
	if got.Content != "we discussed the parser" {
		t.Errorf("content = %q, want the summary", got.Content)
	}
}

func TestHookCompactionWithoutASummary(t *testing.T) {
	var got Step
	s := newSession(t, func(h *hookRunner) {
		h.compaction = []OnCompactionHook{
			func(_ context.Context, _ *HookContext, step Step) error {
				got = step
				return nil
			},
		}
	})

	callHook(t, s, pb.CallHookRequest_builder{
		Type:             pb.LifecycleHook_LIFECYCLE_HOOK_ON_COMPACTION.Enum(),
		OnCompactionArgs: pb.OnCompactionArgs_builder{TrajectoryId: proto.String("main")}.Build(),
	}.Build())

	if got.Content == "" {
		t.Error("a compaction with no summary produced an empty step")
	}
}

func TestHookStopContinues(t *testing.T) {
	var seen StopArgs
	s := newSession(t, func(h *hookRunner) {
		h.stop = []StopHook{
			func(_ context.Context, _ *HookContext, args StopArgs) (StopDecision, error) {
				seen = args
				return StopDecision{Continue: true, Reason: "the tests still fail"}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_STOP.Enum(),
		StopArgs: pb.StopArgs_builder{
			ResponseText:      proto.String("all set"),
			TrajectoryId:      proto.String("main"),
			ContinuationCount: proto.Int32(2),
		}.Build(),
	}.Build())

	result := resp.GetStopResult()
	if result.GetDecision() != pb.StopResult_CONTINUE {
		t.Errorf("decision = %v, want CONTINUE", result.GetDecision())
	}
	if result.GetReason() != "the tests still fail" {
		t.Errorf("reason = %q, want the hook's", result.GetReason())
	}
	if seen.Response != "all set" || seen.TrajectoryID != "main" || seen.ContinuationCount != 2 {
		t.Errorf("the hook saw %+v", seen)
	}
}

func TestHookStopAllowsStopping(t *testing.T) {
	s := newSession(t, func(h *hookRunner) {
		h.stop = []StopHook{
			func(context.Context, *HookContext, StopArgs) (StopDecision, error) {
				return StopDecision{}, nil
			},
		}
	})

	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type:     pb.LifecycleHook_LIFECYCLE_HOOK_STOP.Enum(),
		StopArgs: pb.StopArgs_builder{}.Build(),
	}.Build())

	if got := resp.GetStopResult().GetDecision(); got != pb.StopResult_ALLOW_STOP {
		t.Errorf("decision = %v, want ALLOW_STOP", got)
	}
}

func TestHookPanicIsReportedNotCrashed(t *testing.T) {
	s := newSession(t, func(h *hookRunner) {
		h.preTurn = []PreTurnHook{
			func(context.Context, *HookContext, []Content) (TurnDecision, error) {
				panic("kaboom")
			},
		}
	})

	// The harness is blocked on a reply, so a panic has to become one.
	callHook(t, s, pb.CallHookRequest_builder{
		Name:        proto.String("my_hook"),
		Type:        pb.LifecycleHook_LIFECYCLE_HOOK_PRE_TURN.Enum(),
		PreTurnArgs: pb.PreTurnArgs_builder{}.Build(),
	}.Build())

	// And the session has to survive it.
	resp := callHook(t, s, pb.CallHookRequest_builder{
		RequestId:    proto.String("req-2"),
		Type:         pb.LifecycleHook_LIFECYCLE_HOOK_POST_TURN.Enum(),
		PostTurnArgs: pb.PostTurnArgs_builder{}.Build(),
	}.Build())
	if !resp.HasEmptyResult() {
		t.Errorf("the next hook answered with %v, want an empty result", resp)
	}
}

func TestHookUnknownTypeIsStillAnswered(t *testing.T) {
	s := newSession(t)

	// An unrecognized hook must not leave the harness waiting forever.
	resp := callHook(t, s, pb.CallHookRequest_builder{
		Type: pb.LifecycleHook_LIFECYCLE_HOOK_UNSPECIFIED.Enum(),
		Name: proto.String("something_new"),
	}.Build())

	if !resp.HasEmptyResult() {
		t.Errorf("answered with %v, want an empty result", resp)
	}
}

func TestStepIDFrom(t *testing.T) {
	if got := stepIDFrom("", 0); got != "" {
		t.Errorf("stepIDFrom identified a step as %q, want none", got)
	}
	if got := stepIDFrom("main", 0); got != "main:0" {
		t.Errorf("stepIDFrom = %q, want main:0", got)
	}
	if got := stepIDFrom("", 3); got != "3" {
		t.Errorf("stepIDFrom = %q, want 3", got)
	}
}

func TestToolResultFromArgsKeepsTheErrorIdentity(t *testing.T) {
	result := toolResultFromArgs(pb.PostToolArgs_builder{
		ToolName:     proto.String("invoke_subagent"),
		Error:        proto.String("the subagent failed"),
		CallId:       proto.String("call-3"),
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(4),
	}.Build())

	var toolErr *ToolError
	if !errors.As(result.Err, &toolErr) {
		t.Fatalf("error = %T, want *ToolError", result.Err)
	}
	if toolErr.ToolName != string(ToolStartSubagent) {
		t.Errorf("tool = %q, want %q", toolErr.ToolName, ToolStartSubagent)
	}
	if toolErr.CallID != "call-3" || toolErr.StepID != "main:4" {
		t.Errorf("call id = %q, step id = %q", toolErr.CallID, toolErr.StepID)
	}
}
