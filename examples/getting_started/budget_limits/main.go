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

// Command budget_limits exercises all five session budget dials and shows the
// stop reason each one produces when it runs out.
//
// A budget caps a whole session, not a turn. Exhausting one does not raise an
// error: the turn ends early and reports why through StopReason, which leaves
// the caller free to decide whether that is a failure.
//
//	go run ./examples/getting_started/budget_limits
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	ctx := context.Background()

	fmt.Println("Running the Antigravity SDK budget enforcement demos...")
	demos := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"max model calls (MaxModelCalls=1)", demoMaxModelCalls},
		{"max tool calls (MaxToolCalls=1)", demoMaxToolCalls},
		{"max input tokens (MaxInputTokens=50)", demoMaxInputTokens},
		{"max output tokens (MaxOutputTokens=30)", demoMaxOutputTokens},
		{"max total tokens (MaxTotalTokens=100)", demoMaxTotalTokens},
	}
	for i, demo := range demos {
		fmt.Printf("\n%s\n%d. Testing %s\n%[1]s\n", strings.Repeat("=", 60), i+1, demo.name)
		if err := demo.fn(ctx); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Printf("\n%s\nAll 5 budget dials exercised.\n%[1]s\n", strings.Repeat("=", 60))
}

// demoMaxModelCalls halts once the session has made its one allowed model call.
func demoMaxModelCalls(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithBudget(antigravity.BudgetConfig{MaxModelCalls: 1}))
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("Turn 1: asking a question (consumes the one allowed model call)...")
	text, reason, err := turn(ctx, agent, "What is 2 + 2? Reply with just the number.")
	if err != nil {
		return err
	}
	fmt.Printf("  Agent response: %s\n", strings.TrimSpace(text))
	fmt.Println("  Turn 1 stop reason:", reason)
	if reason == antigravity.StopMaxModelCalls {
		fmt.Println("  [Limit reached] The session model call budget ran out during turn 1.")
	}

	fmt.Println("\nTurn 2: asking again with the budget already exhausted...")
	_, reason, err = turn(ctx, agent, "What is 3 + 3?")
	if err != nil {
		return err
	}
	fmt.Println("  Turn 2 stop reason:", reason)
	if reason == antigravity.StopMaxModelCalls {
		fmt.Println("  [Halted] Turn 2 was prevented from making a model call at all.")
	}
	return nil
}

type cityArgs struct {
	City string `json:"city" jsonschema:"description=Name of the city."`
}

func lookupWeather(_ context.Context, args cityArgs) (string, error) {
	return fmt.Sprintf("Sunny and 24C in %s", args.City), nil
}

func lookupTimezone(_ context.Context, args cityArgs) (string, error) {
	return fmt.Sprintf("UTC+9 for %s", args.City), nil
}

// demoMaxToolCalls halts partway through a prompt that needs two tool calls.
func demoMaxToolCalls(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithTools(
			antigravity.MustNewTool("lookup_weather",
				"Looks up the current weather for a given city.", lookupWeather),
			antigravity.MustNewTool("lookup_timezone",
				"Looks up the time zone for a given city.", lookupTimezone),
		),
		antigravity.WithBudget(antigravity.BudgetConfig{MaxToolCalls: 1}))
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "First call lookup_weather for 'Tokyo', and then call lookup_timezone for 'Tokyo'."
	fmt.Println("Sending a multi-tool prompt:", prompt)
	_, reason, err := turn(ctx, agent, prompt)
	if err != nil {
		return err
	}
	fmt.Println("  Stop reason:", reason)
	if reason == antigravity.StopMaxToolCalls {
		fmt.Println("  [Halted] The second tool execution was stopped by the budget.")
	}
	return nil
}

// demoMaxInputTokens halts before inference, since the input cap is checked
// against the prompt rather than against what the prompt produces.
func demoMaxInputTokens(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithBudget(antigravity.BudgetConfig{MaxInputTokens: 50}))
	if err != nil {
		return err
	}
	defer agent.Close()

	prompt := "Summarize the following passage:\n" +
		strings.Repeat("The quick brown fox jumps over the lazy dog. ", 30)
	fmt.Println("Sending a large prompt, well past 50 input tokens...")
	_, reason, err := turn(ctx, agent, prompt)
	if err != nil {
		return err
	}
	fmt.Println("  Stop reason:", reason)
	if reason == antigravity.StopMaxInputTokens {
		fmt.Println("  [Proactively halted] The input budget was exceeded before inference.")
	}
	return nil
}

// demoMaxOutputTokens lets turn 1 overshoot, then blocks turn 2. Output caps
// are cumulative and checked after generation, so the turn that crosses the
// line still produces its answer.
func demoMaxOutputTokens(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithBudget(antigravity.BudgetConfig{MaxOutputTokens: 30}))
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("Turn 1: requesting a response longer than 30 output tokens...")
	text, reason, err := turn(ctx, agent, "Write a detailed paragraph explaining photosynthesis.")
	if err != nil {
		return err
	}
	fmt.Printf("  Turn 1 response: %s...\n", head(text, 60))
	fmt.Println("  Turn 1 stop reason:", reason)

	fmt.Println("\nTurn 2: continuing with the output budget spent...")
	_, reason, err = turn(ctx, agent, "Continue.")
	if err != nil {
		return err
	}
	fmt.Println("  Turn 2 stop reason:", reason)
	if reason == antigravity.StopMaxOutputTokens {
		fmt.Println("  [Halted] Cumulative output exceeded the output token limit.")
	}
	return nil
}

// demoMaxTotalTokens caps net uncached input plus output across the session.
func demoMaxTotalTokens(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithBudget(antigravity.BudgetConfig{MaxTotalTokens: 100}))
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("Turn 1: sending a prompt that will consume more than 100 total tokens...")
	text, reason, err := turn(ctx, agent, "Explain the theory of general relativity in 3 sentences.")
	if err != nil {
		return err
	}
	fmt.Printf("  Turn 1 response: %s...\n", head(text, 60))
	fmt.Println("  Turn 1 stop reason:", reason)

	fmt.Println("\nTurn 2: continuing with the total budget spent...")
	_, reason, err = turn(ctx, agent, "Tell me more.")
	if err != nil {
		return err
	}
	fmt.Println("  Turn 2 stop reason:", reason)
	if reason == antigravity.StopMaxTotalTokens {
		fmt.Println("  [Halted] Cumulative net token consumption exceeded the total limit.")
	}
	return nil
}

// turn runs one prompt to completion and reports the text and stop reason. The
// stream has to be drained before the stop reason is final.
func turn(ctx context.Context, agent *antigravity.Agent, prompt string) (string, antigravity.StopReason, error) {
	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return "", "", err
	}
	text, err := resp.Wait()
	if err != nil {
		return "", "", err
	}
	return text, resp.StopReason(), nil
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
