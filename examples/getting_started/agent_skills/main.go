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

// Command agent_skills points the agent at a directory of skills and asks it
// what it can now do.
//
// A skill is a directory containing a SKILL.md whose YAML frontmatter carries a
// name and a description. The harness shows the agent those descriptions, and
// the agent reads the full SKILL.md with its view_file tool when a task matches
// one. Skills are how you hand an agent a repeatable procedure without spending
// system-prompt budget on it.
//
// Run from the repository root, so the relative skills path resolves:
//
//	go run ./examples/getting_started/agent_skills
package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

// skillsDir holds the example skill, relative to the repository root.
const skillsDir = "examples/skills/code-review"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// The harness resolves skill paths itself, and it does not share this
	// process's working directory, so pass an absolute path.
	path, err := filepath.Abs(skillsDir)
	if err != nil {
		return err
	}
	fmt.Println("  Loading skills from:", path)

	agent, err := antigravity.New(ctx, antigravity.WithSkillsPaths(path))
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "What available skills do you have?"
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Println("  Agent:", text)

	return nil
}
