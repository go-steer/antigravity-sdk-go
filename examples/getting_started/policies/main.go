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

// Command policies locks an agent down with declarative tool-call rules.
//
// Policies decide at call time. The tool stays visible to the model, and a
// denial comes back as an explanation the model can read and adapt to, rather
// than as a missing capability it never knew it had. Use capabilities to hide a
// tool entirely; use a policy when the answer depends on the arguments.
//
// The default for a new agent is ConfirmRunCommand: everything allowed except
// run_command. This example replaces that with a deny-by-default posture and
// allows back only what it needs. AllowAll() opens everything, shell included.
//
//	go run ./examples/getting_started/policies
package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

type secretArgs struct {
	SecretName string `json:"secret_name" jsonschema:"description=The name of the secret to look up."`
}

func lookupSecret(_ context.Context, args secretArgs) (string, error) {
	return "SUPER_SECRET_VALUE_FOR_" + args.SecretName, nil
}

// runCommandArgs mirrors the harness's run_command arguments. Only the fields a
// predicate reads need to be declared.
type runCommandArgs struct {
	CommandLine string `json:"command_line"`
}

// blocksRM matches shell invocations that mention rm.
func blocksRM(_ context.Context, call antigravity.ToolCall) (bool, error) {
	var args runCommandArgs
	if err := call.UnmarshalArgs(&args); err != nil {
		// Failing to parse means failing to prove the call is safe.
		return true, nil
	}
	return strings.Contains(args.CommandLine, "rm"), nil
}

// touchesCriticalFile matches writes to keys and to anything production.
//
// CanonicalPath is filled in by the connection layer for file tools, so a
// predicate can match a platform-native absolute path without knowing which
// argument name this particular tool uses for it.
func touchesCriticalFile(_ context.Context, call antigravity.ToolCall) (bool, error) {
	path := call.CanonicalPath
	return strings.HasSuffix(path, ".key") || strings.Contains(path, "production"), nil
}

// approveProgrammatically stands in for a human. An interactive program would
// pass antigravity.ConfirmInTerminal() instead.
func approveProgrammatically(_ context.Context, call antigravity.ToolCall) (bool, error) {
	fmt.Printf("\n  [ask-user] Intercepted a request for tool: %s\n", call.Name)
	fmt.Printf("  [ask-user] Arguments: %s\n", call.Args)
	fmt.Println("  [ask-user] Simulating user review... decision: DENY.")
	return false, nil
}

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	fmt.Println("  === Tool call policies demo ===")

	// Rules are evaluated most-specific-first: a specific deny beats a specific
	// ask, which beats a specific allow, which beats a wildcard. Order within
	// the slice is therefore documentation, not precedence.
	policies := antigravity.One(
		// 1. Deny everything by default.
		antigravity.DenyAll(),

		// 2. Allow reading directory contents.
		antigravity.AllowTool(antigravity.ToolListDir),

		// 3. Allow shell, except when the command mentions rm.
		antigravity.AllowTool(antigravity.ToolRunCommand),
		antigravity.DenyTool(antigravity.ToolRunCommand,
			antigravity.When(blocksRM),
			antigravity.Named("block-rm")),

		// 4. Allow writes, but ask before touching a critical file.
		antigravity.AllowTool(antigravity.ToolEditFile),
		antigravity.AllowTool(antigravity.ToolCreateFile),
		antigravity.AskUserTool(antigravity.ToolEditFile, approveProgrammatically,
			antigravity.When(touchesCriticalFile),
			antigravity.Named("ask-for-critical-edits")),
		antigravity.AskUserTool(antigravity.ToolCreateFile, approveProgrammatically,
			antigravity.When(touchesCriticalFile),
			antigravity.Named("ask-for-critical-creates")),

		// 5. Deny a custom tool by name.
		antigravity.Deny("lookup_secret", antigravity.Named("block-secret-lookup")),
	)

	agent, err := antigravity.New(ctx,
		antigravity.WithTools(antigravity.MustNewTool("lookup_secret",
			"Looks up a secret by name.", lookupSecret)),
		antigravity.WithPolicies(policies),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("\n  Chatting with the agent...")
	for _, prompt := range []string{
		// Allowed: list_directory has an explicit allow.
		"List the files in the current directory.",
		// Denied by block-rm.
		"Delete all files using rm -rf.",
		// Routed to the ask-user handler, which declines.
		"Create a new configuration file named production.key with content 'debug=true'.",
		// Denied by block-secret-lookup.
		"Look up the secret named 'api_key' using lookup_secret.",
	} {
		fmt.Printf("\n  User: %s\n", prompt)
		resp, err := agent.Chat(ctx, antigravity.Text(prompt))
		if err != nil {
			return err
		}
		text, err := resp.Wait()
		if err != nil {
			return err
		}
		fmt.Println("  Agent:", text)
	}

	return nil
}
