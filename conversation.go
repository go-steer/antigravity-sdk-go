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
	"iter"
	"slices"
	"sync"
)

// defaultMaxHistory bounds retained steps so a long-running session does not
// grow without limit.
const defaultMaxHistory = 10000

// Conversation is a stateful session with the agent.
//
// It accumulates the step history, tracks where turns began and where the
// model's context was compacted, and exposes the streaming primitives that
// [Agent.Chat] is built on. Most callers want [Agent.Chat]; reach for a
// Conversation directly when you need history, usage, or manual send and
// receive control.
//
// A Conversation supports one turn at a time. Sending while a turn is still
// running drains the remainder of that turn into history first.
type Conversation struct {
	proc *eventProcessor

	// sendMu serializes turns, so two goroutines cannot interleave prompts.
	sendMu sync.Mutex
	// recvMu guards the step stream, so only one reader drains a turn.
	recvMu sync.Mutex

	mu             sync.Mutex
	steps          []Step
	turnStarts     []int
	compactions    []int
	maxHistory     int
	turnStartUsage UsageMetadata
	lastTurnUsage  *UsageMetadata
	// current is the most recent streaming response, kept so an abandoned one
	// can be released before the next turn starts.
	current *ChatResponse
}

// newConversation builds a conversation over a running event processor,
// seeded with any history the harness restored.
func newConversation(proc *eventProcessor, history []Step, maxHistory int) *Conversation {
	if maxHistory <= 0 {
		maxHistory = defaultMaxHistory
	}
	c := &Conversation{proc: proc, maxHistory: maxHistory}
	for _, s := range history {
		c.record(s)
	}
	return c
}

// record appends a step to history, noting compactions and enforcing the cap.
func (c *Conversation) record(s Step) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.steps = append(c.steps, s)
	if s.Type == StepCompaction {
		c.compactions = append(c.compactions, len(c.steps)-1)
	}
	c.trimLocked()
}

// trimLocked drops the oldest steps once history exceeds its cap, shifting the
// recorded indices to match.
func (c *Conversation) trimLocked() {
	overflow := len(c.steps) - c.maxHistory
	if overflow <= 0 {
		return
	}
	c.steps = slices.Delete(c.steps, 0, overflow)

	shift := func(indices []int) []int {
		out := indices[:0]
		for _, i := range indices {
			if i >= overflow {
				out = append(out, i-overflow)
			}
		}
		return out
	}
	c.turnStarts = shift(c.turnStarts)
	c.compactions = shift(c.compactions)
}

// ---------------------------------------------------------------------------
// Sending and receiving
// ---------------------------------------------------------------------------

// Send delivers a prompt to the agent and returns as soon as it is on the
// wire. Read the resulting steps with [Conversation.Steps] or
// [Conversation.Chunks].
//
// If a previous turn is still running, its remaining steps are drained into
// history first, so the new prompt does not interleave with it.
func (c *Conversation) Send(ctx context.Context, prompt ...Content) error {
	if err := validatePrompt(prompt); err != nil {
		return err
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	// An undrained response still holds the step stream. Releasing it first is
	// what lets a caller stop reading mid-turn and simply send again.
	c.mu.Lock()
	previous := c.current
	c.current = nil
	c.mu.Unlock()
	if previous != nil {
		// Close reports what the abandoned turn ended with, which is the
		// previous caller's business. This one hears about it through the
		// drain below.
		_ = previous.Close()
	}

	// Idleness alone does not mean the last turn is accounted for: a turn that
	// ended before anyone read it leaves its events queued, and one of them may
	// be the failure that ended it. Draining is what surfaces that.
	if !c.proc.isIdle() || c.proc.hasPending() {
		if err := c.drain(ctx); err != nil {
			return err
		}
	}

	c.mu.Lock()
	c.turnStarts = append(c.turnStarts, len(c.steps))
	c.turnStartUsage = c.proc.usage()
	c.lastTurnUsage = nil
	c.mu.Unlock()

	c.proc.resetForTurn()
	return c.proc.sendUserInput(ctx, prompt)
}

// drain consumes the rest of the current turn into history, discarding the
// steps. It is what lets a caller abandon a stream and still send again.
func (c *Conversation) drain(ctx context.Context) error {
	var err error
	for _, e := range c.Steps(ctx) {
		if e != nil {
			err = e
		}
	}
	return err
}

// Steps yields each step of the current turn as it arrives, ending when the
// agent goes idle.
//
// Steps are recorded in history as they are yielded. A non-nil error
// accompanies a zero [Step] and reports a failure within the turn; the turn
// may continue afterwards, so iteration ends only when the agent goes idle.
//
// Only one goroutine may read a turn at a time.
func (c *Conversation) Steps(ctx context.Context) iter.Seq2[Step, error] {
	return func(yield func(Step, error) bool) {
		c.recvMu.Lock()
		defer c.recvMu.Unlock()

		for {
			select {
			case <-ctx.Done():
				yield(Step{}, ctx.Err())
				return

			case ev := <-c.proc.steps:
				switch {
				case ev.idle:
					// The read loop queues this marker and only then opens the
					// idle gate, so a reader can get here first. Opening it
					// ourselves is what makes "iteration ended" mean "the turn
					// is over" to the next Send, which would otherwise see a
					// running turn with an empty queue and drain forever.
					// markIdle is idempotent; the read loop's own call is then
					// a no-op.
					c.proc.markIdle()
					c.finishTurn()
					return
				case ev.err != nil:
					if !yield(Step{}, ev.err) {
						return
					}
				default:
					c.record(ev.step)
					if !yield(ev.step, nil) {
						return
					}
				}
			}
		}
	}
}

// finishTurn records the usage the turn consumed, derived from the difference
// between the cumulative totals before and after.
func (c *Conversation) finishTurn() {
	after := c.proc.usage()

	c.mu.Lock()
	defer c.mu.Unlock()
	delta := after.Sub(c.turnStartUsage)
	c.lastTurnUsage = &delta
}

// Chunks yields the turn's semantic events: text deltas, reasoning deltas, and
// tool calls.
//
// Tool calls are deduplicated by id, because the agent loop reports the same
// call across several step transitions. A call without an id is always
// yielded, since it cannot be recognized as a repeat.
func (c *Conversation) Chunks(ctx context.Context) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		seen := map[string]bool{}

		for step, err := range c.Steps(ctx) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}

			if step.Source == SourceModel && step.Target == TargetUser {
				if step.ThinkingDelta != "" {
					if !yield(ThoughtChunk{StepIndex: step.Index, Text: step.ThinkingDelta}, nil) {
						return
					}
				}
				if step.ContentDelta != "" {
					if !yield(TextChunk{StepIndex: step.Index, Text: step.ContentDelta}, nil) {
						return
					}
				}
			}

			for _, call := range step.ToolCalls {
				if call.ID != "" {
					if seen[call.ID] {
						continue
					}
					seen[call.ID] = true
				}
				if !yield(call, nil) {
					return
				}
			}
		}
	}
}

// Chat sends a prompt and returns a [ChatResponse] that streams the turn.
//
// It returns as soon as the prompt is sent; nothing is generated until the
// response is read.
func (c *Conversation) Chat(ctx context.Context, prompt ...Content) (*ChatResponse, error) {
	if err := c.Send(ctx, prompt...); err != nil {
		return nil, err
	}
	return newChatResponse(ctx, c), nil
}

// ---------------------------------------------------------------------------
// History and state
// ---------------------------------------------------------------------------

// History returns every step received so far, across all turns.
//
// This is the full transcript, including steps the model may no longer have in
// context. Use [Conversation.CompactionIndices] to find where its context was
// compacted.
func (c *Conversation) History() []Step {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.steps)
}

// LastResponse returns the text of the most recent completed model response,
// or an empty string if there has not been one.
func (c *Conversation) LastResponse() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.steps) - 1; i >= 0; i-- {
		if c.steps[i].IsCompleteResponse {
			return c.steps[i].Content
		}
	}
	return ""
}

// LastStructuredOutput returns the payload of the most recent finish step, or
// nil if no step carried one.
func (c *Conversation) LastStructuredOutput() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.steps) - 1; i >= 0; i-- {
		if c.steps[i].Type == StepFinish {
			return c.steps[i].StructuredOutput
		}
	}
	return nil
}

// TurnCount returns how many prompts have been sent on this conversation.
func (c *Conversation) TurnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.turnStarts)
}

// CompactionIndices returns the history positions where the model's context
// was compacted.
func (c *Conversation) CompactionIndices() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.compactions)
}

// ClearHistory discards the recorded transcript, freeing memory in a
// long-running session. The conversation itself stays active.
func (c *Conversation) ClearHistory() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = nil
	c.turnStarts = nil
	c.compactions = nil
	c.lastTurnUsage = nil
	c.turnStartUsage = UsageMetadata{}
}

// Usage returns the session's cumulative token usage.
func (c *Conversation) Usage() UsageMetadata { return c.proc.usage() }

// UsageByTrajectory returns cumulative usage per trajectory, which separates
// the main agent from its subagents.
func (c *Conversation) UsageByTrajectory() map[string]UsageMetadata {
	return c.proc.usageByTrajectory()
}

// LastTurnUsage returns the tokens the most recent completed turn consumed, or
// nil if no turn has finished.
func (c *Conversation) LastTurnUsage() *UsageMetadata {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastTurnUsage == nil {
		return nil
	}
	u := *c.lastTurnUsage
	return &u
}

// StopReason explains why the most recent turn ended. It is
// [StopUnspecified] for a turn that ran to completion normally.
func (c *Conversation) StopReason() StopReason { return c.proc.stopReason() }

// IsIdle reports whether the agent is currently between turns.
func (c *Conversation) IsIdle() bool { return c.proc.isIdle() }

// WaitForIdle blocks until the current turn finishes.
func (c *Conversation) WaitForIdle(ctx context.Context) error {
	select {
	case <-c.proc.idleSignal():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cancel asks the agent to stop the current turn.
//
// It returns once the request is sent. The turn ends asynchronously, and its
// reader receives an error wrapping [ErrCancelled].
func (c *Conversation) Cancel(ctx context.Context) error { return c.proc.halt(ctx) }

// Trigger pushes a message into the agent from outside the conversation,
// starting a turn the same way a prompt would.
//
// It is what a [Trigger] uses to report an external event — a timer firing, a
// file changing, a webhook arriving. Unlike [Conversation.Send], it does not
// wait for the turn to be readable, so the caller is free to fire and forget.
func (c *Conversation) Trigger(ctx context.Context, message string) error {
	return c.proc.sendTrigger(ctx, message)
}
