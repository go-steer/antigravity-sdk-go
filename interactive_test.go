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
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// fakeTerminal points the terminal helpers at a scripted input and a captured
// output for the duration of a test.
func fakeTerminal(t *testing.T, input string) *bytes.Buffer {
	t.Helper()

	out := &bytes.Buffer{}
	prevIn, prevOut := termIn, termOut
	termIn, termOut = newLineReader(strings.NewReader(input)), out
	t.Cleanup(func() { termIn, termOut = prevIn, prevOut })
	return out
}

func TestLineReaderReadsSequentially(t *testing.T) {
	r := newLineReader(strings.NewReader("first\nsecond\n"))

	for _, want := range []string{"first", "second"} {
		got, err := r.readLine(t.Context())
		if err != nil {
			t.Fatalf("readLine: %v", err)
		}
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestLineReaderReportsEOFRepeatedly(t *testing.T) {
	r := newLineReader(strings.NewReader("only\n"))

	if _, err := r.readLine(t.Context()); err != nil {
		t.Fatalf("readLine: %v", err)
	}
	// Exhausted input must stay exhausted rather than blocking a second caller.
	for i := range 2 {
		if _, err := r.readLine(t.Context()); !errors.Is(err, io.EOF) {
			t.Errorf("read %d after EOF: err = %v, want io.EOF", i, err)
		}
	}
}

func TestLineReaderRespectsCancellation(t *testing.T) {
	// A reader with no data and no EOF: blocked forever, like a real terminal
	// waiting for a keystroke.
	r := newLineReader(blockingReader{})

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := r.readLine(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readLine ignored the cancelled context")
	}
}

// blockingReader never returns, standing in for a terminal awaiting input.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { select {} }

// ---------------------------------------------------------------------------
// Confirmation
// ---------------------------------------------------------------------------

func TestConfirmInTerminal(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{" y \n", true},
		{"n\n", false},
		{"\n", false},
		{"maybe\n", false},
		{"", false}, // EOF: no answer is not approval
	}

	for _, tt := range tests {
		fakeTerminal(t, tt.input)
		got, err := ConfirmInTerminal()(t.Context(), ToolCall{Name: "run_command"})
		if err != nil {
			t.Errorf("input %q: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("input %q: allowed = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestConfirmInTerminalShowsTheCall(t *testing.T) {
	out := fakeTerminal(t, "n\n")
	call := ToolCall{Name: "run_command", Args: []byte(`{"command":"rm -rf /"}`)}

	if _, err := ConfirmInTerminal()(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run_command", "rm -rf /"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the prompt does not mention %q:\n%s", want, out)
		}
	}
}

func TestConfirmToolCallsInTerminalDenies(t *testing.T) {
	fakeTerminal(t, "n\n")
	got, err := ConfirmToolCallsInTerminal()(t.Context(), nil, ToolCall{Name: "edit_file"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deny {
		t.Error("Deny = false, want true")
	}
	if got.Reason == "" {
		t.Error("a denial must carry a reason for the model")
	}
}

func TestConfirmToolCallsInTerminalAllows(t *testing.T) {
	fakeTerminal(t, "yes\n")
	got, err := ConfirmToolCallsInTerminal()(t.Context(), nil, ToolCall{Name: "edit_file"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Deny {
		t.Errorf("Deny = true, want false (%s)", got.Reason)
	}
}

// ---------------------------------------------------------------------------
// Questions
// ---------------------------------------------------------------------------

func TestParseAnswer(t *testing.T) {
	single := Question{
		Text:    "Which?",
		Options: []QuestionOption{{Text: "Alpha"}, {Text: "Beta"}},
	}
	multi := Question{
		Text:        "Which ones?",
		Options:     single.Options,
		MultiSelect: true,
	}

	tests := []struct {
		name  string
		reply string
		q     Question
		want  Answer
	}{
		{"empty skips", "", single, Answer{Skipped: true}},
		{"by number", "2", single, Answer{SelectedOptions: []int{1}}},
		{"by text", "alpha", single, Answer{SelectedOptions: []int{0}}},
		{"freeform", "something else", single, Answer{Text: "something else"}},
		{"number out of range", "9", single, Answer{Text: "9"}},
		{"multi select", "1, 2", multi, Answer{SelectedOptions: []int{0, 1}}},
		{"partial match is freeform", "1, nope", multi, Answer{Text: "1, nope"}},
		{"commas without options", "a, b", Question{Text: "?"}, Answer{Text: "a, b"}},
		// A comma is only a separator where several answers are allowed.
		{"comma in single select", "1, 2", single, Answer{Text: "1, 2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAnswer(tt.reply, tt.q)
			if got.Skipped != tt.want.Skipped || got.Text != tt.want.Text ||
				!slices.Equal(got.SelectedOptions, tt.want.SelectedOptions) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAnswerQuestionsInTerminal(t *testing.T) {
	out := fakeTerminal(t, "2\nfreeform reply\n")
	req := QuestionRequest{Questions: []Question{
		{Text: "Pick one", Options: []QuestionOption{{Text: "Alpha"}, {Text: "Beta"}}},
		{Text: "Say something"},
	}}

	got, err := AnswerQuestionsInTerminal()(t.Context(), nil, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Answers) != 2 {
		t.Fatalf("got %d answers, want 2", len(got.Answers))
	}
	if !slices.Equal(got.Answers[0].SelectedOptions, []int{1}) {
		t.Errorf("answer 0 = %+v, want option 1 selected", got.Answers[0])
	}
	if got.Answers[1].Text != "freeform reply" {
		t.Errorf("answer 1 = %+v, want the typed text", got.Answers[1])
	}
	for _, want := range []string{"Pick one", "1. Alpha", "2. Beta", "Say something"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the transcript does not contain %q:\n%s", want, out)
		}
	}
}

func TestAnswerQuestionsInTerminalStopsAtEOF(t *testing.T) {
	// Input runs out after the first answer; the second question goes
	// unanswered rather than being answered wrongly.
	fakeTerminal(t, "1\n")
	req := QuestionRequest{Questions: []Question{
		{Text: "First", Options: []QuestionOption{{Text: "Alpha"}}},
		{Text: "Second"},
	}}

	got, err := AnswerQuestionsInTerminal()(t.Context(), nil, req)
	if err != nil {
		t.Fatalf("EOF must not fail the hook: %v", err)
	}
	if len(got.Answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(got.Answers))
	}
}

// ---------------------------------------------------------------------------
// Configuration upgrades
// ---------------------------------------------------------------------------

func TestConfirmInsteadOfDenyUpgradesTheDefaults(t *testing.T) {
	got := confirmInsteadOfDeny(ConfirmRunCommand(nil))

	var gate *Policy
	for i, p := range got {
		if p.Tool == string(ToolRunCommand) {
			gate = &got[i]
		}
	}
	if gate == nil {
		t.Fatal("the run_command policy disappeared")
	}
	if gate.Decision != DecisionAskUser {
		t.Errorf("Decision = %q, want %q", gate.Decision, DecisionAskUser)
	}
	if gate.AskUser == nil {
		t.Error("an ask-user policy needs a handler")
	}
	if gate.Name != "confirm_run_command" {
		t.Errorf("Name = %q, want the original name preserved", gate.Name)
	}
}

func TestConfirmInsteadOfDenyLeavesOtherPoliciesAlone(t *testing.T) {
	pred := func(context.Context, ToolCall) (bool, error) { return true, nil }
	original := []Policy{
		DenyAll(),
		DenyTool(ToolEditFile),
		// A conditional denial is a deliberate rule, not a default.
		DenyTool(ToolRunCommand, When(pred)),
	}
	got := confirmInsteadOfDeny(original)

	for i := range original {
		if got[i].Decision != DecisionDeny {
			t.Errorf("policy %d (%s) was upgraded, want it left alone", i, got[i].Tool)
		}
	}
}

func TestConfirmInsteadOfDenyDoesNotMutateItsInput(t *testing.T) {
	original := ConfirmRunCommand(nil)
	confirmInsteadOfDeny(original)

	for _, p := range original {
		if p.Tool == string(ToolRunCommand) && p.Decision != DecisionDeny {
			t.Error("the caller's policy slice was modified in place")
		}
	}
}

func TestInteractiveDefaults(t *testing.T) {
	c := newConfig()
	interactiveDefaults()(c)

	if c.capabilities.Behavior != BehaviorInteractive {
		t.Errorf("Behavior = %q, want %q", c.capabilities.Behavior, BehaviorInteractive)
	}
	if len(c.hooks.interaction) != 1 {
		t.Errorf("got %d interaction hooks, want the terminal one installed", len(c.hooks.interaction))
	}
}

func TestInteractiveDefaultsKeepsTheCallersInteractionHook(t *testing.T) {
	mine := func(context.Context, *HookContext, QuestionRequest) (*QuestionAnswers, error) {
		return nil, nil
	}
	c := newConfig()
	WithInteractionHook(mine)(c)
	interactiveDefaults()(c)

	if len(c.hooks.interaction) != 1 {
		t.Errorf("got %d interaction hooks, want only the caller's", len(c.hooks.interaction))
	}
}

// ---------------------------------------------------------------------------
// Progress messages
// ---------------------------------------------------------------------------

func TestSpinnerMessage(t *testing.T) {
	tests := []struct {
		name string
		step Step
		want string
	}{
		{"one tool", Step{Type: StepToolCall, ToolCalls: []ToolCall{{Name: "view_file"}}}, "Running view_file..."},
		{
			"two tools",
			Step{Type: StepToolCall, ToolCalls: []ToolCall{{Name: "a"}, {Name: "b"}}},
			"Running a, b...",
		},
		{"unnamed tool step", Step{Type: StepToolCall}, "Running a tool..."},
		{"compaction", Step{Type: StepCompaction}, "Compacting the context..."},
		{"reasoning", Step{Source: SourceModel, ThinkingDelta: "hmm"}, "Reasoning..."},
		{"plain text", Step{Source: SourceModel, ContentDelta: "hello"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := spinnerMessage(tt.step); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpinnerIsInertWithoutATTY(t *testing.T) {
	// Tests do not run against a terminal, so the spinner must stay silent
	// rather than writing escape sequences into captured output.
	out := fakeTerminal(t, "")
	s := newSpinner("Thinking...")
	s.start()
	s.update("Running...")
	s.stopAndClear()

	if out.Len() != 0 {
		t.Errorf("the spinner wrote %q, want nothing", out)
	}
	if activeSpinner.Load() != nil {
		t.Error("the spinner is still registered as active")
	}
}

// interactiveAgent wires an agent to a fake harness and a scripted terminal,
// which is what the loop needs on both ends.
func interactiveAgent(t *testing.T, input string) (*Agent, *harness.FakeTransport, *bytes.Buffer) {
	t.Helper()

	a, fake := fakeAgent(t)
	t.Cleanup(func() {
		fake.Close()
		a.proc.stop()
	})
	return a, fake, fakeTerminal(t, input)
}

// answerTurn waits for the prompt to reach the harness and replies to it.
func answerTurn(t *testing.T, fake *harness.FakeTransport, answer string) {
	t.Helper()

	waitSent(t, fake)
	fake.Push(pb.OutputEvent_builder{StepUpdate: textStep("main", 0, answer, true)}.Build())
	fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
		}.Build(),
	}.Build())
}

// waitForInteract fails the test if the loop does not finish promptly.
func waitForInteract(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Interact never returned")
		return nil
	}
}

func TestInteract(t *testing.T) {
	a, fake, out := interactiveAgent(t, "hello\nexit\n")

	done := make(chan error, 1)
	go func() { done <- a.Interact(t.Context()) }()

	answerTurn(t, fake, "Hi there.")

	if err := waitForInteract(t, done); err != nil {
		t.Fatalf("Interact: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Agent: Hi there.") {
		t.Errorf("output = %q, want the agent's reply printed", got)
	}
	if !strings.Contains(out.String(), "Goodbye.") {
		t.Errorf("output = %q, want the exit acknowledged", out)
	}
}

func TestInteractIgnoresBlankLines(t *testing.T) {
	// A bare newline at the prompt is not a prompt, and must not start a turn.
	a, fake, _ := interactiveAgent(t, "\n   \nexit\n")

	done := make(chan error, 1)
	go func() { done <- a.Interact(t.Context()) }()

	if err := waitForInteract(t, done); err != nil {
		t.Fatalf("Interact: %v", err)
	}
	if sent := fake.Sent(); len(sent) != 0 {
		t.Errorf("the loop sent %v, want nothing for blank input", sent)
	}
}

func TestInteractEndsAtEOF(t *testing.T) {
	// Ctrl-D ends the session as cleanly as typing exit does.
	a, _, out := interactiveAgent(t, "")

	done := make(chan error, 1)
	go func() { done <- a.Interact(t.Context()) }()

	if err := waitForInteract(t, done); err != nil {
		t.Fatalf("Interact: %v", err)
	}
	if !strings.Contains(out.String(), "Goodbye.") {
		t.Errorf("output = %q, want the session closed politely", out)
	}
}

func TestInteractReportsATurnFailureAndCarriesOn(t *testing.T) {
	a, fake, out := interactiveAgent(t, "hello\nexit\n")

	done := make(chan error, 1)
	go func() { done <- a.Interact(t.Context()) }()

	waitSent(t, fake)
	fake.Push(pb.OutputEvent_builder{
		TrajectoryStateUpdate: pb.TrajectoryStateUpdate_builder{
			TrajectoryId: proto.String("main"),
			State:        pb.TrajectoryStateUpdate_STATE_FULLY_IDLE.Enum(),
			Error:        proto.String("the model refused"),
		}.Build(),
	}.Build())

	// A failed turn is reported at the prompt rather than ending the session,
	// so the user can try something else.
	if err := waitForInteract(t, done); err != nil {
		t.Fatalf("Interact: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "The turn failed") {
		t.Errorf("output = %q, want the failure reported", got)
	}
}

func TestInteractStopsWhenTheContextIsCancelled(t *testing.T) {
	a, fake, _ := interactiveAgent(t, "hello\nexit\n")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Interact(ctx) }()

	// Cancel mid-turn: the loop reports the cancellation instead of printing it
	// and asking for another prompt.
	waitSent(t, fake)
	cancel()

	if err := waitForInteract(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("Interact = %v, want the cancellation", err)
	}
}

func TestInteractBeforeStartup(t *testing.T) {
	var a Agent
	if err := a.Interact(t.Context()); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Interact = %v, want ErrNotStarted", err)
	}
}

func TestRunInteractiveReportsAStartupFailure(t *testing.T) {
	// There is no harness binary to start here, so this only proves the error
	// comes back rather than being swallowed by the loop's setup.
	fakeTerminal(t, "")
	t.Setenv("GEMINI_API_KEY", "test-key")

	err := RunInteractive(t.Context(), WithBinaryPath(filepath.Join(t.TempDir(), "absent")))
	if err == nil {
		t.Fatal("RunInteractive succeeded without a harness binary")
	}
}

// lockedBuffer is a writer safe to read while the spinner is animating. The
// real terminal has one writer, so the spinner does not lock on its own.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSpinnerAnimates(t *testing.T) {
	// The spinner is disabled off a TTY, which every test runs on, so it is
	// enabled by hand here to exercise the animation itself.
	out := &lockedBuffer{}
	prev := termOut
	termOut = out
	t.Cleanup(func() { termOut = prev })

	s := &spinner{enabled: true, message: "Thinking..."}
	s.start()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Thinking...") {
		if time.Now().After(deadline) {
			t.Fatalf("the spinner never drew a frame (output %q)", out)
		}
		time.Sleep(time.Millisecond)
	}
	s.update("Running a tool...")
	s.stopAndClear()

	// Stopping clears the line, so the next thing printed starts clean.
	if !strings.HasSuffix(out.String(), "\r\033[K") {
		t.Errorf("output = %q, want the spinner line cleared", out)
	}
	if activeSpinner.Load() != nil {
		t.Error("the spinner is still registered as active")
	}
}
