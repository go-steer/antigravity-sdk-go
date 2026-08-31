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
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// session is a conversation wired to a fake harness, which is as close to a
// real one as the SDK gets without the localharness binary.
type session struct {
	*Conversation
	fake *harness.FakeTransport
	proc *eventProcessor
}

// newSession starts a conversation over a fake harness. Each configure
// function may register hooks before the read loop starts.
func newSession(t *testing.T, configure ...func(*hookRunner)) *session {
	t.Helper()

	return newSessionWith(t, nil, nil, configure...)
}

// newSessionWith is [newSession] for tests that also need client-side tools or
// a policy enforcer, the two things the processor consults when the harness
// asks it something.
func newSessionWith(t *testing.T, tools map[string]Tool, enforcer *Enforcer, configure ...func(*hookRunner)) *session {
	t.Helper()

	fake := harness.NewFakeTransport()
	hooks := newHookRunner()
	for _, fn := range configure {
		fn(hooks)
	}
	proc := newEventProcessor(fake, tools, enforcer, hooks, nil)
	s := &session{
		Conversation: newConversation(proc, nil, 0),
		fake:         fake,
		proc:         proc,
	}

	// The read loop outlives the context that started it, exactly as it does in
	// a real session; closing the transport is what ends it.
	proc.start(context.WithoutCancel(t.Context()))
	t.Cleanup(func() {
		fake.Close()
		proc.stop()
	})
	return s
}

// pushStep injects a step update, as if the harness had emitted it.
func (s *session) pushStep(su *pb.StepUpdate) {
	s.fake.Push(pb.OutputEvent_builder{StepUpdate: su}.Build())
}

// pushIdle ends a trajectory, which is what stops a reader iterating.
func (s *session) pushIdle(trajectory string) {
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String(trajectory),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
		}.Build(),
	}.Build())
}

// pushUsage reports a cumulative token total.
func (s *session) pushUsage(total int) {
	prompt := uint64(total) / 3
	s.fake.Push(pb.OutputEvent_builder{
		UsageUpdate: pb.UsageUpdate_builder{
			Total: pb.UsageMetadata_builder{
				PromptTokenCount:     proto.Uint64(prompt),
				CandidatesTokenCount: proto.Uint64(uint64(total) - prompt),
				TotalTokenCount:      proto.Uint64(uint64(total)),
			}.Build(),
		}.Build(),
	}.Build())
}

// syncOnUsage waits until the read loop has processed everything pushed up to
// this point. Events are handled in order on one goroutine, so a usage update
// landing proves the events queued before it already have.
func (s *session) syncOnUsage(t *testing.T, total int) {
	t.Helper()

	s.pushUsage(total)
	deadline := time.Now().Add(5 * time.Second)
	for s.Usage().TotalTokenCount != int64(total) {
		if time.Now().After(deadline) {
			t.Fatalf("the harness never reported a total of %d tokens", total)
		}
		time.Sleep(time.Millisecond)
	}
}

// textStep builds a model response step aimed at the user.
func textStep(trajectory string, index uint32, text string, done bool) *pb.StepUpdate {
	state := pb.StepUpdate_STATE_ACTIVE
	if done {
		state = pb.StepUpdate_STATE_DONE
	}
	return pb.StepUpdate_builder{
		TrajectoryId: proto.String(trajectory),
		StepIndex:    proto.Uint32(index),
		Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:       pb.StepUpdate_TARGET_USER.Enum(),
		State:        state.Enum(),
		Text:         proto.String(text),
		TextDelta:    proto.String(text),
	}.Build()
}

// collect drains a turn, returning its steps and the first error reported.
func collect(t *testing.T, c *Conversation) ([]Step, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var steps []Step
	var firstErr error
	for step, err := range c.Steps(ctx) {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		steps = append(steps, step)
	}
	return steps, firstErr
}

func TestConversationStreamsATurn(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("hello")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.pushStep(textStep("main", 0, "Hi", false))
	s.pushStep(textStep("main", 0, "Hi there", true))
	s.pushIdle("main")

	steps, err := collect(t, s.Conversation)
	if err != nil {
		t.Fatalf("the turn reported %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[1].Content != "Hi there" {
		t.Errorf("last step content = %q", steps[1].Content)
	}
	if steps[0].IsCompleteResponse {
		t.Error("an in-progress step is marked as a complete response")
	}
	if !steps[1].IsCompleteResponse {
		t.Error("the finished model step is not marked as a complete response")
	}
	if got := s.TurnCount(); got != 1 {
		t.Errorf("TurnCount = %d, want 1", got)
	}
	if got := s.LastResponse(); got != "Hi there" {
		t.Errorf("LastResponse = %q, want the final response text", got)
	}
}

func TestConversationSendRejectsAnEmptyPrompt(t *testing.T) {
	s := newSession(t)

	prompts := [][]Content{nil, {}, {Text("")}, {Text("  "), Text("\n")}}
	for _, prompt := range prompts {
		if err := s.Send(t.Context(), prompt...); !errors.Is(err, ErrInvalidPrompt) {
			t.Errorf("Send(%v) = %v, want ErrInvalidPrompt", prompt, err)
		}
	}
	if got := len(s.fake.Sent()); got != 0 {
		t.Errorf("%d events reached the harness, want none", got)
	}
}

func TestConversationSendsTheUserInput(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("hello")); err != nil {
		t.Fatal(err)
	}

	sent := s.fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d events, want 1", len(sent))
	}
	if !sent[0].HasUserInput() {
		t.Fatalf("sent %v, want a user input event", sent[0])
	}
	parts := sent[0].GetUserInput().GetParts()
	if len(parts) != 1 || parts[0].GetText() != "hello" {
		t.Errorf("parts = %v, want a single text part", parts)
	}
}

func TestConversationRecordsHistoryAcrossTurns(t *testing.T) {
	s := newSession(t)

	for i := range uint32(2) {
		if err := s.Send(t.Context(), Text("go")); err != nil {
			t.Fatal(err)
		}
		s.pushStep(textStep("main", i, "answer", true))
		s.pushIdle("main")
		if _, err := collect(t, s.Conversation); err != nil {
			t.Fatal(err)
		}
	}

	if got := len(s.History()); got != 2 {
		t.Errorf("history has %d steps, want 2", got)
	}
	if got := s.TurnCount(); got != 2 {
		t.Errorf("TurnCount = %d, want 2", got)
	}
}

func TestConversationTrimsHistoryToTheCap(t *testing.T) {
	c := newConversation(newEventProcessor(harness.NewFakeTransport(), nil, nil, newHookRunner(), nil), nil, 3)

	for i := range 5 {
		c.record(Step{Index: i})
	}

	history := c.History()
	if len(history) != 3 {
		t.Fatalf("history has %d steps, want 3", len(history))
	}
	// The oldest go, not the newest.
	if history[0].Index != 2 || history[2].Index != 4 {
		t.Errorf("history holds steps %d..%d, want 2..4", history[0].Index, history[2].Index)
	}
}

func TestConversationTrimShiftsRecordedIndices(t *testing.T) {
	c := newConversation(newEventProcessor(harness.NewFakeTransport(), nil, nil, newHookRunner(), nil), nil, 3)

	c.record(Step{Index: 0, Type: StepCompaction}) // dropped by the trim
	c.record(Step{Index: 1})
	c.record(Step{Index: 2, Type: StepCompaction}) // survives, shifted down
	c.record(Step{Index: 3})
	c.record(Step{Index: 4})

	got := c.CompactionIndices()
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("CompactionIndices = %v, want [0] after trimming", got)
	}
	if c.History()[got[0]].Index != 2 {
		t.Error("the recorded index no longer points at the compaction step")
	}
}

func TestConversationSeedsRestoredHistory(t *testing.T) {
	proc := newEventProcessor(harness.NewFakeTransport(), nil, nil, newHookRunner(), nil)
	c := newConversation(proc, []Step{
		{Index: 0, Content: "earlier", IsCompleteResponse: true},
		{Index: 1, Type: StepCompaction},
	}, 0)

	if got := len(c.History()); got != 2 {
		t.Errorf("history has %d steps, want the 2 restored", got)
	}
	if got := c.CompactionIndices(); len(got) != 1 || got[0] != 1 {
		t.Errorf("CompactionIndices = %v, want [1]", got)
	}
	if got := c.LastResponse(); got != "earlier" {
		t.Errorf("LastResponse = %q, want the restored response", got)
	}
	// Restored history is not a turn this client sent.
	if got := c.TurnCount(); got != 0 {
		t.Errorf("TurnCount = %d, want 0", got)
	}
}

func TestConversationClearHistory(t *testing.T) {
	proc := newEventProcessor(harness.NewFakeTransport(), nil, nil, newHookRunner(), nil)
	c := newConversation(proc, []Step{{Content: "x", Type: StepCompaction}}, 0)

	c.ClearHistory()

	if len(c.History()) != 0 {
		t.Error("steps survived ClearHistory")
	}
	if len(c.CompactionIndices()) != 0 {
		t.Error("compaction markers survived ClearHistory")
	}
	if c.LastResponse() != "" {
		t.Error("LastResponse survived ClearHistory")
	}
}

func TestConversationChunksSplitsDeltasAndDeduplicatesToolCalls(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId:  proto.String("main"),
		StepIndex:     proto.Uint32(0),
		Source:        pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:        pb.StepUpdate_TARGET_USER.Enum(),
		State:         pb.StepUpdate_STATE_ACTIVE.Enum(),
		ThinkingDelta: proto.String("hmm"),
		TextDelta:     proto.String("hello"),
	}.Build())
	// The same call, reported on two updates of the same step.
	for range 2 {
		s.pushStep(pb.StepUpdate_builder{
			TrajectoryId: proto.String("main"),
			StepIndex:    proto.Uint32(1),
			Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
			Target:       pb.StepUpdate_TARGET_ENVIRONMENT.Enum(),
			State:        pb.StepUpdate_STATE_ACTIVE.Enum(),
			ViewFile: pb.ActionViewFile_builder{
				FilePath: proto.String("/tmp/f.txt"),
			}.Build(),
		}.Build())
	}
	s.pushIdle("main")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	var thoughts, texts int
	var calls []ToolCall
	for chunk, err := range s.Chunks(ctx) {
		if err != nil {
			t.Fatalf("Chunks reported %v", err)
		}
		switch c := chunk.(type) {
		case ThoughtChunk:
			thoughts++
		case TextChunk:
			texts++
		case ToolCall:
			calls = append(calls, c)
		}
	}

	if thoughts != 1 || texts != 1 {
		t.Errorf("got %d thought and %d text chunks, want 1 of each", thoughts, texts)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d tool chunks, want 1: the repeat must be deduplicated", len(calls))
	}
	if calls[0].Name != string(ToolViewFile) {
		t.Errorf("tool = %q, want %q", calls[0].Name, ToolViewFile)
	}
}

func TestConversationReportsATurnFailure(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			Error:        proto.String("the model refused"),
		}.Build(),
	}.Build())

	_, err := collect(t, s.Conversation)
	if !errors.Is(err, ErrExecution) {
		t.Fatalf("err = %v, want ErrExecution", err)
	}
	// The failure does not replace going idle: the turn still ends.
	if !s.IsIdle() {
		t.Error("the session is not idle after a failed turn")
	}
}

func TestConversationReportsCancellation(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_CANCELLED.Enum(),
		}.Build(),
	}.Build())

	_, err := collect(t, s.Conversation)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}

func TestConversationSubagentIdleDoesNotEndTheTurn(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	// The main trajectory is whichever one speaks first.
	s.pushStep(textStep("main", 0, "delegating", false))
	s.pushStep(textStep("sub", 0, "working", true))
	s.pushIdle("sub")
	s.pushStep(textStep("main", 1, "done", true))
	s.pushIdle("main")

	steps, err := collect(t, s.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3: a subagent going idle must not end the turn", len(steps))
	}
	if steps[2].Content != "done" {
		t.Errorf("the turn ended on %q, want the main trajectory's last step", steps[2].Content)
	}
}

func TestConversationSendDiscardsAnAbandonedTurn(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("first")); err != nil {
		t.Fatal(err)
	}
	s.pushStep(textStep("main", 0, "partial", true))
	s.pushIdle("main")
	s.syncOnUsage(t, 9)

	// Nothing of the first turn was read. Sending again must clear it out
	// rather than let it bleed into the next turn.
	if err := s.Send(t.Context(), Text("second")); err != nil {
		t.Fatalf("the second Send failed: %v", err)
	}
	s.pushStep(textStep("main", 1, "answer", true))
	s.pushIdle("main")

	steps, err := collect(t, s.Conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Content != "answer" {
		t.Errorf("the second turn yielded %v, want only its own step", steps)
	}
}

func TestConversationIdleState(t *testing.T) {
	s := newSession(t)

	if !s.IsIdle() {
		t.Error("a session with no turn in flight is not idle")
	}
	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	if s.IsIdle() {
		t.Error("the session reports idle while a turn is in flight")
	}

	s.pushIdle("main")

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.WaitForIdle(ctx); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
	if !s.IsIdle() {
		t.Error("the session is not idle after the trajectory went quiet")
	}
}

func TestConversationWaitForIdleRespectsCancellation(t *testing.T) {
	s := newSession(t)
	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WaitForIdle(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestConversationCancelAndTrigger(t *testing.T) {
	s := newSession(t)

	if err := s.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := s.Trigger(t.Context(), "the build finished"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	sent := s.fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d events, want 2", len(sent))
	}
	if !sent[0].HasHaltRequest() {
		t.Errorf("the first event is %v, want a halt request", sent[0])
	}
	if got := sent[1].GetAutomatedTrigger(); got != "the build finished" {
		t.Errorf("automated_trigger = %q, want the trigger's message", got)
	}
}

func TestConversationTracksUsagePerTurn(t *testing.T) {
	s := newSession(t)

	// A turn's usage is the delta of two cumulative snapshots, so the first
	// turn has to leave a total behind for the second to be measured against.
	for _, total := range []int{30, 100} {
		if err := s.Send(t.Context(), Text("go")); err != nil {
			t.Fatal(err)
		}
		s.pushUsage(total)
		s.pushStep(textStep("main", 0, "answer", true))
		s.pushIdle("main")
		if _, err := collect(t, s.Conversation); err != nil {
			t.Fatal(err)
		}
	}

	if got := s.Usage().TotalTokenCount; got != 100 {
		t.Errorf("Usage().TotalTokenCount = %d, want the latest cumulative total", got)
	}
	last := s.LastTurnUsage()
	if last == nil {
		t.Fatal("LastTurnUsage is nil after a completed turn")
	}
	if last.TotalTokenCount != 70 {
		t.Errorf("the turn consumed %d tokens, want 70", last.TotalTokenCount)
	}
}

func TestConversationStepsRespectsCancellation(t *testing.T) {
	s := newSession(t)
	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var got error
	for _, err := range s.Steps(ctx) {
		got = err
	}
	if !errors.Is(got, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", got)
	}
}

func TestConversationLastStructuredOutput(t *testing.T) {
	proc := newEventProcessor(harness.NewFakeTransport(), nil, nil, newHookRunner(), nil)
	c := newConversation(proc, []Step{
		{Type: StepFinish, StructuredOutput: []byte(`{"first":true}`)},
		{Type: StepTextResponse},
		{Type: StepFinish, StructuredOutput: []byte(`{"second":true}`)},
	}, 0)

	if got := string(c.LastStructuredOutput()); got != `{"second":true}` {
		t.Errorf("LastStructuredOutput = %s, want the most recent finish payload", got)
	}
}

func TestConversationReportsAHangup(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	// A harness that dies mid-turn has to release the reader, not hang it.
	s.fake.Close()

	if _, err := collect(t, s.Conversation); err != nil {
		t.Errorf("a clean hangup reported %v, want the turn to simply end", err)
	}
}

func TestConversationSendDrainsATurnStillInFlight(t *testing.T) {
	s := newSession(t)

	if err := s.Send(t.Context(), Text("first")); err != nil {
		t.Fatal(err)
	}
	s.pushStep(textStep("main", 0, "partial", true))
	s.syncOnUsage(t, 9)

	// The first turn has not ended, so the second Send has to wait it out
	// rather than interleave two turns on one connection.
	sent := make(chan error, 1)
	go func() { sent <- s.Send(t.Context(), Text("second")) }()

	select {
	case err := <-sent:
		t.Fatalf("Send returned while the first turn was still running (err = %v)", err)
	case <-time.After(50 * time.Millisecond):
	}

	s.pushIdle("main")
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("the second Send failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never returned after the first turn ended")
	}

	// Draining discards the steps from the stream but not from history.
	history := s.History()
	if len(history) != 1 || history[0].Content != "partial" {
		t.Errorf("history = %+v, want the drained step recorded", history)
	}
}

func TestConversationSendReportsADrainedFailure(t *testing.T) {
	// A turn that dies while a second prompt is being sent must surface its
	// failure to the caller of Send, not vanish.
	s := newSession(t)

	if err := s.Send(t.Context(), Text("first")); err != nil {
		t.Fatal(err)
	}
	s.syncOnUsage(t, 9)

	sent := make(chan error, 1)
	go func() { sent <- s.Send(t.Context(), Text("second")) }()

	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			Error:        proto.String("the model refused"),
		}.Build(),
	}.Build())

	select {
	case err := <-sent:
		if !errors.Is(err, ErrExecution) {
			t.Errorf("Send = %v, want the drained turn's failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send never returned")
	}
}

func TestConversationSendReportsAFailureFromAFinishedTurn(t *testing.T) {
	// The same failure, but the turn is already over and idle by the time Send
	// runs. Going idle is not what accounts for a turn — reading it is — so the
	// error still has to reach the next caller rather than be reset away.
	s := newSession(t)

	if err := s.Send(t.Context(), Text("first")); err != nil {
		t.Fatal(err)
	}

	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			Error:        proto.String("the model refused"),
		}.Build(),
	}.Build())
	s.syncOnUsage(t, 9)

	if !s.IsIdle() {
		t.Fatal("the trajectory is still running after a fully-idle update")
	}
	if err := s.Send(t.Context(), Text("second")); !errors.Is(err, ErrExecution) {
		t.Errorf("Send = %v, want the finished turn's failure", err)
	}
}

func TestConversationUsageByTrajectory(t *testing.T) {
	s := newSession(t)

	s.fake.Push(pb.OutputEvent_builder{
		UsageUpdate: pb.UsageUpdate_builder{
			Total: pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(30)}.Build(),
			Agents: []*pb.TrajectoryUsageEntry{
				pb.TrajectoryUsageEntry_builder{
					TrajectoryId: proto.String("main"),
					Usage:        pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(20)}.Build(),
				}.Build(),
				pb.TrajectoryUsageEntry_builder{
					TrajectoryId: proto.String("researcher"),
					Usage:        pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(10)}.Build(),
				}.Build(),
			},
		}.Build(),
	}.Build())
	s.syncOnUsage(t, 30)

	// Subagents bill to their own trajectory, which is the only way a caller
	// can attribute cost to delegated work.
	got := s.UsageByTrajectory()
	if len(got) != 2 || got["main"].TotalTokenCount != 20 || got["researcher"].TotalTokenCount != 10 {
		t.Errorf("UsageByTrajectory = %v, want both trajectories", got)
	}
}

func TestConversationStopReason(t *testing.T) {
	s := newSession(t)

	if got := s.StopReason(); got != StopUnspecified {
		t.Errorf("StopReason = %q, want none before a turn has ended", got)
	}

	if err := s.Send(t.Context(), Text("go")); err != nil {
		t.Fatal(err)
	}
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			StopReason:   pb.TrajectoryStateUpdate_STOP_REASON_MAX_TOOL_CALLS_EXCEEDED.Enum(),
		}.Build(),
	}.Build())

	if _, err := collect(t, s.Conversation); err != nil {
		t.Fatal(err)
	}
	// A budget cap is not a failure, so the turn ends cleanly and the reason is
	// the only signal that the agent did not finish on its own terms.
	if got := s.StopReason(); got != StopMaxToolCalls {
		t.Errorf("StopReason = %q, want the budget cap", got)
	}
}
