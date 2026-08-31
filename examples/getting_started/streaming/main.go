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

// Command streaming prints the agent's reasoning and its final answer token by
// token as they arrive, rather than waiting for the turn to finish.
//
//	go run ./examples/getting_started/streaming
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
	agent, err := antigravity.New(ctx)
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "Solve this riddle: I speak without a mouth and hear " +
		"without ears. I have no body, but I come alive with wind. What am " +
		"I? Explain your reasoning."
	fmt.Printf("  User: %s\n\n", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	defer resp.Close()

	fmt.Println("  Agent (streaming thoughts):")
	fmt.Println("  -------------------------------------------------------")
	for thought, err := range resp.Thoughts() {
		if err != nil {
			return err
		}
		fmt.Print(thought)
	}
	fmt.Printf("\n  -------------------------------------------------------\n\n")

	fmt.Println("  Agent (streaming final answer):")
	fmt.Println("  -------------------------------------------------------")
	for token, err := range resp.Text() {
		if err != nil {
			return err
		}
		fmt.Print(token)
	}
	fmt.Printf("\n  -------------------------------------------------------\n\n")

	// resp.ToolCalls() streams tool calls as they arrive, and resp.Chunks()
	// gives the unified raw stream of every content type at once.
	return nil
}
