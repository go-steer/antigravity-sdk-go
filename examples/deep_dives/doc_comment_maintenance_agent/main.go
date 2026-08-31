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

// Command doc_comment_maintenance_agent audits Go doc comments and fills in
// the gaps.
//
// It is the sibling of examples/deep_dives/doc_maintenance_agent: same shape,
// tighter constraints. Editing source is riskier than editing prose, so this
// one is fenced twice over. Capabilities remove the tools that could do
// something other than editing — no create_file, no run_command, no subagents
// — and the policies then permit edit_file only for a .go file inside the
// target directory. Removing a tool and denying it are different mechanisms
// worth knowing apart: a disabled tool is absent from the model's context,
// while a denied one is visible and refused at call time.
//
//	go run ./examples/deep_dives/doc_comment_maintenance_agent -dir ./internal
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	dir := flag.String("dir", ".", "directory whose doc comments to maintain")
	prompt := flag.String("prompt",
		"Audit all Go files in the target directory and make sure every "+
			"exported symbol has a doc comment. Add or improve comments as needed.",
		"instruction to send to the agent")
	flag.Parse()

	if err := run(context.Background(), *dir, *prompt); err != nil {
		log.Fatal(err)
	}
}

var friendlyToolNames = map[string]string{
	string(antigravity.ToolViewFile):  "Viewing file",
	string(antigravity.ToolListDir):   "Listing directory",
	string(antigravity.ToolSearchDir): "Searching directory",
	string(antigravity.ToolFindFile):  "Finding files",
	string(antigravity.ToolEditFile):  "Editing file",
}

func announce(_ context.Context, _ *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
	name, ok := friendlyToolNames[call.Name]
	if !ok {
		name = call.Name
	}
	if call.CanonicalPath != "" {
		fmt.Printf("%s: %s\n", name, call.CanonicalPath)
	} else {
		fmt.Printf("%s with arguments: %s\n", name, call.Args)
	}
	return antigravity.ToolDecision{}, nil
}

func run(ctx context.Context, dir, prompt string) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	target, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fmt.Println("Target directory:", target)

	isGoFileInTarget := func(_ context.Context, call antigravity.ToolCall) (bool, error) {
		path := call.CanonicalPath
		if path == "" {
			return false, nil
		}
		return filepath.Ext(path) == ".go" && underDir(path, target), nil
	}

	instructions := "You are a technical writer maintaining the doc comments " +
		"of a Go codebase.\n\n" +
		"Guidelines:\n" +
		"1. Focus: audit every Go file in the target directory and find " +
		"exported symbols with a missing or inadequate doc comment.\n" +
		"2. Style: follow Go convention. A doc comment is a complete sentence " +
		"beginning with the name of the symbol it documents, it explains why " +
		"rather than restating the signature, and it lives directly above the " +
		"declaration with no blank line between.\n" +
		"3. Safety: you may only add or improve comments. Do not change " +
		"implementation code, control flow, signatures, or declarations. Every " +
		"edit must be confined to comment text.\n" +
		"4. Action: apply fixes directly to .go files under " + target + ". " +
		"You may not edit anything else."

	fmt.Println("Creating the doc comment maintenance agent...")

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt(instructions),
		antigravity.WithWorkspaces(target),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			// Whatever the model decides to do, it has nothing here to do it
			// with except reading and editing.
			DisabledTools: []antigravity.BuiltinTool{
				antigravity.ToolCreateFile,
				antigravity.ToolRunCommand,
				antigravity.ToolAskQuestion,
				antigravity.ToolStartSubagent,
				antigravity.ToolGenerateImage,
				antigravity.ToolFinish,
			},
		}),
		antigravity.WithPolicies(antigravity.One(
			antigravity.AllowTool(antigravity.ToolViewFile),
			antigravity.AllowTool(antigravity.ToolListDir),
			antigravity.AllowTool(antigravity.ToolSearchDir),
			antigravity.AllowTool(antigravity.ToolFindFile),
			antigravity.AllowTool(antigravity.ToolEditFile,
				antigravity.When(isGoFileInTarget),
				antigravity.Named("allow-edit-go-only-in-target")),
			antigravity.DenyAll(),
		)),
		antigravity.WithPreToolCallHook(announce),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("\nStreaming agent output:")
	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
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
	return nil
}

// underDir reports whether path is dir or something inside it.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
