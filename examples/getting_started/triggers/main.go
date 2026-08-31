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

// Command triggers runs background work alongside a session and pushes what it
// finds into the conversation.
//
// A trigger is a function that runs for the life of the session and calls
// tc.Send when something happens. What it sends lands in the agent's history as
// an automated notification, so the agent can report on it in a later turn
// without the user ever having relayed it.
//
// Two shapes are shown: Every, which handles the polling loop for you, and a
// bare Trigger, which owns its own loop and can react to anything.
//
//	go run ./examples/getting_started/triggers
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	ctx := context.Background()
	if err := runPeriodic(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\n%s\n\n", strings.Repeat("=", 60))
	if err := runCustom(ctx); err != nil {
		log.Fatal(err)
	}
}

// runPeriodic polls a support queue with the Every helper.
func runPeriodic(ctx context.Context) error {
	fmt.Println("  === Support queue trigger demo ===")
	fmt.Println("  Creating the agent and starting the session...")

	// Triggers run concurrently with the session, so shared state needs to be
	// safe for concurrent access. standby keeps the trigger quiet until turn 1
	// has finished, which keeps the demo's output readable.
	var standby atomic.Bool
	var ticks atomic.Int32

	pollQueue := antigravity.Every(time.Second, func(ctx context.Context, tc *antigravity.TriggerContext) error {
		if !standby.Load() {
			return nil
		}
		// Fire once, on the second tick after standby begins.
		if ticks.Add(1) != 2 {
			return nil
		}

		fmt.Println("\n  [trigger event] Alert: a new ticket appeared in the queue...")
		return tc.Send(ctx, "[SYSTEM ALERT] New critical ticket assigned: "+
			"(internal issue). Title: Database Connection Leak in Prod.")
	})

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt(
			"You are a system operations and support assistant. You monitor a "+
				"queue of incoming support tickets. When the user asks for "+
				"updates, you must check and report any tickets that came in "+
				"from the background system alert trigger."),
		antigravity.WithNamedTrigger("poll-support-queue", pollQueue),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	if err := ask(ctx, agent, "Your task will be to standby and simply let me "+
		"know if there are any critical tickets received."); err != nil {
		return err
	}

	// Turn 1 is done; let the trigger speak.
	standby.Store(true)
	fmt.Println("\n  Sleeping for 5 seconds. A new ticket will arrive in the background...")
	time.Sleep(5 * time.Second)

	// The trigger's message is already in history, so the agent recalls it.
	return ask(ctx, agent, "I'm back. Did anything critical come in while I was working?")
}

// runCustom uses a bare Trigger that owns its loop, which is what you want when
// the event source is push-based rather than a poll on a fixed interval.
func runCustom(ctx context.Context) error {
	fmt.Println("  === Custom webhook trigger demo ===")
	fmt.Println("  Creating the agent and starting the session...")

	var active atomic.Bool

	webhookListener := func(ctx context.Context, tc *antigravity.TriggerContext) error {
		fmt.Printf("\n  [webhook trigger] %s started...\n", tc.Name())

		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		tick := 0
		for {
			// The context is cancelled when the session ends, which is what
			// stops the trigger. A trigger that ignores it leaks.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}

			if !active.Load() {
				continue
			}
			tick++
			if tick == 3 {
				fmt.Println("\n  [webhook trigger] Event received: 'AppBuild-42' status FAILED.")
				return tc.Send(ctx, "[WEBHOOK ALERT] CI/CD Build Pipeline "+
					"'AppBuild-42' FAILED on branch 'main'. Reason: Lint errors "+
					"in routes.py.")
			}
		}
	}

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt(
			"You are a CI/CD operations assistant. You monitor pipeline status "+
				"via an external webhook trigger. When the user asks for "+
				"updates, you must check and report any failures that came in "+
				"from the webhook alert trigger."),
		antigravity.WithNamedTrigger("webhook-listener", webhookListener),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	if err := ask(ctx, agent, "Your task will be to standby and simply let me "+
		"know if there are any critical pipeline webhook alerts received."); err != nil {
		return err
	}

	active.Store(true)
	fmt.Println("\n  Sleeping for 5 seconds. A pipeline failure will arrive in the background...")
	time.Sleep(5 * time.Second)

	if err := ask(ctx, agent, "I'm back. Any updates on my builds?"); err != nil {
		return err
	}

	fmt.Println("\n  Ending the session. Background triggers stop with it.")
	return nil
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
