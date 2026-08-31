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

// Command slash_commands sends the builtin /plan command programmatically.
//
// A slash command is a Content part like any other, so it goes in the same
// prompt slice as the text it applies to. /plan puts the agent in planning
// mode: instead of editing straight away it writes an implementation plan to
// the app data directory and waits for approval, which is why the agent below
// runs in interactive mode.
//
//	go run ./examples/getting_started/slash_commands
package main

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// One temporary directory serves as both the workspace and the app data
	// directory, so the generated plan is easy to find and easy to throw away.
	dir, err := os.MkdirTemp("", "slash_commands_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	agent, err := antigravity.New(ctx,
		antigravity.WithWorkspaces(dir),
		antigravity.WithAppDataDir(dir),
		// Planning writes files and runs commands, and stopping to confirm each
		// one would drown out the point of the example.
		antigravity.WithPolicies(antigravity.One(antigravity.AllowAll())),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			Behavior: antigravity.BehaviorInteractive,
		}),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Print("--- Programmatic /plan slash command ---\n\n")

	resp, err := agent.Chat(ctx,
		antigravity.SlashCommand{Name: antigravity.SlashPlan},
		antigravity.Text("Write a Go program that prints the numbers 1 to 10."),
	)
	if err != nil {
		return err
	}
	if err := render(resp); err != nil {
		return err
	}

	return showPlan(dir)
}

// render prints thoughts and text as they arrive, labelling each time the
// stream switches between the two.
func render(resp *antigravity.ChatResponse) error {
	const (
		gray  = "\033[90m"
		green = "\033[32m"
		reset = "\033[0m"
	)

	kind := ""
	for chunk, err := range resp.Chunks() {
		if err != nil {
			return err
		}
		switch c := chunk.(type) {
		case antigravity.ThoughtChunk:
			if kind != "thought" {
				fmt.Printf("\n%s[Thought]: ", gray)
				kind = "thought"
			}
			fmt.Print(c.Text)
		case antigravity.TextChunk:
			if kind != "text" {
				if kind == "thought" {
					fmt.Print(reset)
				}
				fmt.Printf("\n%s[Response]:%s ", green, reset)
				kind = "text"
			}
			fmt.Print(c.Text)
		}
	}
	if kind == "thought" {
		fmt.Print(reset)
	}
	fmt.Println()
	return nil
}

// showPlan finds the plan artifact and prints the top of it.
func showPlan(dir string) error {
	var plan string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			plan = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return err
	}
	if plan == "" {
		fmt.Println("\nNo plan artifact was written.")
		return nil
	}

	fmt.Printf("\nPlan written to %s\n\n", plan)

	f, err := os.Open(plan)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Println("--- First 5 lines ---")
	scanner := bufio.NewScanner(f)
	for i := 0; i < 5 && scanner.Scan(); i++ {
		fmt.Println(" ", scanner.Text())
	}
	fmt.Println("---------------------")
	return scanner.Err()
}
