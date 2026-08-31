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
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// waitSent returns the next event the SDK writes back to the harness.
func waitSent(t *testing.T, fake *harness.FakeTransport) *pb.InputEvent {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	ev, err := fake.WaitSent(ctx)
	if err != nil {
		t.Fatalf("the SDK never answered the harness: %v", err)
	}
	return ev
}

// ---------------------------------------------------------------------------
// Custom tool calls
// ---------------------------------------------------------------------------

// pushToolCall injects a call to a client-side tool.
func (s *session) pushToolCall(id, name, args string) {
	s.fake.Push(pb.OutputEvent_builder{
		ToolCall: pb.ToolCall_builder{
			Id:            proto.String(id),
			Name:          proto.String(name),
			ArgumentsJson: proto.String(args),
		}.Build(),
	}.Build())
}

func TestToolCallRunsTheToolAndReturnsItsResult(t *testing.T) {
	type args struct {
		City string `json:"city"`
	}
	tool, err := NewTool("weather", "reports the weather",
		func(_ context.Context, a args) (map[string]any, error) {
			return map[string]any{"city": a.City, "temp": 21}, nil
		})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"weather": tool}, nil)
	s.pushToolCall("call-1", "weather", `{"city":"Oslo"}`)

	resp := waitSent(t, s.fake).GetToolResponse()
	if resp.GetId() != "call-1" {
		t.Errorf("response id = %q, want the call's id", resp.GetId())
	}
	if resp.GetErrorMessage() != "" {
		t.Errorf("error = %q, want none", resp.GetErrorMessage())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(resp.GetResponseJson()), &got); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if got["city"] != "Oslo" {
		t.Errorf("response = %v, want the tool's own result", got)
	}
}

func TestToolCallIsAnnouncedAsAStep(t *testing.T) {
	tool, err := NewTool("noop", "does nothing", func(context.Context, struct{}) (string, error) {
		return "done", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"noop": tool}, nil)
	s.pushToolCall("call-1", "noop", `{}`)

	// A caller streaming the turn should see the dispatch, not just the result.
	select {
	case ev := <-s.proc.steps:
		if ev.step.Type != StepToolCall || len(ev.step.ToolCalls) != 1 {
			t.Fatalf("step = %+v, want a tool call", ev.step)
		}
		if ev.step.ToolCalls[0].Name != "noop" || ev.step.ToolCalls[0].ID != "call-1" {
			t.Errorf("tool call = %+v", ev.step.ToolCalls[0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatched tool call was never announced")
	}
}

func TestToolCallForAnUnregisteredToolReportsAnError(t *testing.T) {
	// The harness stalls until it gets a response, so even a call we cannot
	// route has to be answered.
	s := newSessionWith(t, map[string]Tool{}, nil)
	s.pushToolCall("call-1", "ghost", `{}`)

	resp := waitSent(t, s.fake).GetToolResponse()
	if !strings.Contains(resp.GetErrorMessage(), "ghost") {
		t.Errorf("error = %q, want it to name the missing tool", resp.GetErrorMessage())
	}
	if resp.GetResponseJson() != "" {
		t.Errorf("response = %q, want none alongside the error", resp.GetResponseJson())
	}
}

func TestToolCallFailureIsReportedToTheHarness(t *testing.T) {
	tool, err := NewTool("boom", "fails", func(context.Context, struct{}) (string, error) {
		return "", errors.New("the disk is on fire")
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"boom": tool}, nil)
	s.pushToolCall("call-1", "boom", `{}`)

	resp := waitSent(t, s.fake).GetToolResponse()
	if !strings.Contains(resp.GetErrorMessage(), "the disk is on fire") {
		t.Errorf("error = %q, want the tool's own message", resp.GetErrorMessage())
	}
}

func TestToolPanicBecomesAnError(t *testing.T) {
	// One misbehaving tool must not take the session down with it.
	tool, err := NewTool("panicky", "panics", func(context.Context, struct{}) (string, error) {
		panic("nope")
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"panicky": tool}, nil)
	s.pushToolCall("call-1", "panicky", `{}`)

	resp := waitSent(t, s.fake).GetToolResponse()
	if !strings.Contains(resp.GetErrorMessage(), "panicked") {
		t.Errorf("error = %q, want it to report the panic", resp.GetErrorMessage())
	}

	// The session still answers afterwards.
	s.pushToolCall("call-2", "panicky", `{}`)
	if got := waitSent(t, s.fake).GetToolResponse().GetId(); got != "call-2" {
		t.Errorf("second response id = %q, want call-2", got)
	}
}

func TestToolMediaBecomesAnAttachment(t *testing.T) {
	image, err := NewImage([]byte{0x89, 'P'}, "image/png", "a chart")
	if err != nil {
		t.Fatalf("NewImage: %v", err)
	}
	tool, err := NewTool("chart", "draws", func(context.Context, struct{}) (*Image, error) {
		return image, nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"chart": tool}, nil)
	s.pushToolCall("call-1", "chart", `{}`)

	resp := waitSent(t, s.fake).GetToolResponse()
	media := resp.GetSupplementalMedia()
	if len(media) != 1 {
		t.Fatalf("got %d attachments, want 1", len(media))
	}
	if media[0].GetMimeType() != "image/png" || media[0].GetDescription() != "a chart" {
		t.Errorf("attachment = %v", media[0])
	}
	// Media alone leaves no JSON result, so the model is told what it got.
	if !strings.Contains(resp.GetResponseJson(), "media attachment") {
		t.Errorf("response = %q, want a note about the attachment", resp.GetResponseJson())
	}
}

func TestStepUpdateSuppressesAClientToolCall(t *testing.T) {
	// The same call arrives twice, as a step update and as a tool_call event.
	// Only the latter drives execution, so the step must not double-report it.
	tool, err := NewTool("weather", "reports", func(context.Context, struct{}) (string, error) {
		return "sunny", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}

	s := newSessionWith(t, map[string]Tool{"weather": tool}, nil)
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId: proto.String("t1"),
		StepIndex:    proto.Uint32(0),
		State:        pb.StepUpdate_STATE_ACTIVE.Enum(),
		CustomTool: pb.ActionCustomTool_builder{
			ToolCall: pb.ToolCall_builder{
				Id:   proto.String("call-1"),
				Name: proto.String("weather"),
			}.Build(),
		}.Build(),
	}.Build())

	select {
	case ev := <-s.proc.steps:
		if len(ev.step.ToolCalls) != 0 {
			t.Errorf("ToolCalls = %v, want none: the tool_call event carries it", ev.step.ToolCalls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the step update never arrived")
	}
}

// ---------------------------------------------------------------------------
// Questions
// ---------------------------------------------------------------------------

// questionStep builds a step waiting on a multiple-choice question.
func questionStep(questions ...*pb.UserQuestion) *pb.StepUpdate {
	return pb.StepUpdate_builder{
		TrajectoryId: proto.String("t1"),
		StepIndex:    proto.Uint32(3),
		State:        pb.StepUpdate_STATE_WAITING_FOR_USER.Enum(),
		QuestionsRequest: pb.UserQuestionsRequest_builder{
			Questions: questions,
		}.Build(),
	}.Build()
}

func multipleChoice(text string, multi bool, choices ...string) *pb.UserQuestion {
	return pb.UserQuestion_builder{
		MultipleChoice: pb.MultipleChoice_builder{
			Question:      proto.String(text),
			Choices:       choices,
			IsMultiSelect: proto.Bool(multi),
		}.Build(),
	}.Build()
}

func TestQuestionsAreAnsweredByTheInteractionHook(t *testing.T) {
	var seen QuestionRequest
	s := newSessionWith(t, nil, nil, func(h *hookRunner) {
		h.interaction = []OnInteractionHook{
			func(_ context.Context, _ *HookContext, req QuestionRequest) (*QuestionAnswers, error) {
				seen = req
				return &QuestionAnswers{Answers: []Answer{{
					SelectedOptions: []int{1},
					Text:            "the second one",
				}}}, nil
			},
		}
	})

	s.pushStep(questionStep(multipleChoice("which?", false, "a", "b")))

	resp := waitSent(t, s.fake).GetQuestionResponse()
	if resp.GetTrajectoryId() != "t1" || resp.GetStepIndex() != 3 {
		t.Errorf("answer addressed to %q/%d, want t1/3", resp.GetTrajectoryId(), resp.GetStepIndex())
	}
	answers := resp.GetResponse().GetAnswers()
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	mc := answers[0].GetMultipleChoiceAnswer()
	if len(mc.GetSelectedChoiceIndices()) != 1 || mc.GetSelectedChoiceIndices()[0] != 1 {
		t.Errorf("selected = %v, want [1]", mc.GetSelectedChoiceIndices())
	}
	if mc.GetFreeformResponse() != "the second one" {
		t.Errorf("freeform = %q", mc.GetFreeformResponse())
	}

	if len(seen.Questions) != 1 || seen.Questions[0].Text != "which?" {
		t.Fatalf("the hook saw %+v", seen.Questions)
	}
	if len(seen.Questions[0].Options) != 2 || seen.Questions[0].Options[1].Text != "b" {
		t.Errorf("options = %+v, want both choices", seen.Questions[0].Options)
	}
}

func TestQuestionsAreAnsweredWithNoHookRegistered(t *testing.T) {
	// Silence would stall the turn, so an unanswerable question is answered as
	// unanswered.
	s := newSession(t)
	s.pushStep(questionStep(multipleChoice("which?", false, "a", "b")))

	answers := waitSent(t, s.fake).GetQuestionResponse().GetResponse().GetAnswers()
	if len(answers) != 1 || !answers[0].GetUnanswered() {
		t.Errorf("answers = %v, want one unanswered reply", answers)
	}
}

func TestQuestionAnswersLineUpWithQuestionsTheSDKUnderstands(t *testing.T) {
	// A question type the SDK cannot represent still occupies its slot, so the
	// hook's replies must not shift onto the wrong questions.
	s := newSessionWith(t, nil, nil, func(h *hookRunner) {
		h.interaction = []OnInteractionHook{
			func(context.Context, *HookContext, QuestionRequest) (*QuestionAnswers, error) {
				return &QuestionAnswers{Answers: []Answer{{SelectedOptions: []int{0}}}}, nil
			},
		}
	})

	s.pushStep(questionStep(
		pb.UserQuestion_builder{}.Build(), // an unrepresentable question
		multipleChoice("which?", false, "a", "b"),
	))

	answers := waitSent(t, s.fake).GetQuestionResponse().GetResponse().GetAnswers()
	if len(answers) != 2 {
		t.Fatalf("got %d answers, want one per question", len(answers))
	}
	if !answers[0].GetUnanswered() {
		t.Error("the unrepresentable question was answered")
	}
	if got := answers[1].GetMultipleChoiceAnswer().GetSelectedChoiceIndices(); len(got) != 1 || got[0] != 0 {
		t.Errorf("answers[1] = %v, want the hook's reply", got)
	}
}

func TestQuestionAnswerProto(t *testing.T) {
	question := Question{Options: []QuestionOption{{Text: "a"}, {Text: "b"}}}

	if got := questionAnswerProto(Answer{Skipped: true}, question); !got.GetUnanswered() {
		t.Error("a skipped answer was not reported as unanswered")
	}

	// Indices outside the question asked would misrepresent the reply.
	got := questionAnswerProto(Answer{SelectedOptions: []int{-1, 0, 5}}, question)
	if idx := got.GetMultipleChoiceAnswer().GetSelectedChoiceIndices(); len(idx) != 1 || idx[0] != 0 {
		t.Errorf("indices = %v, want only the valid one", idx)
	}
}

func TestAQuestionIsAnsweredOnlyOnce(t *testing.T) {
	// The harness repeats a step update as it evolves; answering twice would
	// desynchronize the conversation.
	s := newSession(t)
	step := questionStep(multipleChoice("which?", false, "a"))
	s.pushStep(step)
	s.pushStep(step)

	if !waitSent(t, s.fake).HasQuestionResponse() {
		t.Fatal("the question was not answered at all")
	}
	// The claim that suppresses the repeat happens on the read loop, so once
	// the loop has drained there is no second answer still to come.
	s.syncOnUsage(t, 30)

	var answered int
	for _, ev := range s.fake.Sent() {
		if ev.HasQuestionResponse() {
			answered++
		}
	}
	if answered != 1 {
		t.Errorf("sent %d answers, want exactly 1", answered)
	}
}

// ---------------------------------------------------------------------------
// Tool confirmation
// ---------------------------------------------------------------------------

func TestToolConfirmationIsAccepted(t *testing.T) {
	// Gating happens earlier, through policies and the pre-tool hook, so by the
	// time the harness asks the answer is already yes.
	s := newSession(t)
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId:            proto.String("t1"),
		StepIndex:               proto.Uint32(2),
		State:                   pb.StepUpdate_STATE_WAITING_FOR_USER.Enum(),
		ToolConfirmationRequest: pb.ToolConfirmationRequest_builder{}.Build(),
	}.Build())

	got := waitSent(t, s.fake).GetToolConfirmation()
	if !got.GetAccepted() {
		t.Error("the confirmation was declined")
	}
	if got.GetTrajectoryId() != "t1" || got.GetStepIndex() != 2 {
		t.Errorf("confirmation addressed to %q/%d, want t1/2", got.GetTrajectoryId(), got.GetStepIndex())
	}
}

func TestAStepLeavingTheWaitingStateCanBeAskedAgain(t *testing.T) {
	// A step that waits, resumes, and waits again is a new question, not a
	// repeat of the one already answered.
	s := newSession(t)
	waiting := questionStep(multipleChoice("which?", false, "a"))
	active := pb.StepUpdate_builder{
		TrajectoryId: proto.String("t1"),
		StepIndex:    proto.Uint32(3),
		State:        pb.StepUpdate_STATE_ACTIVE.Enum(),
	}.Build()

	s.pushStep(waiting)
	s.pushStep(active)
	s.pushStep(waiting)

	for i := range 2 {
		if !waitSent(t, s.fake).HasQuestionResponse() {
			t.Fatalf("send %d is not an answer; want one per wait", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Dynamic policy decisions
// ---------------------------------------------------------------------------

// policySession builds a session whose enforcer has published its rules, which
// is what assigns the rule ids the harness refers to.
func policySession(t *testing.T, policies ...Policy) *session {
	t.Helper()

	enforcer, err := NewEnforcer(policies, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	enforcer.policyConfig()
	return newSessionWith(t, nil, enforcer)
}

// pushPolicyRequest asks the client to decide a deferred rule.
func (s *session) pushPolicyRequest(requestID, ruleID, tool, args string) {
	s.fake.Push(pb.OutputEvent_builder{
		PolicyDecisionRequest: pb.PolicyDecisionRequest_builder{
			RequestId: proto.String(requestID),
			RuleId:    proto.String(ruleID),
			ToolArgs: pb.PreToolArgs_builder{
				ToolName:      proto.String(tool),
				ArgumentsJson: proto.String(args),
			}.Build(),
		}.Build(),
	}.Build())
}

func TestPolicyDecisionAsksTheUser(t *testing.T) {
	var seen ToolCall
	s := policySession(t, Policy{
		Tool:     "run_command",
		Decision: DecisionAskUser,
		AskUser: func(_ context.Context, call ToolCall) (bool, error) {
			seen = call
			return true, nil
		},
	})

	s.pushPolicyRequest("req-1", "rule_0", "run_command", `{"command":"ls"}`)

	got := waitSent(t, s.fake).GetPolicyDecisionResponse()
	if got.GetRequestId() != "req-1" {
		t.Errorf("request id = %q, want it echoed", got.GetRequestId())
	}
	if got.GetOutcome() != pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_ALLOW {
		t.Errorf("outcome = %v, want ALLOW", got.GetOutcome())
	}
	if seen.Name != "run_command" || string(seen.Args) != `{"command":"ls"}` {
		t.Errorf("the handler saw %+v", seen)
	}
}

func TestPolicyDecisionDeniedByTheUser(t *testing.T) {
	s := policySession(t, Policy{
		Tool:     "run_command",
		Name:     "shell",
		Decision: DecisionAskUser,
		AskUser:  func(context.Context, ToolCall) (bool, error) { return false, nil },
	})

	s.pushPolicyRequest("req-1", "rule_0", "run_command", `{}`)

	got := waitSent(t, s.fake).GetPolicyDecisionResponse()
	if got.GetOutcome() != pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY {
		t.Errorf("outcome = %v, want DENY", got.GetOutcome())
	}
	if !strings.Contains(got.GetDenyReason(), "shell") {
		t.Errorf("reason = %q, want it to name the policy", got.GetDenyReason())
	}
}

func TestPolicyDecisionPredicateOutcomes(t *testing.T) {
	tests := []struct {
		name   string
		when   Predicate
		want   pb.PolicyEvaluationOutcome
		reason string
	}{
		{
			name: "a matching predicate applies the decision",
			when: func(context.Context, ToolCall) (bool, error) { return true, nil },
			want: pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
		},
		{
			// A rule that does not apply must not deny; later rules still get
			// their say.
			name: "a non-matching predicate defers",
			when: func(context.Context, ToolCall) (bool, error) { return false, nil },
			want: pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_NO_MATCH,
		},
		{
			// An unevaluable rule must not become an implicit approval.
			name:   "a failing predicate denies",
			when:   func(context.Context, ToolCall) (bool, error) { return false, errors.New("regex broke") },
			want:   pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY,
			reason: "regex broke",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := policySession(t, Policy{
				Tool:     "run_command",
				Decision: DecisionDeny,
				When:     tt.when,
			})
			s.pushPolicyRequest("req-1", "rule_0", "run_command", `{}`)

			got := waitSent(t, s.fake).GetPolicyDecisionResponse()
			if got.GetOutcome() != tt.want {
				t.Errorf("outcome = %v, want %v", got.GetOutcome(), tt.want)
			}
			if tt.reason != "" && !strings.Contains(got.GetDenyReason(), tt.reason) {
				t.Errorf("reason = %q, want it to mention %q", got.GetDenyReason(), tt.reason)
			}
		})
	}
}

func TestPolicyDecisionForAnUnknownRuleDenies(t *testing.T) {
	s := policySession(t)
	s.pushPolicyRequest("req-1", "rule_99", "run_command", `{}`)

	got := waitSent(t, s.fake).GetPolicyDecisionResponse()
	if got.GetOutcome() != pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY {
		t.Errorf("outcome = %v, want DENY for a rule we cannot resolve", got.GetOutcome())
	}
	if !strings.Contains(got.GetDenyReason(), "rule_99") {
		t.Errorf("reason = %q, want it to name the rule", got.GetDenyReason())
	}
}

func TestPolicyDecisionWithNoEnforcerDenies(t *testing.T) {
	s := newSession(t)
	s.pushPolicyRequest("req-1", "rule_0", "run_command", `{}`)

	got := waitSent(t, s.fake).GetPolicyDecisionResponse()
	if got.GetOutcome() != pb.PolicyEvaluationOutcome_POLICY_EVALUATION_OUTCOME_DENY {
		t.Errorf("outcome = %v, want DENY", got.GetOutcome())
	}
}

// ---------------------------------------------------------------------------
// Usage, trajectory state, and session end
// ---------------------------------------------------------------------------

func TestUsageIsTrackedPerTrajectory(t *testing.T) {
	s := newSession(t)
	s.fake.Push(pb.OutputEvent_builder{
		UsageUpdate: pb.UsageUpdate_builder{
			Total: pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(100)}.Build(),
			Agents: []*pb.TrajectoryUsageEntry{
				pb.TrajectoryUsageEntry_builder{
					TrajectoryId: proto.String("main"),
					Usage:        pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(60)}.Build(),
				}.Build(),
				pb.TrajectoryUsageEntry_builder{
					TrajectoryId: proto.String("sub"),
					Usage:        pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(40)}.Build(),
				}.Build(),
				// Entries the harness cannot attribute are skipped rather than
				// collapsed under an empty key.
				pb.TrajectoryUsageEntry_builder{
					Usage: pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(7)}.Build(),
				}.Build(),
			},
		}.Build(),
	}.Build())
	s.syncOnUsage(t, 100)

	byTrajectory := s.proc.usageByTrajectory()
	if len(byTrajectory) != 2 {
		t.Fatalf("usage = %v, want the two attributed trajectories", byTrajectory)
	}
	if byTrajectory["main"].TotalTokenCount != 60 || byTrajectory["sub"].TotalTokenCount != 40 {
		t.Errorf("usage = %v", byTrajectory)
	}

	// The map is a copy; mutating it must not corrupt the session's totals.
	byTrajectory["main"] = UsageMetadata{}
	if s.proc.usageByTrajectory()["main"].TotalTokenCount != 60 {
		t.Error("usageByTrajectory handed out its internal map")
	}
}

func TestSeedUsageRestoresPriorTotals(t *testing.T) {
	s := newSession(t)
	s.proc.seedUsage(
		&UsageMetadata{TotalTokenCount: 500},
		map[string]UsageMetadata{"main": {TotalTokenCount: 500}},
	)

	if s.proc.usage().TotalTokenCount != 500 {
		t.Errorf("usage = %v, want the restored total", s.proc.usage())
	}
	if s.proc.usageByTrajectory()["main"].TotalTokenCount != 500 {
		t.Error("the restored per-trajectory usage is missing")
	}
}

func TestTrajectoryDepthReachesTheStep(t *testing.T) {
	s := newSession(t)
	// The main trajectory is whichever one reports a step first.
	s.pushStep(textStep("main", 0, "hello", true))
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("sub"),
			Depth:        proto.Int32(2),
			State:        pb.TrajectoryStateUpdate_STATE_RUNNING.Enum(),
		}.Build(),
	}.Build())
	s.pushStep(textStep("sub", 0, "from the subagent", true))
	s.syncOnUsage(t, 30)

	var depths []int
	for len(s.proc.steps) > 0 {
		depths = append(depths, (<-s.proc.steps).step.Depth)
	}
	if len(depths) != 2 || depths[0] != 0 || depths[1] != 2 {
		t.Errorf("depths = %v, want the subagent's step nested", depths)
	}
}

func TestStopReasonIsRecorded(t *testing.T) {
	s := newSession(t)
	s.pushStep(textStep("t1", 0, "hi", true))
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("t1"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			StopReason:   pb.TrajectoryStateUpdate_STOP_REASON_MAX_TOOL_CALLS_EXCEEDED.Enum(),
		}.Build(),
	}.Build())
	s.syncOnUsage(t, 30)

	if got := s.proc.stopReason(); got != StopMaxToolCalls {
		t.Errorf("stopReason = %q, want %q", got, StopMaxToolCalls)
	}

	// A new turn starts with no reason to report.
	s.proc.resetForTurn()
	if got := s.proc.stopReason(); got != StopUnspecified {
		t.Errorf("stopReason = %q after reset, want it cleared", got)
	}
}

func TestRequestSessionEnd(t *testing.T) {
	s := newSession(t)

	done := make(chan error, 1)
	go func() { done <- s.proc.requestSessionEnd(context.WithoutCancel(t.Context())) }()

	if !waitSent(t, s.fake).GetSessionEndRequest() {
		t.Fatal("the SDK did not ask the harness to end the session")
	}
	s.fake.Push(pb.OutputEvent_builder{
		SessionEndResponse: proto.Bool(true),
	}.Build())

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("requestSessionEnd: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("requestSessionEnd never returned after the harness acknowledged")
	}
}

func TestRequestSessionEndHonorsCancellation(t *testing.T) {
	// Without an acknowledgement the caller must still be able to give up.
	s := newSession(t)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- s.proc.requestSessionEnd(ctx) }()

	waitSent(t, s.fake)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("requestSessionEnd = %v, want a cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("requestSessionEnd ignored the cancelled context")
	}
}

func TestUserInputParts(t *testing.T) {
	image, err := NewImage([]byte{1}, "image/png", "a chart")
	if err != nil {
		t.Fatalf("NewImage: %v", err)
	}

	parts, err := userInputParts([]Content{
		Text("hello"),
		SlashCommand{Name: "review"},
		image,
	})
	if err != nil {
		t.Fatalf("userInputParts: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	if parts[0].GetText() != "hello" {
		t.Errorf("part 0 = %v", parts[0])
	}
	if parts[1].GetSlashCommand().GetName() != "review" {
		t.Errorf("part 1 = %v", parts[1])
	}
	if parts[2].GetMedia().GetMimeType() != "image/png" {
		t.Errorf("part 2 = %v", parts[2])
	}
}

func TestUserInputPartsRejectsUnknownContent(t *testing.T) {
	_, err := userInputParts([]Content{unknownContent{}})
	if !errors.Is(err, ErrInvalidPrompt) {
		t.Errorf("error = %v, want ErrInvalidPrompt", err)
	}
}

// unknownContent is a Content implementation the wire conversion does not know
// about, which only this package could construct.
type unknownContent struct{}

func (unknownContent) isContent() {}

func TestJSONArgs(t *testing.T) {
	if got := jsonArgs(nil); got != nil {
		t.Errorf("jsonArgs(nil) = %v, want nil", got)
	}
	if got := jsonArgs(json.RawMessage("not json")); got != nil {
		t.Errorf("jsonArgs(invalid) = %v, want nil", got)
	}
	got := jsonArgs(json.RawMessage(`{"a":1}`))
	if len(got) != 1 || got["a"] != float64(1) {
		t.Errorf("jsonArgs = %v", got)
	}
}
