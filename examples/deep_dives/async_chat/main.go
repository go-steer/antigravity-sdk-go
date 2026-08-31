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

// Command async_chat runs a group discussion with no turn order.
//
// Compare examples/deep_dives/round_based_chat, where every agent speaks once
// per round in lockstep. Here each agent owns a goroutine, wakes whenever
// anyone posts, and speaks as soon as it has something to say. Ordering is
// emergent: whoever finishes Chat first gets the next word.
//
// Trade-offs against the round-based model:
//
//   - Nobody waits on a slow peer, and the discussion ends by itself once
//     agents run out of things to add.
//   - A consistently fast agent can dominate, and an agent may answer before
//     it has seen every message.
//
// The wake-up mechanism is the standard Go broadcast: a channel that is closed
// and replaced on every post, so waiters can select on it alongside
// ctx.Done().
//
//	go run ./examples/deep_dives/async_chat
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

const (
	passToken        = "[PASS]"
	maxPassesInARow  = 2
	discussionBudget = 2 * time.Minute
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// passTurn lets an agent decline a turn. Without it the model has no way to
// say nothing, and the discussion never converges.
func passTurn(context.Context, struct{}) (string, error) { return passToken, nil }

// ---------------------------------------------------------------------------
// The room
// ---------------------------------------------------------------------------

type message struct {
	sender string
	text   string
}

// room is the shared transcript plus a broadcast for new arrivals.
type room struct {
	mu      sync.Mutex
	history []message
	// changed is closed on every post and replaced with a fresh channel, which
	// is how a waiter learns there is something new without polling.
	changed chan struct{}
}

func newRoom() *room { return &room{changed: make(chan struct{})} }

func (r *room) post(m message) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.history = append(r.history, m)
	close(r.changed)
	r.changed = make(chan struct{})
}

// since blocks until the transcript is longer than n, then returns everything
// after n along with the new length.
func (r *room) since(ctx context.Context, n int) ([]message, int, error) {
	for {
		r.mu.Lock()
		if len(r.history) > n {
			fresh := append([]message(nil), r.history[n:]...)
			total := len(r.history)
			r.mu.Unlock()
			return fresh, total, nil
		}
		// Take the current channel before unlocking: a post between the unlock
		// and the select closes this exact channel, so the wake-up is not lost.
		changed := r.changed
		r.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return nil, n, ctx.Err()
		}
	}
}

func (r *room) transcript() []message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]message(nil), r.history...)
}

// ---------------------------------------------------------------------------
// One agent's independent loop
// ---------------------------------------------------------------------------

func participate(ctx context.Context, name string, agent *antigravity.Agent, r *room) error {
	seen, passes := 0, 0

	for passes < maxPassesInARow {
		fresh, total, err := r.since(ctx, seen)
		if err != nil {
			// The deadline fired or a peer failed; leaving quietly is correct.
			return nil
		}
		seen = total

		// Skip this agent's own posts, which are already in its history, along
		// with passes and empty replies.
		var unseen []message
		for _, m := range fresh {
			if m.sender != name && m.text != "" && !strings.Contains(m.text, passToken) {
				unseen = append(unseen, m)
			}
		}
		if len(unseen) == 0 {
			continue
		}

		resp, err := agent.Chat(ctx, antigravity.Text(prompt(unseen)))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		text, err := resp.Wait()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		text = strings.TrimSpace(text)

		if text == "" || strings.Contains(text, passToken) {
			passes++
			fmt.Printf("\n  %s: (pass)\n", name)
		} else {
			passes = 0
			fmt.Printf("\n  %s: %s\n", name, text)
		}

		// Passes are posted too. If every agent passed silently, nobody would
		// wake anybody, and the whole room would block until the deadline.
		r.post(message{sender: name, text: text})
		seen = len(r.transcript())
	}

	fmt.Printf("\n  %s is leaving the discussion.\n", name)
	return nil
}

// prompt formats only what this agent has not seen. Agents are stateful, so
// resending the whole transcript would duplicate their own context.
//
// Note that this splices other agents' raw output into a prompt. A production
// version should fence or structure the untrusted text, since one agent can
// otherwise write something that steers the next.
func prompt(unseen []message) string {
	var b strings.Builder
	b.WriteString("New messages from other agents:\n\n")
	for i, m := range unseen {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[%s]: %s", m.sender, m.text)
	}
	b.WriteString("\n\nRespond to the latest messages. Address other agents " +
		"by name when you agree, disagree, or build on their points. Keep it " +
		"under 3 sentences. If you have nothing to add, call pass_turn().")
	return b.String()
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

var personas = []struct {
	name         string
	instructions string
}{{
	"Pragmatic Priya",
	"You are Pragmatic Priya, a senior engineer in a group chat with " +
		"Visionary Vince (a futurist thinker) and Cautious Cora (a risk " +
		"analyst). Focus on what is technically feasible today.\n\n" +
		"- Refer to Vince and Cora by name when responding to their points.\n" +
		"- Ground speculative ideas in current engineering constraints.\n" +
		"- If the topic is purely theoretical, call pass_turn().\n" +
		"- Keep responses under 3 sentences.",
}, {
	"Visionary Vince",
	"You are Visionary Vince, a futurist thinker in a group chat with " +
		"Pragmatic Priya (a senior engineer) and Cautious Cora (a risk " +
		"analyst). Paint bold pictures of what is possible in 10-20 years.\n\n" +
		"- Refer to Priya and Cora by name when building on their points.\n" +
		"- Only respond when you have a genuinely forward-looking angle.\n" +
		"- If the discussion is purely about present-day details, call pass_turn().\n" +
		"- Keep responses under 3 sentences.",
}, {
	"Cautious Cora",
	"You are Cautious Cora, a risk analyst in a group chat with Pragmatic " +
		"Priya (an engineer) and Visionary Vince (a futurist). Identify what " +
		"could go wrong.\n\n" +
		"- Refer to Priya and Vince by name when questioning their claims.\n" +
		"- If everyone is being sufficiently cautious, call pass_turn().\n" +
		"- Be constructive: flag risks with mitigations, not just doom.\n" +
		"- Keep responses under 3 sentences.",
}}

func run(ctx context.Context) error {
	const topic = "Should AI agents be allowed to autonomously deploy code to production?"

	fmt.Println("Async agent chat (no rounds)")
	fmt.Printf("\n%s\nTopic: %s\n%s\n", strings.Repeat("=", 60), topic, strings.Repeat("=", 60))

	pass := antigravity.MustNewTool("pass_turn",
		"Decline to respond this turn. Call this when the topic is outside "+
			"your expertise, you agree with what has been said, or your input "+
			"would be redundant.", passTurn)

	r := newRoom()

	// The discussion is bounded in wall-clock time as well as by passes, in
	// case the agents keep finding things to say.
	ctx, cancel := context.WithTimeout(ctx, discussionBudget)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(personas))

	for i, p := range personas {
		agent, err := antigravity.New(ctx,
			antigravity.WithSystemPrompt(p.instructions),
			antigravity.WithTools(pass),
		)
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		defer agent.Close()

		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = participate(ctx, p.name, agent, r)
		}()
	}

	// Seeding the topic is what wakes the loops up for the first time.
	r.post(message{sender: "User", text: topic})
	wg.Wait()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Printf("\n  Stopped after %s.\n", discussionBudget)
	} else {
		fmt.Println("\n  All agents finished.")
	}

	history := r.transcript()
	fmt.Printf("\n%s\nTranscript (%d turns)\n%s\n", strings.Repeat("=", 60), len(history), strings.Repeat("=", 60))
	for i, m := range history {
		fmt.Printf("  %d. [%s]: %s\n", i+1, m.sender, m.text)
	}
	return errors.Join(errs...)
}
