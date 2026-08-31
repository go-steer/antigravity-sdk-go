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

// Command host_tool_hooks wires one of every lifecycle hook and prints what
// each one receives.
//
// The hooks are deliberately trivial. The value is in seeing the shape of the
// data at each point: what a pre-turn hook can inspect before the model runs,
// what a post-tool-call hook learns about a result, what a tool error looks
// like before the model sees it.
//
// Two things are worth noticing:
//
//   - Subagents arrive as ordinary tool calls named start_subagent, so a
//     single pair of tool hooks observes the whole hierarchy. Filtering on the
//     name is all it takes to treat delegation specially.
//
//   - This example reads steps rather than text. Conversation.Send plus
//     Conversation.Steps exposes each step as it completes, including steps
//     produced inside a subagent, which the aggregated response text folds
//     away. ParentTrajectoryID is what tells the two apart.
//
// Run with:
//
//	go run ./examples/deep_dives/host_tool_hooks
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Hooks — each one logs its payload and gets out of the way
// ---------------------------------------------------------------------------

func onSessionStart(context.Context, *antigravity.HookContext) error {
	fmt.Println("[Hook] Session started.")
	return nil
}

func onSessionEnd(context.Context, *antigravity.HookContext) error {
	fmt.Println("[Hook] Session ended.")
	return nil
}

// onPreTurn sees the prompt before the model does, and could refuse the turn
// by returning TurnDecision{Deny: true}.
func onPreTurn(_ context.Context, _ *antigravity.HookContext, prompt []antigravity.Content) (antigravity.TurnDecision, error) {
	fmt.Printf("[Hook] Pre-turn, prompt: %v\n", prompt)
	return antigravity.TurnDecision{}, nil
}

func onPostTurn(_ context.Context, _ *antigravity.HookContext, response string) error {
	fmt.Printf("[Hook] Post-turn, response: %q\n", response)
	return nil
}

func onPreToolCall(_ context.Context, _ *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
	if call.Name == string(antigravity.ToolStartSubagent) {
		fmt.Printf("[Hook] Pre-subagent, step %s, args: %s\n", call.StepID, call.Args)
	} else {
		fmt.Printf("[Hook] Pre-tool-call, step %s, tool %s, args: %s\n", call.StepID, call.Name, call.Args)
	}
	return antigravity.ToolDecision{}, nil
}

func onPostToolCall(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	if result.Name == string(antigravity.ToolStartSubagent) {
		fmt.Printf("[Hook] Post-subagent, step %s, result: %v\n", result.StepID, result.Result)
	} else {
		fmt.Printf("[Hook] Post-tool-call, step %s, tool %s, result: %v\n", result.StepID, result.Name, result.Result)
	}
	return nil
}

// onToolError could substitute a message for the model to read. Returning ""
// keeps the harness's own formatting, which is what happens here.
func onToolError(_ context.Context, _ *antigravity.HookContext, err *antigravity.ToolError) (string, error) {
	fmt.Printf("[Hook] Tool error, step %s: %v\n", err.StepID, err)
	return "", nil
}

func onCompaction(_ context.Context, _ *antigravity.HookContext, step antigravity.Step) error {
	fmt.Printf("[Hook] Compaction at step %s\n", step.ID)
	return nil
}

// onInteraction answers questions programmatically, picking the first option
// every time. Without a hook like this the agent would block on a human.
func onInteraction(_ context.Context, _ *antigravity.HookContext, req antigravity.QuestionRequest) (*antigravity.QuestionAnswers, error) {
	fmt.Printf("[Hook] Interaction, %d question(s)\n", len(req.Questions))

	answers := make([]antigravity.Answer, 0, len(req.Questions))
	for _, q := range req.Questions {
		fmt.Printf("        %s\n", q.Text)
		if len(q.Options) > 0 {
			answers = append(answers, antigravity.Answer{SelectedOptions: []int{0}})
		} else {
			answers = append(answers, antigravity.Answer{Text: "auto-response"})
		}
	}
	return &antigravity.QuestionAnswers{Answers: answers}, nil
}

// ---------------------------------------------------------------------------
// Tools, present only to make the tool hooks fire
// ---------------------------------------------------------------------------

type greetArgs struct {
	Name string `json:"name" jsonschema:"description=The name to greet."`
}

func greet(_ context.Context, args greetArgs) (string, error) {
	return "Hello, " + args.Name + "!", nil
}

func brokenTool(context.Context, struct{}) (string, error) {
	return "", errors.New("this tool is intentionally broken")
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func run(ctx context.Context) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	agent, err := antigravity.New(ctx,
		antigravity.WithTools(
			antigravity.MustNewTool("greet", "Returns a greeting for the given name.", greet),
			antigravity.MustNewTool("broken_tool", "A tool that always fails.", brokenTool),
		),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			// Interactive behavior lets the agent ask questions, and subagents
			// give the delegation hooks something to observe.
			Behavior:        antigravity.BehaviorInteractive,
			EnableSubagents: true,
		}),
		antigravity.WithSessionStartHook(onSessionStart),
		antigravity.WithSessionEndHook(onSessionEnd),
		antigravity.WithPreTurnHook(onPreTurn),
		antigravity.WithPostTurnHook(onPostTurn),
		antigravity.WithPreToolCallHook(onPreToolCall),
		antigravity.WithPostToolCallHook(onPostToolCall),
		antigravity.WithToolErrorHook(onToolError),
		antigravity.WithCompactionHook(onCompaction),
		antigravity.WithInteractionHook(onInteraction),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	prompts := []string{
		"Please greet Alice using the greet tool.",
		"Please call the broken_tool tool.",
		"Ask me a multiple-choice trivia question.",
		"Invoke a subagent to write a short poem about nature.",
	}
	for _, prompt := range prompts {
		if err := runPrompt(ctx, agent, prompt); err != nil {
			return err
		}
	}

	fmt.Println("\n--- All prompts complete ---")
	return nil
}

// runPrompt sends one prompt and prints every completed response step,
// labelling subagent output separately from the agent's own.
func runPrompt(ctx context.Context, agent *antigravity.Agent, prompt string) error {
	fmt.Printf("\n%s\n--- Sending: %q ---\n%s\n", strings.Repeat("=", 60), prompt, strings.Repeat("=", 60))

	conv := agent.Conversation()
	if err := conv.Send(ctx, antigravity.Text(prompt)); err != nil {
		return err
	}

	for step, err := range conv.Steps(ctx) {
		if err != nil {
			return err
		}
		if !step.IsCompleteResponse {
			continue
		}
		label := "Final response"
		if step.ParentTrajectoryID != "" {
			label = fmt.Sprintf("Subagent response (depth %d)", step.Depth)
		}
		fmt.Printf("\n--- %s ---\n%s\n", label, step.Content)
	}
	return nil
}
