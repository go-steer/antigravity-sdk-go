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

// Package antigravity is a Go SDK for building AI agents powered by
// Antigravity and Gemini.
//
// The SDK provides a stateful infrastructure layer that abstracts the agentic
// loop, letting you focus on what your agent does rather than how it runs. The
// loop itself is executed by a companion binary, localharness, which the SDK
// launches and drives over a local WebSocket.
//
// # Quickstart
//
//	agent, err := antigravity.New(ctx,
//		antigravity.WithSystemPrompt("You are a helpful assistant."),
//	)
//	if err != nil {
//		return err
//	}
//	defer agent.Close()
//
//	resp, err := agent.Chat(ctx, antigravity.Text("Hello!"))
//	if err != nil {
//		return err
//	}
//	text, err := resp.Wait()
//
// Runnable programs covering every feature live in examples/.
//
// # Safety defaults
//
// By default an Agent exposes all builtin tools but denies run_command, via
// the [ConfirmRunCommand] policy. Supply an ask-user handler to turn that
// denial into an interactive confirmation, or pass [AllowAll] for fully
// autonomous execution including shell access. File tools are automatically
// scoped to the configured workspaces, which default to the process working
// directory.
//
// Enabling write tools or MCP servers with an empty policy list and no
// pre-tool-call hook is rejected at startup rather than silently permitted.
//
// # The harness binary
//
// The SDK requires the localharness binary. It is located via the
// ANTIGRAVITY_HARNESS_PATH environment variable, or by searching PATH. Use
// [WithBinaryPath] to point at it explicitly.
package antigravity
