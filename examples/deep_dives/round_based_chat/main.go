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

// Command round_based_chat runs a panel discussion in synchronized rounds.
//
// Three agents answer at once each round, and the round ends when the slowest
// of them finishes. Everyone therefore sees the same transcript before
// speaking again, which keeps the debate coherent at the cost of waiting on
// the slow agent. Compare examples/deep_dives/async_chat, which drops the
// synchronization entirely.
//
// Two other mechanisms carry the example. Each agent has a pass_turn tool, so
// silence is something the model can choose rather than something the harness
// has to detect; when every agent passes, the discussion is over. And each has
// a timer trigger that nudges it to wrap up, which shows a trigger firing
// alongside a normal chat loop rather than driving it.
//
//	go run ./examples/deep_dives/round_based_chat
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

const (
	passToken      = "[PASS]"
	maxRounds      = 4
	nudgeableAfter = time.Minute
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// passTurn gives the model a way to say nothing.
func passTurn(context.Context, struct{}) (string, error) { return passToken, nil }

// moderatorNudge is the trigger body. Send delivers the message as an external
// notification rather than a user prompt, so it does not disturb the round
// structure the chat loop maintains.
func moderatorNudge(ctx context.Context, tc *antigravity.TriggerContext) error {
	return tc.Send(ctx, "The discussion is wrapping up. Make your final point concisely.")
}

// ---------------------------------------------------------------------------
// The room
// ---------------------------------------------------------------------------

type message struct {
	sender string
	text   string
}

type participant struct {
	name  string
	agent *antigravity.Agent
	// seen is how much of the transcript this agent has been shown. Each
	// round's goroutine touches only its own participant, so no lock is needed.
	seen int
}

type room struct {
	participants []*participant
	history      []message
}

func (r *room) discuss(ctx context.Context, topic string) error {
	bar := strings.Repeat("=", 60)
	fmt.Printf("\n%s\nTopic: %s\n%s\n", bar, topic, bar)

	r.history = append(r.history, message{"User", topic})

	for round := 0; round < maxRounds; round++ {
		spoke, err := r.playRound(ctx)
		if err != nil {
			return err
		}
		if len(spoke) == 0 {
			fmt.Println("\n  Everyone passed; the discussion is complete.")
			return nil
		}
		r.history = append(r.history, spoke...)
	}

	fmt.Printf("\n  Stopped after %d rounds.\n", maxRounds)
	return nil
}

// playRound asks every agent at once and returns the substantive replies.
func (r *room) playRound(ctx context.Context) ([]message, error) {
	replies := make([]message, len(r.participants))
	errs := make([]error, len(r.participants))

	var wg sync.WaitGroup
	for i, p := range r.participants {
		// Snapshot what this agent has not seen before the goroutines start,
		// so every one of them works from the same transcript.
		var unseen []message
		for _, m := range r.history[p.seen:] {
			if m.sender != p.name {
				unseen = append(unseen, m)
			}
		}
		p.seen = len(r.history)
		if len(unseen) == 0 {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := p.agent.Chat(ctx, antigravity.Text(prompt(unseen)))
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", p.name, err)
				return
			}
			text, err := resp.Wait()
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", p.name, err)
				return
			}
			replies[i] = message{p.name, strings.TrimSpace(text)}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	var spoke []message
	for i, m := range replies {
		if m.sender == "" {
			continue
		}
		if m.text == "" || strings.Contains(m.text, passToken) {
			fmt.Printf("\n  %s: (pass)\n", r.participants[i].name)
			continue
		}
		fmt.Printf("\n  %s: %s\n", m.sender, m.text)
		spoke = append(spoke, m)
	}
	return spoke, nil
}

// prompt formats only the messages this agent has yet to see. The agents are
// stateful, so replaying the whole transcript would duplicate their context.
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
	"Rational Rita",
	"You are Rational Rita, a research specialist in a group chat with " +
		"Creative Cal (an imaginative thinker) and Skeptical Sam (a devil's " +
		"advocate). Give concise, factual answers grounded in evidence.\n\n" +
		"- Refer to Cal and Sam by name when responding to their points.\n" +
		"- Correct inaccuracies from other agents.\n" +
		"- If the topic is purely creative or a matter of opinion, call pass_turn().\n" +
		"- Keep responses under 3 sentences.",
}, {
	"Creative Cal",
	"You are Creative Cal, a creative thinker in a group chat with Rational " +
		"Rita (a fact-driven researcher) and Skeptical Sam (a devil's " +
		"advocate). Offer imaginative perspectives and metaphors.\n\n" +
		"- Refer to Rita and Sam by name when building on their points.\n" +
		"- Only respond when you have a genuinely fresh angle.\n" +
		"- If the discussion is purely factual, call pass_turn().\n" +
		"- Keep responses under 3 sentences.",
}, {
	"Skeptical Sam",
	"You are Skeptical Sam, a devil's advocate in a group chat with Rational " +
		"Rita (a researcher) and Creative Cal (a creative thinker). Challenge " +
		"assumptions and poke holes.\n\n" +
		"- Refer to Rita and Cal by name when questioning their claims.\n" +
		"- If everyone is being balanced, call pass_turn().\n" +
		"- Be constructive, not contrarian for its own sake.\n" +
		"- Keep responses under 3 sentences.",
}}

func run(ctx context.Context) error {
	fmt.Println("Agent chat room")

	pass := antigravity.MustNewTool("pass_turn",
		"Decline to respond this round. Call this when the topic is outside "+
			"your expertise, you agree with what has been said, or your input "+
			"would be redundant.", passTurn)

	r := &room{}
	for _, p := range personas {
		agent, err := antigravity.New(ctx,
			antigravity.WithSystemPrompt(p.instructions),
			antigravity.WithTools(pass),
			antigravity.WithNamedTrigger("moderator",
				antigravity.Every(nudgeableAfter, moderatorNudge)),
		)
		if err != nil {
			return err
		}
		defer agent.Close()

		r.participants = append(r.participants, &participant{name: p.name, agent: agent})
	}

	topics := []string{
		"Should we colonize Mars, or focus on fixing Earth first?",
		"What is the most overrated programming language?",
	}
	for _, topic := range topics {
		if err := r.discuss(ctx, topic); err != nil {
			return err
		}
	}

	bar := strings.Repeat("=", 60)
	fmt.Printf("\n%s\nTranscript (%d turns)\n%s\n", bar, len(r.history), bar)
	for i, m := range r.history {
		fmt.Printf("  %d. [%s]: %s\n", i+1, m.sender, m.text)
	}
	return nil
}
