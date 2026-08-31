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
	"strings"
	"sync"
)

// ChatResponse is a live turn returned by [Agent.Chat] or
// [Conversation.Chat].
//
// Nothing is read from the harness until the response is consumed. Every
// accessor — [ChatResponse.Chunks], [ChatResponse.Text],
// [ChatResponse.Thoughts], [ChatResponse.ToolCalls] — returns an independent
// cursor over one shared buffer, so several of them can walk the same turn
// without competing for it. A cursor that reaches the live edge pulls from the
// network, and what it pulls becomes visible to the others.
//
// Cursors may be consumed sequentially or concurrently. Abandoning a response
// without draining it leaves the turn running: call [ChatResponse.Close], or
// send again, to release it.
type ChatResponse struct {
	conv *Conversation

	// mu guards everything below, and serializes network pulls so only one
	// cursor is ever advancing the underlying stream.
	mu   sync.Mutex
	next func() (Chunk, error, bool)
	stop func()
	buf  []chunkEntry
	done bool
}

// chunkEntry is one buffered stream event. A turn can report a recoverable
// error and keep going, so errors are buffered in sequence alongside chunks
// rather than kept as a single terminal value.
type chunkEntry struct {
	chunk Chunk
	err   error
}

// newChatResponse wraps the conversation's chunk stream in a replayable
// buffer. It registers itself as the conversation's outstanding response so
// the next send can reclaim the turn.
func newChatResponse(ctx context.Context, conv *Conversation) *ChatResponse {
	next, stop := iter.Pull2(conv.Chunks(ctx))
	r := &ChatResponse{conv: conv, next: next, stop: stop}

	conv.mu.Lock()
	conv.current = r
	conv.mu.Unlock()

	return r
}

// at returns the buffered entry at pos, pulling from the network for as long
// as the buffer falls short. It reports false once the turn is over.
//
// The lock is held across the pull. Other cursors block, but they are waiting
// at the same live edge and have nothing to do until this pull lands.
func (r *ChatResponse) at(pos int) (chunkEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for pos >= len(r.buf) {
		if r.done {
			return chunkEntry{}, false
		}
		chunk, err, ok := r.next()
		if !ok {
			r.done = true
			r.stop()
			return chunkEntry{}, false
		}
		r.buf = append(r.buf, chunkEntry{chunk: chunk, err: err})
	}
	return r.buf[pos], true
}

// Chunks returns a cursor over every semantic event in the turn: text deltas,
// reasoning deltas, and tool calls.
//
// A non-nil error accompanies a nil [Chunk]. It reports a failure within the
// turn, which may still continue afterwards; the cursor ends when the turn
// does.
func (r *ChatResponse) Chunks() iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		for pos := 0; ; pos++ {
			entry, ok := r.at(pos)
			if !ok {
				return
			}
			if !yield(entry.chunk, entry.err) {
				return
			}
		}
	}
}

// Text returns a cursor over the response's text deltas. Concatenating them
// reproduces the full response.
//
// This is the common case: printing the agent's answer as it is written.
func (r *ChatResponse) Text() iter.Seq2[string, error] {
	return filterChunks(r, func(c Chunk) (string, bool) {
		t, ok := c.(TextChunk)
		return t.Text, ok
	})
}

// Thoughts returns a cursor over the model's reasoning deltas.
func (r *ChatResponse) Thoughts() iter.Seq2[string, error] {
	return filterChunks(r, func(c Chunk) (string, bool) {
		t, ok := c.(ThoughtChunk)
		return t.Text, ok
	})
}

// ToolCalls returns a cursor over the tool invocations of the turn, delivered
// as they are dispatched.
func (r *ChatResponse) ToolCalls() iter.Seq2[ToolCall, error] {
	return filterChunks(r, func(c Chunk) (ToolCall, bool) {
		t, ok := c.(ToolCall)
		return t, ok
	})
}

// filterChunks builds a cursor over the subset of chunks that convert, while
// passing every error through so no failure is silently dropped.
func filterChunks[T any](r *ChatResponse, convert func(Chunk) (T, bool)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		for chunk, err := range r.Chunks() {
			if err != nil {
				if !yield(zero, err) {
					return
				}
				continue
			}
			if v, ok := convert(chunk); ok {
				if !yield(v, nil) {
					return
				}
			}
		}
	}
}

// Resolve drains the turn and returns every chunk it produced.
//
// The returned error is the last one the turn reported, if any; the chunks
// received before it are still returned.
func (r *ChatResponse) Resolve() ([]Chunk, error) {
	var (
		chunks  []Chunk
		lastErr error
	)
	for chunk, err := range r.Chunks() {
		if err != nil {
			lastErr = err
			continue
		}
		chunks = append(chunks, chunk)
	}
	return chunks, lastErr
}

// Wait drains the turn and returns the complete response text.
//
// Use it when the streaming is incidental and only the final answer matters.
func (r *ChatResponse) Wait() (string, error) {
	var (
		b       strings.Builder
		lastErr error
	)
	for text, err := range r.Text() {
		if err != nil {
			lastErr = err
			continue
		}
		b.WriteString(text)
	}
	return b.String(), lastErr
}

// StructuredOutput drains the turn and returns the structured payload the
// agent produced, or nil if it produced none.
//
// Use [json.Unmarshal] to decode it into your own type.
func (r *ChatResponse) StructuredOutput() (json.RawMessage, error) {
	if _, err := r.Resolve(); err != nil {
		return nil, err
	}
	return r.conv.LastStructuredOutput(), nil
}

// Usage reports the tokens this turn consumed. It is nil until the turn ends.
func (r *ChatResponse) Usage() *UsageMetadata { return r.conv.LastTurnUsage() }

// StopReason explains why the turn ended, and is [StopUnspecified] for a turn
// that ran to completion normally.
func (r *ChatResponse) StopReason() StopReason { return r.conv.StopReason() }

// Cancel halts generation. It is a no-op once the turn has finished.
func (r *ChatResponse) Cancel(ctx context.Context) error {
	if r.finished() {
		return nil
	}
	return r.conv.Cancel(ctx)
}

// Close abandons the turn's remaining output and releases the underlying
// stream. Cursors created afterwards see only what was already buffered.
//
// It is safe to call more than once, and unnecessary for a response that has
// been drained. Close must not race a cursor that is still reading.
func (r *ChatResponse) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil
	}
	r.done = true
	r.stop()
	return nil
}

// finished reports whether the stream has been exhausted or closed.
func (r *ChatResponse) finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}
