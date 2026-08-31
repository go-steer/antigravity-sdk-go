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

// Command observability turns on SDK logging, audits tool calls with a hook,
// and reports token usage at the end of the session.
//
// These are the three things worth wiring up before running an agent anywhere
// you cannot watch it: structured logs for what the SDK did, a post-tool hook
// for what the agent did, and usage counts for what it cost.
//
// For OpenTelemetry spans instead of prose, see the tracing module and
// examples/deep_dives/observability_otel.
//
//	go run ./examples/getting_started/observability
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

type weatherArgs struct {
	Location string `json:"location" jsonschema:"description=The city to get the weather for."`
}

func getWeather(_ context.Context, args weatherArgs) (string, error) {
	return fmt.Sprintf("The weather in %s is sunny.", args.Location), nil
}

// auditToolCall records every tool execution, successful or not. Post-tool is
// the right place for an audit trail: pre-tool sees intentions, this sees
// outcomes.
func auditToolCall(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	if result.Err != nil {
		fmt.Printf("\n  [audit] %s failed: %v\n", result.Name, result.Err)
		return nil
	}
	fmt.Printf("\n  [audit] %s completed. Result: %v\n", result.Name, result.Result)
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// The SDK logs through log/slog. Passing a debug-level logger surfaces the
	// protocol traffic; the default discards everything.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	agent, err := antigravity.New(ctx,
		antigravity.WithLogger(logger),
		antigravity.WithTools(antigravity.MustNewTool("get_weather",
			"Gets the weather for a location.", getWeather)),
		antigravity.WithPostToolCallHook(auditToolCall),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "What is the weather in Seattle?"
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}

	fmt.Print("  Agent: ")
	for token, err := range resp.Text() {
		if err != nil {
			return err
		}
		fmt.Print(token)
	}
	fmt.Println()

	// Conversation usage is cumulative across turns; resp.Usage() is the tally
	// for this turn alone.
	usage := agent.Conversation().Usage()
	fmt.Println("\n  --- Token usage ---")
	fmt.Println("  Prompt tokens:  ", usage.PromptTokenCount)
	fmt.Println("  Cached tokens:  ", usage.CachedContentTokenCount)
	fmt.Println("  Output tokens:  ", usage.CandidatesTokenCount)
	fmt.Println("  Thinking tokens:", usage.ThoughtsTokenCount)
	fmt.Println("  Total tokens:   ", usage.TotalTokenCount)

	return nil
}
