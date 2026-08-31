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

// Command persona_config shapes the agent's system prompt two ways.
//
// TemplatedInstructions is what almost everyone wants: it overrides the
// agent's identity and appends your own titled sections, while keeping the
// harness's default prompt — safety mandates, tool protocols, workspace and
// skill context — underneath.
//
// CustomInstructions replaces all of that. Nothing is added for you: no
// environment context, no skill listing, no subagent coordination rules. It is
// a break-glass feature, and this example shows the amount of scaffolding you
// take on by using it.
//
// Run from the repository root, so the relative skills path resolves:
//
//	go run ./examples/getting_started/persona_config
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

const skillsDir = "examples/skills/code-review"

func main() {
	ctx := context.Background()
	if err := runTemplated(ctx); err != nil {
		log.Fatal(err)
	}
	if err := runCustom(ctx); err != nil {
		log.Fatal(err)
	}
}

type styleGuideArgs struct {
	Language string `json:"language" jsonschema:"description=The programming language to look up."`
}

func checkStyleGuide(_ context.Context, args styleGuideArgs) (string, error) {
	switch strings.ToLower(args.Language) {
	case "go", "golang":
		return "Use MixedCaps, not underscores. Keep initialisms capitalized: userID, not userId.", nil
	case "python":
		return "Use snake_case for functions and variables. Use CamelCase for classes.", nil
	default:
		return "No specific rules found.", nil
	}
}

// runTemplated overrides the identity and appends sections, keeping the
// harness defaults.
func runTemplated(ctx context.Context) error {
	fmt.Println("  === Templated system instructions ===")

	instructions := antigravity.TemplatedInstructions{
		Identity: "You are an expert Code Quality Reviewer.\n" +
			"Your role is to review code for readability, maintainability, " +
			"and adherence to style guides.",
		// Titled sections are passed through as structure rather than prose,
		// which makes them easier for the model to follow selectively.
		Sections: []antigravity.InstructionSection{{
			Title: "review_criteria",
			Content: "- Focus on readability and simplicity.\n" +
				"- Ensure meaningful variable and function names.",
		}, {
			Title: "style_guide_instructions",
			// Naming the tool explicitly grounds the agent in the toolset it
			// actually has.
			Content: "When reviewing Go code, use the `check_style_guide` " +
				"tool to verify rules.",
		}},
	}

	agent, err := antigravity.New(ctx,
		antigravity.WithInstructions(instructions),
		antigravity.WithTools(antigravity.MustNewTool("check_style_guide",
			"Checks the style guide rules for a given language.", checkStyleGuide)),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "Review this Go code: `func MY_FUNCTION(X int) int { return X*2 }`")
}

// runCustom replaces the system prompt outright, and therefore has to assemble
// everything the harness would normally supply.
func runCustom(ctx context.Context) error {
	fmt.Println("\n  === Custom system instructions ===")

	skillPath, err := filepath.Abs(skillsDir)
	if err != nil {
		return err
	}

	// Under a full override the SDK's environment context is gone, so gather
	// it here and paste it into the prompt.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	userInfo := fmt.Sprintf(`
<user_information>
Operating System: %s
Active Workspace CWD: %s
Storage Directory (App Data): %s
</user_information>
`, runtime.GOOS, cwd, filepath.Join(home, ".gemini", "antigravity"))

	prompt := identityText + skillsInstructions([]string{skillPath}) + guidelinesText + userInfo

	agent, err := antigravity.New(ctx,
		antigravity.WithInstructions(antigravity.CustomInstructions{Text: prompt}),
		antigravity.WithTools(antigravity.MustNewTool("check_style_guide",
			"Checks the style guide rules for a given language.", checkStyleGuide)),
		antigravity.WithSkillsPaths(skillPath),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "Review this Go code: `func foo(x int) int { return x+1 }`")
}

// skillsInstructions compiles the skill listing the harness would otherwise
// prepend for us.
//
// A production version would parse the description out of each SKILL.md's YAML
// frontmatter; this one hardcodes it to stay standalone.
func skillsInstructions(paths []string) string {
	if len(paths) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n<skills>\n")
	b.WriteString("Skills enhance your abilities with specialized expertise and repeatable workflows to help solve advanced workflows.\n")
	b.WriteString("When a task matches an available skill's description, you must inspect the complete SKILL.md with your 'view_file' tool in order to understand its capabilities.\n\n")
	b.WriteString("Available skills:\n")
	for _, path := range paths {
		fmt.Fprintf(&b, "* **%s** (located at `%s/SKILL.md`) — Provides guidelines for code readability, style compliance, and refactoring.\n",
			filepath.Base(path), path)
	}
	b.WriteString("</skills>\n")
	return b.String()
}

const identityText = `
<identity>
You are an expert Code Quality Reviewer agent. Your goal is to help developers maintain high standards of readability, maintainability, and correctness in their code. You will receive code snippets or descriptions of code changes and provide actionable feedback. You must always prioritize addressing the user's specific questions or concerns about the code.
</identity>
`

const guidelinesText = "\n" + `
<review_guidelines>
### When to recommend refactoring:
- The code has high cyclomatic complexity (too many nested loops/conditionals).
- The code violates DRY (Don't Repeat Yourself) principles significantly.
- The code is difficult to unit test in its current form.

### Don't recommend refactoring for:
- Minor personal style preferences that don't impact readability.
- Micro-optimizations that make the code harder to understand.
</review_guidelines>

<task_management>
### When to suggest breaking up the review:
- If the provided code snippet is longer than 200 lines.
- If the user is asking for both a security audit and a performance review at the same time.
In these cases, suggest reviewing one specific aspect or file first.
</task_management>

<behavioral_principles>
1. **Acknowledge Ambiguity**: If a request is underspecified or could be interpreted in multiple ways, ask the user for clarification before proceeding.
2. **Precision**: When suggesting code changes, always specify the file path and, if applicable, the line range.
3. **Focus on Delta**: Do not restate full file contents or large blocks of code unless necessary. Focus only on what needs to change.
4. **Closure**: End every turn with a clear summary of what was accomplished and what the next steps are.
</behavioral_principles>

<review_artifact_format>
When generating a detailed review artifact in Markdown, use the following elements to ensure high quality and scannability:

### Alerts
Use GitHub-style alerts to highlight critical issues:
> [!IMPORTANT]
> Critical security or correctness issues that must be fixed.

> [!NOTE]
> General improvements or style suggestions.

### Code Diffs
When suggesting changes, use diff blocks to show exactly what to add or remove:
` + "```diff" + `
-func OldFunc() {}
+func NewFunc() {}
` + "```" + `

### Tables
Use tables to compare alternative approaches or list multiple findings:
| File | Line | Issue | Severity |
| :--- | :--- | :--- | :--- |
| main.go | 12 | Hardcoded API key | Critical |
</review_artifact_format>

<tool_usage>
You have access to the ` + "`check_style_guide`" + ` tool. When reviewing code, always use this tool to verify language-specific style rules before making recommendations.
</tool_usage>
`

func ask(ctx context.Context, agent *antigravity.Agent, prompt string) error {
	fmt.Println("  User:", prompt)
	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Printf("  Agent: %s\n", text)
	return nil
}
