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

// Command mcp_tools connects the agent to an MCP server over both transports,
// then narrows what it may do two different ways.
//
// The server is examples/resources/piratemath, which offers pirate_multiply
// and pirate_divide. Filtering with DisabledTools removes a tool from the
// model's context, so it is never offered and costs no tokens. Denying it with
// a policy leaves it visible and rejects the call, so the model learns why it
// was refused. Pick the first for tools the agent should never see, and the
// second for anything conditional.
//
// Run from the repository root:
//
//	go run ./examples/getting_started/mcp_tools
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	antigravity "github.com/go-steer/antigravity-sdk-go"
	"github.com/go-steer/antigravity-sdk-go/examples/resources/piratemath"
)

func main() {
	// The work is in run so the deferred cleanup of the built server actually
	// runs: log.Fatal exits the process without unwinding.
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	// The harness launches the stdio server itself, so it needs a real
	// executable rather than a `go run` invocation with this program's working
	// directory.
	serverPath, cleanup, err := buildServer(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	for _, demo := range []func(context.Context, string) error{
		mcpStdio,
		mcpFiltering,
		mcpPolicies,
	} {
		if err := demo(ctx, serverPath); err != nil {
			return err
		}
	}
	return mcpHTTP(ctx)
}

// buildServer compiles the example MCP server and returns the binary's path.
func buildServer(ctx context.Context) (path string, cleanup func(), err error) {
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

// mcpStdio runs the server as a subprocess of the harness.
func mcpStdio(ctx context.Context, serverPath string) error {
	fmt.Println("\n  --- Stdio transport ---")

	server := antigravity.NewMCPStdioServer("pirate_math", serverPath, "--transport=stdio")

	agent, err := antigravity.New(ctx, antigravity.WithMCPServers(server))
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "Use the pirate_multiply tool to multiply 5 and 7.")
}

// mcpFiltering hides one of the server's tools.
func mcpFiltering(ctx context.Context, serverPath string) error {
	fmt.Println("\n  --- Tool filtering (DisabledTools) ---")

	server := antigravity.NewMCPStdioServer("pirate_math", serverPath, "--transport=stdio")
	server.DisabledTools = []string{"pirate_divide"}

	agent, err := antigravity.New(ctx, antigravity.WithMCPServers(server))
	if err != nil {
		return err
	}
	defer agent.Close()

	if err := ask(ctx, agent, "Use the pirate_multiply tool to multiply 6 and 8."); err != nil {
		return err
	}
	// pirate_divide is not in the model's context at all, so expect the agent
	// to say it cannot do this rather than to try and be refused.
	return ask(ctx, agent, "Use the pirate_divide tool to divide 10 by 2.")
}

// mcpPolicies leaves both tools visible and gates them at call time.
func mcpPolicies(ctx context.Context, serverPath string) error {
	fmt.Println("\n  --- Safety policies for MCP tools ---")

	server := antigravity.NewMCPStdioServer("pirate_math", serverPath, "--transport=stdio")

	agent, err := antigravity.New(ctx,
		antigravity.WithMCPServers(server),
		// AllowMCP and DenyMCP each expand to one policy per named tool, which
		// is why WithPolicies takes groups rather than individual rules.
		antigravity.WithPolicies(
			antigravity.One(antigravity.DenyAll()),
			antigravity.AllowMCP(server.Name(), []string{"pirate_multiply"}),
			antigravity.DenyMCP(server.Name(), []string{"pirate_divide"}),
		),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	if err := ask(ctx, agent, "Multiply 4 and 9 using the pirate_multiply tool."); err != nil {
		return err
	}
	// Visible, attempted, and refused at runtime with an explanation.
	return ask(ctx, agent, "Divide 12 by 3 using the pirate_divide tool.")
}

// mcpHTTP serves MCP in this process and points the harness at the URL.
func mcpHTTP(ctx context.Context) error {
	fmt.Println("\n  --- Streamable HTTP transport ---")

	url, stop, err := piratemath.Start(ctx)
	if err != nil {
		return err
	}
	defer stop()

	agent, err := antigravity.New(ctx,
		antigravity.WithMCPServers(antigravity.NewMCPHTTPServer("pirate_math", url)))
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "Use the pirate_multiply tool to multiply 5 and 7.")
}

func ask(ctx context.Context, agent *antigravity.Agent, prompt string) error {
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
	return nil
}
