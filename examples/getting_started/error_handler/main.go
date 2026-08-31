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

// Command error_handler shows the two layers of error handling worth knowing:
// a hook that turns a tool failure into something the model can act on, and
// errors.Is/errors.As on the sentinel errors the SDK returns to your code.
//
// The two are for different audiences. The hook writes for the agent, so the
// turn can recover. The switch at the bottom writes for you, so the program
// can.
//
//	go run ./examples/getting_started/error_handler
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

type explodingArgs struct {
	InputData string `json:"input_data" jsonschema:"description=Any string input."`
}

// explodingTool always fails, which makes the error path deterministic.
func explodingTool(_ context.Context, args explodingArgs) (string, error) {
	fmt.Printf("\n  [tool] exploding_tool called with %q, exploding...\n", args.InputData)
	return "", errors.New("this tool is intentionally broken and always fails")
}

// handleToolError intercepts a failed tool call. Returning a non-empty string
// replaces the error with that text as the tool's result, which is how you
// coach the model through a failure instead of just reporting one. Returning ""
// lets the default handling stand.
func handleToolError(_ context.Context, _ *antigravity.HookContext, err *antigravity.ToolError) (string, error) {
	fmt.Printf("\n  [error handler] Caught: %v\n", err)

	if err.ToolName == "exploding_tool" {
		return fmt.Sprintf("[Tool Error: %v. Please inform the user that the "+
			"operation failed.]", err.Err), nil
	}
	return "", nil
}

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// Warning level surfaces policy denials and capability problems without the
	// full protocol trace.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	fmt.Print("  Error handling example\n\n")

	agent, err := antigravity.New(ctx,
		antigravity.WithLogger(logger),
		antigravity.WithTools(antigravity.MustNewTool("exploding_tool",
			"A tool that always fails, regardless of input.", explodingTool)),
		antigravity.WithToolErrorHook(handleToolError),
	)
	if err != nil {
		// Configuration problems are rejected here, before anything starts.
		return describe(err)
	}
	defer agent.Close()

	const prompt = "Use the exploding_tool with input 'test data'."
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return describe(err)
	}
	text, err := resp.Wait()
	if err != nil {
		return describe(err)
	}
	fmt.Println("  Agent:", text)

	return nil
}

// describe maps an SDK error onto an explanation, and reports it as handled.
//
// The sentinels are matched with errors.Is because the concrete types wrap
// them: a *HarnessError is an ErrConnection, a *ConfigError is an
// ErrInvalidConfig. errors.As gets at the details when you need them.
func describe(err error) error {
	switch {
	case errors.Is(err, antigravity.ErrInvalidConfig):
		var cfgErr *antigravity.ConfigError
		if errors.As(err, &cfgErr) {
			fmt.Printf("\n  [app error] Bad configuration for %q: %v\n", cfgErr.Field, cfgErr.Err)
			return nil
		}
		fmt.Printf("\n  [app error] Bad configuration: %v\n", err)

	case errors.Is(err, antigravity.ErrHarnessNotFound):
		// The most common first-run failure: the localharness binary is not on
		// PATH and ANTIGRAVITY_HARNESS_PATH is unset.
		fmt.Printf("\n  [app error] Harness binary not found: %v\n", err)

	case errors.Is(err, antigravity.ErrConnection):
		// The harness crashed or the WebSocket dropped. A *HarnessError carries
		// the subprocess's stderr, which is usually the only real diagnostic.
		fmt.Printf("\n  [app error] Connection failed: %v\n", err)

	case errors.Is(err, antigravity.ErrInvalidPrompt):
		fmt.Printf("\n  [app error] Nothing to send: %v\n", err)

	case errors.Is(err, antigravity.ErrCancelled):
		fmt.Printf("\n  [app error] The turn was cancelled: %v\n", err)

	case errors.Is(err, antigravity.ErrExecution):
		// The agent loop hit something fatal and cannot continue.
		fmt.Printf("\n  [app error] Agent execution failed: %v\n", err)

	default:
		fmt.Printf("\n  [app error] Unexpected error: %v\n", err)
	}
	return nil
}
