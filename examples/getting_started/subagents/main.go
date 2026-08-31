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

// Command subagents delegates work to other agents three ways.
//
// Delegation buys two things: a clean context window, since the subagent's
// exploration does not fill the caller's history, and a smaller blast radius,
// since a subagent gets only the tools it was granted. The third demo leans on
// the second — the reviewer cannot reach the root agent's secret tool, and says
// so in its report.
//
//   - Dynamic self-delegation: the agent clones itself for a heavy task.
//   - Static subagent: a named 'code_reviewer' with its own prompt and tools.
//   - Nested hierarchy: root -> lead_researcher -> fact_checker, bounded by
//     MaxSubagentDepth and AllowedSubagents.
//
// Run with:
//
//	go run ./examples/getting_started/subagents
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx := context.Background()
	for _, demo := range []func(context.Context, *slog.Logger) error{
		runDynamicSubagent,
		runStaticSubagent,
		runNestedSubagents,
	} {
		if err := demo(ctx, logger); err != nil {
			log.Fatal(err)
		}
	}
}

// subagentDepth tracks whether a subagent is currently running, so the tool log
// can indent its calls. Hooks run concurrently with the read loop, so this is
// atomic.
var subagentActive atomic.Bool

func logPreTool(_ context.Context, _ *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
	if call.Name == string(antigravity.ToolStartSubagent) {
		subagentActive.Store(true)
		fmt.Println("\n  --- [hook] Spawning a subagent ---")
		fmt.Printf("  Arguments: %s\n\n", call.Args)
		return antigravity.ToolDecision{}, nil
	}
	fmt.Printf("%s- [start]: %s (id: %s)\n", indent(), call.Name, call.ID)
	return antigravity.ToolDecision{}, nil
}

func logPostTool(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	if result.Name == string(antigravity.ToolStartSubagent) {
		subagentActive.Store(false)
		fmt.Println("\n  --- [hook] Subagent finished ---")
		fmt.Printf("  Result: %v\n\n", result.Result)
		return nil
	}
	fmt.Printf("%s- [done]:  %s (id: %s)\n", indent(), result.Name, result.ID)
	return nil
}

func indent() string {
	if subagentActive.Load() {
		return "    "
	}
	return "  "
}

func reviewerBadge(_ context.Context, _ struct{}) (string, error) {
	return "Senior-L3-Auditor-Badge", nil
}

func rootAdminSecret(_ context.Context, _ struct{}) (string, error) {
	return "SUPER_SECRET_ROOT_PASSWORD_12345", nil
}

// runDynamicSubagent lets the agent spawn a clone of itself. Nothing is
// declared up front — enabling subagents is enough.
func runDynamicSubagent(ctx context.Context, logger *slog.Logger) error {
	fmt.Println("\n=== Dynamic subagent (self clone) ===")

	agent, err := antigravity.New(ctx,
		antigravity.WithLogger(logger),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnableSubagents: true,
		}),
		antigravity.WithPreToolCallHook(logPreTool),
		antigravity.WithPostToolCallHook(logPostTool),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	_, err = ask(ctx, agent, "Use a subagent to research the Antigravity SDK "+
		"examples in the parent directory. Delegate the task of listing and "+
		"reading the files to the subagent, and then generate a lesson plan "+
		"for me to learn more based on its findings.")
	return err
}

// runStaticSubagent declares a reviewer with its own instructions and a single
// tool, then checks that the isolation held.
func runStaticSubagent(ctx context.Context, logger *slog.Logger) error {
	fmt.Println("\n=== Custom static subagent ===")

	workspace, err := os.MkdirTemp("", "subagent_workspace_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	const targetName = "target_code.go"
	target := filepath.Join(workspace, targetName)
	code := "package widget\n\n" +
		"func Hello() {\n\tprintln(\"hello\")\n}\n\n" +
		"// Add returns the sum of a and b.\n" +
		"func Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(target, []byte(code), 0o644); err != nil {
		return err
	}

	badge := antigravity.MustNewTool("get_reviewer_badge",
		"Returns the reviewer's official certification badge name.", reviewerBadge)
	secret := antigravity.MustNewTool("get_root_admin_secret",
		"Returns the root admin password. For root administration only.", rootAdminSecret)

	reviewer := antigravity.SubagentConfig{
		Name:        "code_reviewer",
		Description: "Audits source files and reports missing doc comments.",
		Instructions: antigravity.CustomInstructions{Text: "You are a code " +
			"reviewer. Read Go files in the workspace and check whether every " +
			"exported function declaration has a doc comment. For each one " +
			"that is missing, output a warning prefixed with " +
			"'[AUDIT_WARNING]'. CRITICAL: every warning you output MUST start " +
			"with '[AUDIT_WARNING]'. Use the 'get_reviewer_badge' tool to sign " +
			"your final audit report with your official badge name. Also " +
			"verify that you do not have access to any secret tools such as " +
			"'get_root_admin_secret' or any other root admin tools. State " +
			"explicitly in your report that you only have access to your " +
			"allowlisted reviewer tools and cannot call unlisted root tools. " +
			"Output your report directly in your final response."},
		// The reviewer gets this tool and no other custom tool, whatever the
		// root agent holds.
		Tools: []antigravity.Tool{badge},
	}

	agent, err := antigravity.New(ctx,
		antigravity.WithLogger(logger),
		antigravity.WithSubagents(reviewer),
		antigravity.WithWorkspaces(workspace),
		antigravity.WithTools(badge, secret),
		antigravity.WithPreToolCallHook(logPreTool),
		antigravity.WithPostToolCallHook(logPostTool),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	text, err := ask(ctx, agent, fmt.Sprintf("Ask the 'code_reviewer' subagent "+
		"to review %s, sign the report with their reviewer badge name, and "+
		"verify whether they have access to the 'get_root_admin_secret' tool. "+
		"Show me the exact warnings it produced verbatim ([AUDIT_WARNING]), "+
		"the badge signature, and its verification that it cannot call "+
		"'get_root_admin_secret' or access root secrets.", targetName))
	if err != nil {
		return err
	}

	fmt.Println("\n  === Verification results ===")
	report("custom instructions applied ([AUDIT_WARNING] prefix)",
		strings.Contains(text, "[AUDIT_WARNING]"))
	report("allowlisted tool reachable (badge signature present)",
		strings.Contains(text, "Senior-L3-Auditor-Badge"))
	report("root secret isolated (get_root_admin_secret never ran)",
		!strings.Contains(text, "SUPER_SECRET_ROOT_PASSWORD_12345"))
	return nil
}

// runNestedSubagents builds a three-tier chain. Depth and reachability are
// capped at the session level, so no prompt can talk the agent into a deeper
// chain than the configuration allows.
func runNestedSubagents(ctx context.Context, logger *slog.Logger) error {
	fmt.Println("\n=== Hierarchical nested subagents ===")

	workspace, err := os.MkdirTemp("", "nested_workspace_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	files := map[string]string{
		"design.md": "# Widget Design\n\n" +
			"The widget uses a pub/sub architecture with at-least-once delivery.\n" +
			"Messages are persisted to a WAL before acknowledgement.\n",
		"perf_data.txt": "p50: 12ms, p99: 145ms, error_rate: 0.02%\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o644); err != nil {
			return err
		}
	}

	// Tier 3, a leaf: it can read, and it cannot delegate, because
	// start_subagent is not in its allowlist.
	factChecker := antigravity.SubagentConfig{
		Name: "fact_checker",
		Description: "Reads specific files and verifies factual claims. " +
			"Reports findings back to the caller.",
		Capabilities: &antigravity.SubagentCapabilities{
			EnabledTools: []antigravity.BuiltinTool{
				antigravity.ToolViewFile,
				antigravity.ToolFindFile,
			},
		},
	}

	// Tier 2: it may delegate, but only to fact_checker.
	leadResearcher := antigravity.SubagentConfig{
		Name: "lead_researcher",
		Description: "Researches a topic by reading files and delegating " +
			"fact-checking to the 'fact_checker' subagent.",
		Capabilities: &antigravity.SubagentCapabilities{
			EnabledTools: []antigravity.BuiltinTool{
				antigravity.ToolViewFile,
				antigravity.ToolFindFile,
				antigravity.ToolListDir,
				antigravity.ToolStartSubagent,
			},
			AllowedSubagents: []string{"fact_checker"},
		},
	}

	// Tier 1: the root sees only lead_researcher, and the whole session is
	// capped at three levels.
	agent, err := antigravity.New(ctx,
		antigravity.WithLogger(logger),
		antigravity.WithSubagents(leadResearcher, factChecker),
		antigravity.WithWorkspaces(workspace),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnableSubagents:  true,
			MaxSubagentDepth: 3,
			AllowedSubagents: []string{"lead_researcher"},
		}),
		antigravity.WithPreToolCallHook(logPreTool),
		antigravity.WithPostToolCallHook(logPostTool),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	_, err = ask(ctx, agent, "Use the 'lead_researcher' subagent to investigate "+
		"the design and performance data in the workspace. The lead_researcher "+
		"should delegate fact-checking of specific claims to 'fact_checker'. "+
		"Give me a summary of the architecture and performance profile.")
	return err
}

func ask(ctx context.Context, agent *antigravity.Agent, prompt string) (string, error) {
	fmt.Println("  User:", prompt)
	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return "", err
	}
	text, err := resp.Wait()
	if err != nil {
		return "", err
	}
	fmt.Printf("\n  Agent:\n%s\n", text)
	return text, nil
}

func report(check string, ok bool) {
	status := "[FAIL]"
	if ok {
		status = "[PASS]"
	}
	fmt.Printf("  %s %s\n", status, check)
}
