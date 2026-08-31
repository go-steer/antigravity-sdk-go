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

// Command autonomous_shell gives the agent unattended shell access.
//
// By default the SDK installs ConfirmRunCommand, which denies run_command
// unless an ask-user handler approves it. Coding assistants and automation
// agents need the shell without a human in the loop, and AllowAll is the opt
// out.
//
// AllowAll means exactly what it says: every tool, including arbitrary shell
// commands, with nothing to stop a bad one. Use it only where you would be
// comfortable running the agent's output yourself. For anything less trusted,
// see examples/getting_started/policies, which narrows access rule by rule.
//
//	go run ./examples/getting_started/autonomous_shell
package main

import (
	"context"
	"fmt"
	"log"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithPolicies(antigravity.One(antigravity.AllowAll())))
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "Run 'echo Hello from the shell!' and show me the output."
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
