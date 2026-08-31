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

// Command human_in_the_loop lets the agent stop and ask the operator a
// question mid-turn.
//
// Two things have to line up. The agent must be in interactive behavior, which
// is what puts the ask_question tool in its context, and the program must
// register an interaction hook to answer with. Without the hook the agent has
// no one to ask, and it will guess instead.
//
// AnswerQuestionsInTerminal reads from stdin, so this example expects a real
// terminal.
//
//	go run ./examples/getting_started/human_in_the_loop
package main

import (
	"context"
	"fmt"
	"log"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt(
			"When you need clarification or more information from the user to "+
				"fulfill a request, you should use the `ask_question` tool to "+
				"prompt them."),
		antigravity.WithInteractionHook(antigravity.AnswerQuestionsInTerminal()),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			Behavior: antigravity.BehaviorInteractive,
		}),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	// Deliberately underspecified, to give the agent something to ask about.
	const prompt = "I want to search for a file."
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	defer resp.Close()

	// Streaming rather than waiting matters here: the question is asked while
	// the turn is still running, and the answer shapes the rest of it.
	for token, err := range resp.Text() {
		if err != nil {
			return err
		}
		fmt.Print(token)
	}
	fmt.Println()

	return nil
}
