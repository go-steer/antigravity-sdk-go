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

// Command app_data_dir_override points the agent's scratch storage somewhere
// of your choosing and verifies that an artifact lands there.
//
// The app data directory is where the harness keeps artifacts, scratch files,
// and uploaded media — distinct from the save directory, which holds
// conversation history. Overriding it matters when you need those files on a
// particular volume, inside a sandbox, or cleaned up per run.
//
// Artifacts are written to <appDataDir>/brain/<conversationID>/.
//
//	go run ./examples/getting_started/app_data_dir_override
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	appDataDir, err := os.MkdirTemp("", "agent_appdata_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(appDataDir)
	fmt.Printf("  Custom app data dir: %s\n\n", appDataDir)

	agent, err := antigravity.New(ctx, antigravity.WithAppDataDir(appDataDir))
	if err != nil {
		return err
	}
	defer agent.Close()

	conversationID := agent.ConversationID()
	fmt.Printf("  Agent session started. Conversation ID: %s\n\n", conversationID)

	const prompt = "Please create an artifact file named 'go_best_practices.md' " +
		"summarizing Go best practices."
	fmt.Println("  User: ", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Printf("  Agent: %s\n\n", text)

	artifact := filepath.Join(appDataDir, "brain", conversationID, "go_best_practices.md")
	fmt.Println("  Checking artifact location:", artifact)
	if _, err := os.Stat(artifact); err != nil {
		fmt.Println("\n  WARNING: the artifact was not found in the custom app data dir.")
		return nil
	}
	fmt.Println("\n  SUCCESS: the artifact was stored in the custom app data dir.")

	return nil
}
