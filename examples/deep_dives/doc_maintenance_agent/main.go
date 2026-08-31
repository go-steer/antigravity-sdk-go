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

// Command doc_maintenance_agent keeps Markdown documentation in step with the
// code it describes.
//
// This is a real, if small, autonomous agent: it reads a directory, compares
// the prose against the source, and rewrites the prose. What makes that safe
// to leave running is the policy set, not the prompt. The agent may read
// anything in the workspace, but edit_file is approved only for a .md file
// inside the target directory, and everything else is denied. A prompt can be
// argued with; a deny-by-default policy cannot.
//
//	go run ./examples/deep_dives/doc_maintenance_agent -dir ./docs
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
	dir := flag.String("dir", ".", "directory whose documentation to maintain")
	prompt := flag.String("prompt",
		"Check all documentation in the target directory and ensure it matches "+
			"the code. Fix any discrepancies you find.",
		"instruction to send to the agent")
	flag.Parse()

	if err := run(context.Background(), *dir, *prompt); err != nil {
		log.Fatal(err)
	}
}

// friendlyToolNames turn the wire names into something readable in a progress
// log.
var friendlyToolNames = map[string]string{
	string(antigravity.ToolViewFile):  "Viewing file",
	string(antigravity.ToolListDir):   "Listing directory",
	string(antigravity.ToolSearchDir): "Searching directory",
	string(antigravity.ToolFindFile):  "Finding files",
	string(antigravity.ToolEditFile):  "Editing file",
}

// announce narrates each call. It approves everything; the policies decide.
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

	// isMarkdownInTarget is the whole safety story. CanonicalPath is the
	// normalized absolute path the connection layer resolved for the call, so
	// the predicate never has to parse a file:// URI or a relative path itself.
	isMarkdownInTarget := func(_ context.Context, call antigravity.ToolCall) (bool, error) {
		path := call.CanonicalPath
		if path == "" {
			return false, nil
		}
		return strings.EqualFold(filepath.Ext(path), ".md") && underDir(path, target), nil
	}

	instructions := "You are an expert technical writer maintaining the " +
		"documentation for a software project, written for external " +
		"developers.\n\n" +
		"Guidelines:\n" +
		"1. Audience: assume the reader knows nothing about this project's " +
		"internals. Use clear, professional, accessible language, and avoid " +
		"jargon.\n" +
		"2. Coverage: prioritize the public API surface. Every exported " +
		"symbol should be reachable from the documentation.\n" +
		"3. Examples: every code snippet must be complete, copy-pasteable, " +
		"and checked against the real API. No 'You are a helpful assistant' " +
		"placeholder prompts.\n" +
		"4. Verification: cross-reference snippets against the source and " +
		"the tests before you claim they work.\n" +
		"5. Action: read the source, then correct the Markdown. You may only " +
		"edit .md files under " + target + "."

	fmt.Println("Creating the documentation maintenance agent...")

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt(instructions),
		antigravity.WithWorkspaces(target),
		antigravity.WithPolicies(antigravity.One(
			antigravity.AllowTool(antigravity.ToolViewFile),
			antigravity.AllowTool(antigravity.ToolListDir),
			antigravity.AllowTool(antigravity.ToolSearchDir),
			antigravity.AllowTool(antigravity.ToolFindFile),
			antigravity.AllowTool(antigravity.ToolEditFile,
				antigravity.When(isMarkdownInTarget),
				antigravity.Named("allow-edit-md-only-in-target")),
			// Order does not matter here: the specific rules above win over
			// this catch-all, which exists so that anything unlisted is refused.
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

// underDir reports whether path is dir or something inside it. Comparing
// prefixes on the string alone would let /tmp/docs-backup pass for /tmp/docs.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
