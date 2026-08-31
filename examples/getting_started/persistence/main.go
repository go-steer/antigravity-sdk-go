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

// Command persistence resumes a conversation in a second, independent session.
//
// Two things have to match for a session to pick up where another left off: the
// save directory, which is where history and artifacts live on disk, and the
// conversation ID, which selects one conversation inside it. The runtime
// assigns the ID on first connect, so read it off the agent and store it
// somewhere durable.
//
// The two sessions below could just as well be two runs of a program, days
// apart.
//
//	go run ./examples/getting_started/persistence
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	saveDir, err := os.MkdirTemp("", "agent_session_")
	if err != nil {
		return err
	}
	// A real program would keep this directory. Cleaning up here keeps the
	// example from littering /tmp.
	defer os.RemoveAll(saveDir)
	fmt.Println("  Save directory:", saveDir)

	fmt.Println("\n  === Session 1: establishing context ===")
	conversationID, err := establish(ctx, saveDir)
	if err != nil {
		return err
	}
	fmt.Print("  Session 1 ended.\n\n")

	fmt.Println("  === Session 2: resuming and verifying recall ===")
	if err := resume(ctx, saveDir, conversationID); err != nil {
		return err
	}
	fmt.Println("  Session 2 ended.")

	return nil
}

// establish says something worth remembering and returns the ID needed to get
// back to it.
func establish(ctx context.Context, saveDir string) (string, error) {
	agent, err := antigravity.New(ctx, antigravity.WithSaveDir(saveDir))
	if err != nil {
		return "", err
	}
	defer agent.Close()

	if err := ask(ctx, agent, "Remember this: my favorite color is blue."); err != nil {
		return "", err
	}

	// The ID is assigned during the handshake, so it is only available once the
	// agent is running.
	id := agent.ConversationID()
	fmt.Println("  Assigned conversation ID:", id)
	return id, nil
}

// resume restores the earlier trajectory and checks that the agent still knows
// what it was told.
func resume(ctx context.Context, saveDir, conversationID string) error {
	agent, err := antigravity.New(ctx,
		antigravity.WithSaveDir(saveDir),
		antigravity.WithConversationID(conversationID),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "What is my favorite color?")
}

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
	fmt.Println("  Agent:", text)
	return nil
}
