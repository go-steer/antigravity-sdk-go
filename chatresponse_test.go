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
	"errors"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// deltaStep builds a step carrying a single text delta.
func deltaStep(index uint32, delta string) *pb.StepUpdate {
	return pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(index),
		Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:       pb.StepUpdate_TARGET_USER.Enum(),
		State:        pb.StepUpdate_STATE_ACTIVE.Enum(),
		TextDelta:    proto.String(delta),
	}.Build()
}

// chatTurn sends a prompt and queues a two-delta reply with one tool call.
func chatTurn(t *testing.T, s *session) *ChatResponse {
	t.Helper()

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId:  proto.String("main"),
		StepIndex:     proto.Uint32(0),
		Source:        pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:        pb.StepUpdate_TARGET_USER.Enum(),
		State:         pb.StepUpdate_STATE_ACTIVE.Enum(),
		ThinkingDelta: proto.String("thinking"),
	}.Build())
	s.pushStep(deltaStep(1, "Hello"))
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(2),
		Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:       pb.StepUpdate_TARGET_ENVIRONMENT.Enum(),
		State:        pb.StepUpdate_STATE_ACTIVE.Enum(),
		ViewFile:     pb.ActionViewFile_builder{FilePath: proto.String("/tmp/f.txt")}.Build(),
	}.Build())
	s.pushStep(deltaStep(3, ", world"))
	s.pushIdle("main")
	return resp
}

func TestChatResponseWaitReturnsTheWholeText(t *testing.T) {
	s := newSession(t)
	resp := chatTurn(t, s)

	got, err := resp.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got != "Hello, world" {
		t.Errorf("Wait = %q, want the concatenated deltas", got)
	}
}

func TestChatResponseCursorsShareOneBuffer(t *testing.T) {
	s := newSession(t)
	resp := chatTurn(t, s)

	// The first cursor drains the turn; the second replays it from the buffer
	// rather than finding the stream exhausted.
	first, err := resp.Wait()
	if err != nil {
		t.Fatal(err)
	}

	var second strings.Builder
	for text, err := range resp.Text() {
		if err != nil {
			t.Fatal(err)
		}
		second.WriteString(text)
	}
	if second.String() != first {
		t.Errorf("the replayed cursor read %q, want %q", second.String(), first)
	}

	var thoughts []string
	for thought, err := range resp.Thoughts() {
		if err != nil {
			t.Fatal(err)
		}
		thoughts = append(thoughts, thought)
	}
	if len(thoughts) != 1 || thoughts[0] != "thinking" {
		t.Errorf("thoughts = %v, want one reasoning delta", thoughts)
	}

	var calls []ToolCall
	for call, err := range resp.ToolCalls() {
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if len(calls) != 1 || calls[0].Name != string(ToolViewFile) {
		t.Errorf("tool calls = %v, want one view_file call", calls)
	}
}

func TestChatResponseCursorsRunConcurrently(t *testing.T) {
	s := newSession(t)
	resp := chatTurn(t, s)

	// Two cursors at the live edge: one pulls, the other must see what it
	// pulled rather than race it.
	var wg sync.WaitGroup
	results := make([]string, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var b strings.Builder
			for text, err := range resp.Text() {
				if err != nil {
					return
				}
				b.WriteString(text)
			}
			results[i] = b.String()
		}()
	}
	wg.Wait()

	for i, got := range results {
		if got != "Hello, world" {
			t.Errorf("cursor %d read %q, want the whole response", i, got)
		}
	}
}

func TestChatResponseResolveReturnsEveryChunk(t *testing.T) {
	s := newSession(t)
	resp := chatTurn(t, s)

	chunks, err := resp.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// One thought, two text deltas, one tool call.
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4: %v", len(chunks), chunks)
	}
}

func TestChatResponseReportsTurnErrors(t *testing.T) {
	s := newSession(t)

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatal(err)
	}
	s.pushStep(deltaStep(0, "partial"))
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			Error:        proto.String("out of quota"),
		}.Build(),
	}.Build())

	// The text read before the failure is still returned alongside the error.
	text, err := resp.Wait()
	if !errors.Is(err, ErrExecution) {
		t.Errorf("err = %v, want ErrExecution", err)
	}
	if text != "partial" {
		t.Errorf("text = %q, want what arrived before the failure", text)
	}
}

func TestChatResponseCloseStopsTheStream(t *testing.T) {
	s := newSession(t)
	resp := chatTurn(t, s)

	// Read one delta, then hang up on the rest.
	for range resp.Text() {
		break
	}
	if err := resp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := resp.Close(); err != nil {
		t.Fatalf("the second Close returned %v, want nil", err)
	}

	var replayed []string
	for text, err := range resp.Text() {
		if err != nil {
			t.Fatal(err)
		}
		replayed = append(replayed, text)
	}
	if len(replayed) != 1 || replayed[0] != "Hello" {
		t.Errorf("a cursor after Close read %v, want only the buffered delta", replayed)
	}
}

func TestChatResponseStructuredOutput(t *testing.T) {
	s := newSession(t)

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatal(err)
	}
	s.pushStep(pb.StepUpdate_builder{
		TrajectoryId: proto.String("main"),
		StepIndex:    proto.Uint32(0),
		Source:       pb.StepUpdate_SOURCE_MODEL.Enum(),
		Target:       pb.StepUpdate_TARGET_USER.Enum(),
		State:        pb.StepUpdate_STATE_DONE.Enum(),
		Finish:       pb.ActionFinish_builder{OutputString: proto.String(`{"ok":true}`)}.Build(),
	}.Build())
	s.pushIdle("main")

	out, err := resp.StructuredOutput()
	if err != nil {
		t.Fatalf("StructuredOutput: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("StructuredOutput = %s, want the finish payload", out)
	}
}

func TestChatResponseUsageArrivesWithTheEndOfTheTurn(t *testing.T) {
	s := newSession(t)

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage() != nil {
		t.Error("Usage is reported before the turn ends")
	}

	s.pushUsage(42)
	s.pushStep(deltaStep(0, "hi"))
	s.pushIdle("main")
	if _, err := resp.Wait(); err != nil {
		t.Fatal(err)
	}

	usage := resp.Usage()
	if usage == nil {
		t.Fatal("Usage is nil after the turn ended")
	}
	if usage.TotalTokenCount != 42 {
		t.Errorf("the turn consumed %d tokens, want 42", usage.TotalTokenCount)
	}
}

func TestChatResponseCancel(t *testing.T) {
	s := newSession(t)

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := countHalts(s.fake.Sent()); got != 1 {
		t.Fatalf("sent %d halt requests, want 1", got)
	}

	// Once the turn is over there is nothing left to halt.
	s.pushIdle("main")
	if _, err := resp.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := resp.Cancel(t.Context()); err != nil {
		t.Fatalf("Cancel after the turn: %v", err)
	}
	if got := countHalts(s.fake.Sent()); got != 1 {
		t.Errorf("sent %d halt requests, want the second Cancel to be a no-op", got)
	}
}

func countHalts(sent []*pb.InputEvent) int {
	n := 0
	for _, ev := range sent {
		if ev.HasHaltRequest() {
			n++
		}
	}
	return n
}

func TestChatReclaimsAnAbandonedResponse(t *testing.T) {
	s := newSession(t)

	first := chatTurn(t, s)
	// Walk away after a single delta, then start a new turn: the old response
	// must be released rather than left holding the stream.
	for range first.Text() {
		break
	}
	s.syncOnUsage(t, 7)

	second, err := s.Chat(t.Context(), Text("again"))
	if err != nil {
		t.Fatalf("the second Chat failed: %v", err)
	}
	if !first.finished() {
		t.Error("the abandoned response still holds the stream")
	}

	s.pushStep(deltaStep(0, "fresh"))
	s.pushIdle("main")
	got, err := second.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh" {
		t.Errorf("the second turn read %q, want only its own output", got)
	}
}

func TestChatResponseStopReason(t *testing.T) {
	s := newSession(t)

	resp, err := s.Chat(t.Context(), Text("go"))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	s.pushStep(deltaStep(0, "as far as I got"))
	s.fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			StopReason:   pb.TrajectoryStateUpdate_STOP_REASON_MAX_OUTPUT_TOKENS_EXCEEDED.Enum(),
		}.Build(),
	}.Build())

	if _, err := resp.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Hitting a budget cap is not an error, so the stop reason is the only way
	// a caller learns the answer was cut short.
	if got := resp.StopReason(); got != StopMaxOutputTokens {
		t.Errorf("StopReason = %q, want %q", got, StopMaxOutputTokens)
	}
}
