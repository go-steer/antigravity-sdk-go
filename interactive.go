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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Terminal helpers for driving an agent by hand.
//
// Everything here reads os.Stdin and writes os.Stdout, which makes it useful
// for local development and debugging and unsuitable for anything running
// unattended. A server should implement the same hooks against its own
// transport instead.

// ---------------------------------------------------------------------------
// Reading a line
// ---------------------------------------------------------------------------

// termIn and termOut are the terminal the helpers in this file talk to. They
// are variables so a test can drive them without a console.
var (
	termIn            = newLineReader(os.Stdin)
	termOut io.Writer = os.Stdout
)

type lineResult struct {
	line string
	err  error
}

// lineReader reads lines on demand from a background goroutine, which is what
// makes a read cancellable: an os.Stdin read cannot be interrupted, so the
// reader is left blocked and the caller walks away from it.
type lineReader struct {
	src   io.Reader
	start sync.Once
	reqs  chan chan lineResult
}

func newLineReader(src io.Reader) *lineReader {
	return &lineReader{src: src, reqs: make(chan chan lineResult)}
}

// readLine returns the next line of input, without its newline.
//
// It reports [io.EOF] when input is exhausted, which is how a terminal helper
// recognizes Ctrl-D.
func (r *lineReader) readLine(ctx context.Context) (string, error) {
	r.start.Do(func() { go r.serve() })

	reply := make(chan lineResult, 1)
	select {
	case r.reqs <- reply:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case res := <-reply:
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// serve hands out one line per request. Once the source is exhausted or fails,
// every later request gets the same answer: input does not come back.
func (r *lineReader) serve() {
	scanner := bufio.NewScanner(r.src)
	for reply := range r.reqs {
		if scanner.Scan() {
			reply <- lineResult{line: scanner.Text()}
			continue
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		reply <- lineResult{err: err}
		for reply := range r.reqs {
			reply <- lineResult{err: err}
		}
		return
	}
}

// prompt writes a prompt and reads the answer, pausing the spinner around it so
// the two do not fight over the line.
func prompt(ctx context.Context, text string) (string, error) {
	sp := activeSpinner.Load()
	if sp != nil {
		sp.pause()
		defer sp.resume()
	}
	fmt.Fprint(termOut, text)
	line, err := termIn.readLine(ctx)
	return strings.TrimSpace(line), err
}

// ---------------------------------------------------------------------------
// Spinner
// ---------------------------------------------------------------------------

// activeSpinner is the spinner the terminal prompts should step around. There
// is at most one, because there is only one terminal.
var activeSpinner atomic.Pointer[spinner]

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner shows progress on a TTY while the agent works.
//
// When stdout is not a terminal it does nothing at all, so redirecting output
// to a file does not fill it with escape sequences.
type spinner struct {
	enabled bool

	mu      sync.Mutex
	message string
	running bool
	stop    chan struct{}
	done    chan struct{}
}

func newSpinner(message string) *spinner {
	info, err := os.Stdout.Stat()
	tty := err == nil && info.Mode()&os.ModeCharDevice != 0
	return &spinner{enabled: tty, message: message}
}

// start begins animating and installs the spinner as the active one.
func (s *spinner) start() {
	activeSpinner.Store(s)
	s.resume()
}

// update changes the message shown on the next frame.
func (s *spinner) update(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = message
}

// resume starts the animation, doing nothing if it is already running.
func (s *spinner) resume() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.spin(s.stop, s.done)
}

// pause stops the animation and clears the line, so whatever prints next
// starts from a clean one.
func (s *spinner) pause() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	stop, done := s.stop, s.done
	s.mu.Unlock()

	close(stop)
	<-done
	fmt.Fprint(termOut, "\r\033[K")
}

// stopAndClear ends the spinner for good.
func (s *spinner) stopAndClear() {
	s.pause()
	activeSpinner.CompareAndSwap(s, nil)
}

func (s *spinner) spin(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; ; i++ {
		s.mu.Lock()
		message := s.message
		s.mu.Unlock()
		fmt.Fprintf(termOut, "\r\033[K%s %s", spinnerFrames[i%len(spinnerFrames)], message)

		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// ---------------------------------------------------------------------------
// Terminal hooks and handlers
// ---------------------------------------------------------------------------

// ConfirmInTerminal returns an [AskUserHandler] that prints the tool call and
// asks on stdin whether to allow it.
//
// Anything but y or yes denies the call, and so does end-of-input: an
// unattended run must not approve by accident.
//
//	antigravity.WithPolicies(antigravity.ConfirmRunCommand(antigravity.ConfirmInTerminal()))
func ConfirmInTerminal() AskUserHandler {
	return func(ctx context.Context, call ToolCall) (bool, error) {
		fmt.Fprintf(termOut, "\nThe agent wants to call %s.\n", call.Name)
		if len(call.Args) > 0 {
			fmt.Fprintf(termOut, "Arguments: %s\n", call.Args)
		}

		answer, err := prompt(ctx, "Allow it? (y/N) ")
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(termOut)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return isYes(answer), nil
	}
}

// ConfirmToolCallsInTerminal returns a [PreToolCallHook] that asks on stdin
// before every tool call.
//
// This is a blunter instrument than [ConfirmInTerminal]: a policy can gate one
// tool, while this gates all of them.
func ConfirmToolCallsInTerminal() PreToolCallHook {
	confirm := ConfirmInTerminal()
	return func(ctx context.Context, _ *HookContext, call ToolCall) (ToolDecision, error) {
		allow, err := confirm(ctx, call)
		switch {
		case err != nil:
			return ToolDecision{}, err
		case allow:
			return ToolDecision{}, nil
		default:
			return ToolDecision{Deny: true, Reason: "The user denied this tool call."}, nil
		}
	}
}

// AnswerQuestionsInTerminal returns an [OnInteractionHook] that puts the
// agent's questions on stdout and reads the answers from stdin.
//
// An answer that names an option, by number or by its exact text, is sent as a
// selection; anything else is sent as freeform text. An empty line skips the
// question, and end-of-input skips the rest of them.
func AnswerQuestionsInTerminal() OnInteractionHook {
	return func(ctx context.Context, _ *HookContext, req QuestionRequest) (*QuestionAnswers, error) {
		answers := make([]Answer, 0, len(req.Questions))

		for _, q := range req.Questions {
			fmt.Fprintf(termOut, "\n%s\n", q.Text)
			for i, opt := range q.Options {
				fmt.Fprintf(termOut, "  %d. %s\n", i+1, opt.Text)
			}
			if q.MultiSelect && len(q.Options) > 0 {
				fmt.Fprintln(termOut, "  (several may be chosen, separated by commas)")
			}

			reply, err := prompt(ctx, "Answer: ")
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(termOut)
				// The remaining questions go unanswered rather than being
				// answered badly; a short slice is a valid response.
				return &QuestionAnswers{Answers: answers}, nil
			}
			if err != nil {
				return nil, err
			}
			answers = append(answers, parseAnswer(reply, q))
		}
		return &QuestionAnswers{Answers: answers}, nil
	}
}

// parseAnswer interprets a typed reply against the options offered.
func parseAnswer(reply string, q Question) Answer {
	if reply == "" {
		return Answer{Skipped: true}
	}

	var selected []int
	fields := strings.Split(reply, ",")
	if !q.MultiSelect {
		fields = []string{reply}
	}
	for _, field := range fields {
		if i, ok := matchOption(strings.TrimSpace(field), q.Options); ok {
			selected = append(selected, i)
		}
	}
	// A reply that only partly matched is sent verbatim instead: guessing at
	// the rest would put words in the user's mouth.
	if len(selected) == len(fields) {
		return Answer{SelectedOptions: selected}
	}
	return Answer{Text: reply}
}

// matchOption resolves a reply to an option index, by 1-based number first and
// then by case-insensitive text.
func matchOption(reply string, options []QuestionOption) (int, bool) {
	if reply == "" || len(options) == 0 {
		return 0, false
	}
	if n, err := strconv.Atoi(reply); err == nil {
		if n >= 1 && n <= len(options) {
			return n - 1, true
		}
		return 0, false
	}
	for i, opt := range options {
		if strings.EqualFold(reply, opt.Text) {
			return i, true
		}
	}
	return 0, false
}

func isYes(answer string) bool {
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// The loop
// ---------------------------------------------------------------------------

// RunInteractive starts an agent and drives it from the terminal, returning
// when the user exits.
//
// It is a development and debugging tool. On top of the options given it
// switches the agent to [BehaviorInteractive], answers the agent's questions
// from stdin unless an interaction hook is already registered, and upgrades a
// plain run_command denial into a confirmation prompt — so the default policy
// set asks rather than refuses. Everything else is left as configured.
//
// Type exit or quit, or press Ctrl-D, to end the session.
func RunInteractive(ctx context.Context, opts ...Option) error {
	// The clone keeps the append from writing into an array the caller holds.
	agent, err := New(ctx, append(slices.Clone(opts), interactiveDefaults())...)
	if err != nil {
		return err
	}
	// Interact's error is the one worth reporting; a close failure on the way
	// out of a session that already ended tells the caller nothing actionable.
	defer func() { _ = agent.Close() }()

	return agent.Interact(ctx)
}

// interactiveDefaults adapts a configuration for terminal use. It runs last,
// so it sees what the caller actually asked for.
func interactiveDefaults() Option {
	return func(c *config) {
		c.capabilities.Behavior = BehaviorInteractive
		if len(c.hooks.interaction) == 0 {
			c.hooks.interaction = append(c.hooks.interaction, AnswerQuestionsInTerminal())
		}
		c.policies = confirmInsteadOfDeny(c.policies)
	}
}

// confirmInsteadOfDeny turns an unconditional run_command denial into a
// terminal confirmation.
//
// Denying run_command outright is the right default for an unattended agent
// and the wrong one at a prompt, where there is a human present to ask. Only
// the plain form is upgraded: a denial carrying a predicate expresses a rule
// the caller wrote deliberately, and is left alone.
func confirmInsteadOfDeny(policies []Policy) []Policy {
	out := slices.Clone(policies)

	for i, p := range out {
		if p.Tool != string(ToolRunCommand) || p.Decision != DecisionDeny || p.When != nil {
			continue
		}
		name := p.Name
		if name == "" {
			name = "interactive_confirm"
		}
		out[i] = AskUserTool(ToolRunCommand, ConfirmInTerminal(), Named(name))
	}
	return out
}

// Interact drives an already-started agent from the terminal, reading prompts
// from stdin and printing each response.
//
// Unlike [RunInteractive] it changes nothing about the configuration, so an
// agent that was not built with [BehaviorInteractive] cannot ask the user
// questions mid-turn. It returns when the user exits or ctx is cancelled, and
// does not close the agent.
func (a *Agent) Interact(ctx context.Context) error {
	if a.conv == nil {
		return ErrNotStarted
	}
	fmt.Fprintln(termOut, "Interactive session. Type exit or quit to end it.")

	for {
		line, err := prompt(ctx, "\nYou: ")
		switch {
		case errors.Is(err, io.EOF):
			fmt.Fprintln(termOut, "\nGoodbye.")
			return nil
		case err != nil:
			return err
		}

		switch strings.ToLower(line) {
		case "":
			continue
		case "exit", "quit":
			fmt.Fprintln(termOut, "Goodbye.")
			return nil
		}

		if err := a.runTurn(ctx, line); err != nil {
			return err
		}
	}
}

// turn runs one exchange, showing progress while the agent works and printing
// the response it settles on.
func (a *Agent) runTurn(ctx context.Context, input string) error {
	if err := a.conv.Send(ctx, Text(input)); err != nil {
		return err
	}

	sp := newSpinner("Thinking...")
	sp.start()
	defer sp.stopAndClear()

	var response string
	var turnErr error
	for step, err := range a.conv.Steps(ctx) {
		if err != nil {
			// A failure within a turn is worth reporting, but the turn may
			// continue afterwards, so it does not end the session.
			turnErr = err
			continue
		}
		if msg := spinnerMessage(step); msg != "" {
			sp.update(msg)
		}
		if step.IsCompleteResponse {
			response = step.Content
		}
	}
	sp.stopAndClear()

	if turnErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return turnErr
		}
		fmt.Fprintf(termOut, "\nThe turn failed: %v\n", turnErr)
	}
	if response != "" {
		fmt.Fprintf(termOut, "\nAgent: %s\n", response)
	}
	return nil
}

// spinnerMessage describes what the agent is doing, or returns an empty string
// for a step not worth announcing.
func spinnerMessage(step Step) string {
	switch {
	case step.Type == StepToolCall && len(step.ToolCalls) == 1:
		return fmt.Sprintf("Running %s...", step.ToolCalls[0].Name)
	case step.Type == StepToolCall && len(step.ToolCalls) > 1:
		names := make([]string, 0, len(step.ToolCalls))
		for _, call := range step.ToolCalls {
			names = append(names, call.Name)
		}
		return fmt.Sprintf("Running %s...", strings.Join(names, ", "))
	case step.Type == StepToolCall:
		return "Running a tool..."
	case step.Type == StepCompaction:
		return "Compacting the context..."
	case step.Source == SourceModel && step.ThinkingDelta != "":
		return "Reasoning..."
	default:
		return ""
	}
}
