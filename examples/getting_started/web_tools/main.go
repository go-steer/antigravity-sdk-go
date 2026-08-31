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

// Command web_tools enables the two builtin tools that reach the network:
// search_web for finding pages, and read_url_content for reading one.
//
// EnabledTools is an allowlist, so each agent below can do exactly one thing.
// That is the point: an agent that can only search cannot also write files.
//
//	go run ./examples/getting_started/web_tools
package main

import (
	"context"
	"fmt"
	"log"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	ctx := context.Background()
	if err := runWebSearch(ctx); err != nil {
		log.Fatal(err)
	}
	if err := runURLFetching(ctx); err != nil {
		log.Fatal(err)
	}
}

func runWebSearch(ctx context.Context) error {
	fmt.Print("=== 1. Web search ===\n\n")

	agent, err := antigravity.New(ctx,
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnabledTools: []antigravity.BuiltinTool{antigravity.ToolSearchWeb},
		}))
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "What is the current weather and temperature in New York " +
		"City right now? Please provide the source."
	fmt.Printf("User: %s\n\n", prompt)
	fmt.Println("Agent is thinking and searching...")

	return answer(ctx, agent, prompt)
}

func runURLFetching(ctx context.Context) error {
	fmt.Print("\n=== 2. URL fetching (read_url_content) ===\n\n")

	agent, err := antigravity.New(ctx,
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnabledTools: []antigravity.BuiltinTool{
				antigravity.ToolReadURLContent,
				// Long pages are cached to disk rather than pasted into the
				// context window, so the agent needs view_file to read them
				// back.
				antigravity.ToolViewFile,
			},
		}))
	if err != nil {
		return err
	}
	defer agent.Close()

	const targetURL = "https://en.wikipedia.org/wiki/Google"
	prompt := fmt.Sprintf("Please read the full page content from %s and tell "+
		"me the exact date that Google acquired DeepMind Technologies.", targetURL)
	fmt.Printf("User: %s\n\n", prompt)
	fmt.Println("Agent is fetching and reading URL content...")

	return answer(ctx, agent, prompt)
}

func answer(ctx context.Context, agent *antigravity.Agent, prompt string) error {
	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Printf("\nAgent response:\n%s\n", text)
	return nil
}
