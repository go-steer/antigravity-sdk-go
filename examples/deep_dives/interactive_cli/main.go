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

// Command interactive_cli is a small chat client built on the SDK.
//
// The SDK ships [antigravity.RunInteractive], which is a complete terminal
// session in one call and the right answer most of the time. This example
// takes the loop apart instead, because a real CLI usually wants things the
// one-liner does not offer: streaming output under its own control,
// per-turn telemetry, and a configuration assembled from flags.
//
// It also stands as the full-stack example. One agent here has a custom Go
// tool, an MCP server over stdio, a policy that asks the terminal before every
// tool call, and a hook that routes the agent's own questions to the same
// terminal.
//
// Run from the repository root:
//
//	go run ./examples/deep_dives/interactive_cli -show-usage
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	model := flag.String("model", "", "Gemini model name, or empty for the SDK default")
	system := flag.String("system", "", "system instructions for the agent")
	disableRunCommand := flag.Bool("disable-run-command", false, "remove the run_command tool")
	showUsage := flag.Bool("show-usage", false, "print token usage and the trajectory after each turn")
	flag.Parse()

	opts := options{
		model:             *model,
		system:            *system,
		disableRunCommand: *disableRunCommand,
		showUsage:         *showUsage,
	}
	if err := run(context.Background(), opts); err != nil {
		log.Fatal(err)
	}
}

type options struct {
	model             string
	system            string
	disableRunCommand bool
	showUsage         bool
}

// readFileUpsideDown is the custom tool. Reversing a file is useless, which is
// the point: nothing about it could come from a builtin.
type upsideDownArgs struct {
	Path string `json:"path" jsonschema:"description=Path to the file to read."`
}

func readFileUpsideDown(_ context.Context, args upsideDownArgs) (string, error) {
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return "", err
	}
	lines := strings.SplitAfter(string(data), "\n")
	slices.Reverse(lines)
	return strings.Join(lines, ""), nil
}

func run(ctx context.Context, opts options) error {
	serverPath, cleanup, err := buildMCPServer(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	capabilities := antigravity.CapabilitiesConfig{
		Behavior: antigravity.BehaviorInteractive,
	}
	if opts.disableRunCommand {
		capabilities.DisabledTools = []antigravity.BuiltinTool{antigravity.ToolRunCommand}
	}

	agentOpts := []antigravity.Option{
		antigravity.WithTools(antigravity.MustNewTool("read_file_upside_down",
			"Reads a file and returns its contents with the lines reversed.",
			readFileUpsideDown)),
		antigravity.WithMCPServers(
			antigravity.NewMCPStdioServer("pirate_math", serverPath, "--transport=stdio")),
		// "*" matches every tool, builtin or otherwise, so nothing runs
		// without a yes at the prompt.
		antigravity.WithPolicies(antigravity.One(
			antigravity.AskUser("*", antigravity.ConfirmInTerminal()))),
		// The policy handles calls the agent makes; this handles questions the
		// agent asks.
		antigravity.WithInteractionHook(antigravity.AnswerQuestionsInTerminal()),
		antigravity.WithCapabilities(capabilities),
	}
	if opts.model != "" {
		agentOpts = append(agentOpts, antigravity.WithModel(opts.model))
	}
	if opts.system != "" {
		agentOpts = append(agentOpts, antigravity.WithSystemPrompt(opts.system))
	}

	agent, err := antigravity.New(ctx, agentOpts...)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("\nGoogle Antigravity SDK demo")
	fmt.Println("Type a message and press Enter. Type exit, or press Ctrl-D, to quit.")

	stdin := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\n> ")
		if !stdin.Scan() {
			break
		}
		input := strings.TrimSpace(stdin.Text())
		if input == "" {
			continue
		}
		if strings.EqualFold(input, "exit") || strings.EqualFold(input, "quit") {
			break
		}

		if err := turn(ctx, agent, input, opts.showUsage); err != nil {
			if errors.Is(err, context.Canceled) {
				break
			}
			// One bad turn should not end the session; the user may want to
			// rephrase and try again.
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
	if err := stdin.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	fmt.Println("\nGoodbye.")
	return nil
}

func turn(ctx context.Context, agent *antigravity.Agent, input string, showUsage bool) error {
	resp, err := agent.Chat(ctx, antigravity.Text(input))
	if err != nil {
		return err
	}
	for text, err := range resp.Text() {
		if err != nil {
			return err
		}
		fmt.Print(text)
	}
	fmt.Println()

	if showUsage {
		printTelemetry(resp.Usage(), agent.Conversation().Usage(), agent.Conversation().History())
	}
	return nil
}

func printTelemetry(turn *antigravity.UsageMetadata, total antigravity.UsageMetadata, history []antigravity.Step) {
	fmt.Println("\n--- Turn token usage ---")
	if turn == nil {
		fmt.Println("  No usage data for this turn.")
	} else {
		printUsage(*turn)
	}

	fmt.Println("\n--- Session cumulative usage ---")
	printUsage(total)

	fmt.Printf("\n--- Trajectory (%d steps) ---\n", len(history))
	for i, s := range history {
		line := fmt.Sprintf("    [%d] %s (%s) - %s", i, s.Type, s.Source, s.Status)
		if len(s.ToolCalls) > 0 {
			names := make([]string, len(s.ToolCalls))
			for j, tc := range s.ToolCalls {
				names[j] = tc.Name
			}
			line += " [" + strings.Join(names, ", ") + "]"
		}
		fmt.Println(line)
	}
	fmt.Println()
}

func printUsage(u antigravity.UsageMetadata) {
	fmt.Println("  Prompt tokens:  ", u.PromptTokenCount)
	fmt.Println("  Cached tokens:  ", u.CachedContentTokenCount)
	fmt.Println("  Output tokens:  ", u.CandidatesTokenCount)
	fmt.Println("  Thinking tokens:", u.ThoughtsTokenCount)
	fmt.Println("  Total tokens:   ", u.TotalTokenCount)
}

// buildMCPServer compiles the example MCP server, which the harness launches
// as a subprocess and so needs as a real executable.
func buildMCPServer(ctx context.Context) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pirate_math_")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }

	path = filepath.Join(dir, "pirate-math-mcp")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}

	cmd := exec.CommandContext(ctx, "go", "build", "-o", path,
		"./examples/resources/piratemath/cmd/pirate-math-mcp")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("building the MCP server (run this from the repository root): %w", err)
	}
	return path, cleanup, nil
}
