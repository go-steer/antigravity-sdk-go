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

// Command hooks registers every lifecycle hook the SDK offers and narrates
// what fires when.
//
// Hooks are how a program observes and steers a session without owning the
// agent loop. Each one is a plain function passed to a With… option; there are
// no decorators and no registry. Every hook receives a *HookContext, a
// per-session scratchpad that lets one hook leave state for another.
//
//	go run ./examples/getting_started/hooks
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

// ---------------------------------------------------------------------------
// Session hooks
// ---------------------------------------------------------------------------

func onSessionStart(_ context.Context, _ *antigravity.HookContext) error {
	fmt.Println("\n  [hook] Session started")
	return nil
}

func onSessionEnd(_ context.Context, _ *antigravity.HookContext) error {
	fmt.Println("\n  [hook] Session ended")
	return nil
}

// ---------------------------------------------------------------------------
// Turn hooks
// ---------------------------------------------------------------------------

// preTurn can refuse a prompt outright. The zero TurnDecision allows it, so an
// observer-only hook returns that unchanged.
func preTurn(_ context.Context, hc *antigravity.HookContext, prompt []antigravity.Content) (antigravity.TurnDecision, error) {
	fmt.Printf("\n  [hook] Pre-turn: intercepted prompt -> %v\n", prompt)

	// HookContext survives across hooks and across turns, which makes it the
	// natural place for a counter like this.
	n := hc.Update("turns", func(current any) any {
		count, _ := current.(int)
		return count + 1
	})
	fmt.Printf("  [hook] Pre-turn: this is turn %v\n", n)

	return antigravity.TurnDecision{}, nil
}

func postTurn(_ context.Context, _ *antigravity.HookContext, response string) error {
	fmt.Printf("\n  [hook] Post-turn: final response -> %q\n", head(response, 80))
	return nil
}

// ---------------------------------------------------------------------------
// Tool hooks
// ---------------------------------------------------------------------------

// preToolCall gates every tool the agent invokes: builtin, custom, and MCP. It
// can deny the call, or shallow-merge replacement arguments over the model's.
func preToolCall(_ context.Context, _ *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
	fmt.Printf("\n  [hook] Pre-tool-call: approving %s (id=%q)\n", call.Name, call.ID)
	return antigravity.ToolDecision{}, nil
}

func postToolCall(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	fmt.Printf("\n  [hook] Post-tool-call: %s returned %v (id=%q, err=%v)\n",
		result.Name, result.Result, result.ID, result.Err)
	return nil
}

// onToolError sees failures from custom tools. Returning a non-empty string
// substitutes that text as the tool's result and lets the agent carry on;
// returning "" lets the error reach the model as a failure.
func onToolError(_ context.Context, _ *antigravity.HookContext, err *antigravity.ToolError) (string, error) {
	fmt.Printf("\n  [hook] Tool error: %v (tool=%s, id=%s)\n", err, err.ToolName, err.CallID)
	return "", nil
}

// ---------------------------------------------------------------------------
// Interaction and compaction hooks
// ---------------------------------------------------------------------------

// onInteraction answers the agent's questions without a human. Picking the
// first option is a stand-in; AnswerQuestionsInTerminal() asks for real.
func onInteraction(_ context.Context, _ *antigravity.HookContext, req antigravity.QuestionRequest) (*antigravity.QuestionAnswers, error) {
	fmt.Printf("\n  [hook] Interaction requested: %d question(s)\n", len(req.Questions))

	answers := make([]antigravity.Answer, 0, len(req.Questions))
	for _, q := range req.Questions {
		fmt.Printf("  [hook]   %s\n", q.Text)
		if len(q.Options) > 0 {
			answers = append(answers, antigravity.Answer{SelectedOptions: []int{0}})
			continue
		}
		answers = append(answers, antigravity.Answer{Text: "Auto-response"})
	}
	return &antigravity.QuestionAnswers{Answers: answers}, nil
}

// onCompaction fires when the harness compacts the context window, which is
// the moment earlier history stops being verbatim.
func onCompaction(_ context.Context, _ *antigravity.HookContext, step antigravity.Step) error {
	fmt.Printf("\n  [hook] Context compaction occurred at step %s\n", step.ID)
	return nil
}

// ---------------------------------------------------------------------------
// Stop hook
// ---------------------------------------------------------------------------

// onStop gets the last word on whether a turn is really over, and can push the
// agent to keep working. ContinuationCount guards against looping forever.
func onStop(_ context.Context, _ *antigravity.HookContext, args antigravity.StopArgs) (antigravity.StopDecision, error) {
	fmt.Printf("\n  [stop hook] Fired (continuationCount=%d, stopReason=%s)\n",
		args.ContinuationCount, args.StopReason)
	fmt.Printf("  [stop hook] Response preview: %q...\n", head(args.Response, 120))

	if strings.Contains(strings.ToLower(args.Response), "mars") && args.ContinuationCount == 0 {
		fmt.Println("  [stop hook] -> continue: pushing the agent to dig deeper.")
		return antigravity.StopDecision{
			Continue: true,
			Reason: "Great start. Now pick the single most surprising fact you " +
				"mentioned and explain why it matters for future space " +
				"exploration. Be concise.",
		}, nil
	}

	fmt.Println("  [stop hook] -> allow stop: the agent may finish.")
	return antigravity.StopDecision{}, nil
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

type greetArgs struct {
	Name string `json:"name" jsonschema:"description=The name of the person to greet."`
}

func greet(_ context.Context, args greetArgs) (string, error) {
	return "Hello, " + args.Name + "!", nil
}

func brokenTool(_ context.Context, _ struct{}) (string, error) {
	return "", errors.New("this tool is intentionally broken")
}

// ---------------------------------------------------------------------------

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithSessionStartHook(onSessionStart),
		antigravity.WithSessionEndHook(onSessionEnd),
		antigravity.WithPreTurnHook(preTurn),
		antigravity.WithPostTurnHook(postTurn),
		antigravity.WithPreToolCallHook(preToolCall),
		antigravity.WithPostToolCallHook(postToolCall),
		antigravity.WithToolErrorHook(onToolError),
		antigravity.WithInteractionHook(onInteraction),
		antigravity.WithCompactionHook(onCompaction),
		antigravity.WithStopHook(onStop),
		antigravity.WithTools(
			antigravity.MustNewTool("greet", "Greets a person by name.", greet),
			antigravity.MustNewTool("broken_tool", "Always fails.", brokenTool),
		),
		// The interaction hook only has something to do when the agent is
		// allowed to ask, which is an interactive-behavior capability.
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			Behavior: antigravity.BehaviorInteractive,
		}),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("  --- Starting interaction ---")
	for _, step := range []struct{ label, prompt string }{
		{"Simple chat (turn hooks)", "Say 'Hello World!'"},
		{"Tool usage (tool hooks)", "Please greet Alice using the greet tool."},
		{"Tool error (error hook)", "Please call the broken_tool tool."},
		{"Interaction (interaction hook)", "Ask me a multiple-choice trivia question."},
		{"Stop hook (dig deeper)", "Tell me 3 interesting facts about Mars."},
	} {
		fmt.Printf("\n  --- %s ---\n", step.label)
		resp, err := agent.Chat(ctx, antigravity.Text(step.prompt))
		if err != nil {
			return err
		}
		fmt.Print("  Agent response: ")
		for token, err := range resp.Text() {
			if err != nil {
				return err
			}
			fmt.Print(token)
		}
		fmt.Println()
	}

	fmt.Println("\n  --- Finished interaction ---")
	return nil
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
