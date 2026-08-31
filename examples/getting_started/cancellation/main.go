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

// Command cancellation stops a turn two different ways and shows how to tell
// them apart.
//
// Cancelling the response tells the harness to abandon the turn: the agent
// stops working, the session stays usable, and the stream ends with
// ErrCancelled. Cancelling the context abandons the caller's side of the
// stream and surfaces context.Canceled; the turn itself may still be running
// on the harness, so the response should be closed to tear it down.
//
// The distinction matters for cost and for state. Only the first one actually
// stops the agent.
//
//	go run ./examples/getting_started/cancellation
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	agent, err := antigravity.New(ctx)
	if err != nil {
		return err
	}
	defer agent.Close()

	if err := cancelTheTurn(ctx, agent); err != nil {
		return err
	}
	if err := cancelTheContext(ctx, agent); err != nil {
		return err
	}

	fmt.Println("\n  Finished the cancellation example.")
	return nil
}

// cancelTheTurn aborts the agent's work through the response handle.
func cancelTheTurn(ctx context.Context, agent *antigravity.Agent) error {
	fmt.Println("\n=== Scenario 1: cancelling the turn ===")

	const prompt = "Write a very long story about a character named cancellation."
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	defer resp.Close()

	// Render on another goroutine so this one can interrupt it. The rendering
	// side does not need to know it is about to be cancelled.
	done := make(chan error, 1)
	go func() { done <- render(resp) }()

	fmt.Println("\n  [Letting generation run for 2 seconds...]")
	select {
	case err := <-done:
		fmt.Println("\n  [The turn finished before it could be cancelled.]")
		return ignoreCancelled(err)
	case <-time.After(2 * time.Second):
	}

	fmt.Println("\n  [Aborting the turn via resp.Cancel]")
	if err := resp.Cancel(ctx); err != nil {
		return err
	}

	// The stream ends with ErrCancelled, which distinguishes a deliberate abort
	// from a failure.
	if err := <-done; errors.Is(err, antigravity.ErrCancelled) {
		fmt.Printf("\n  [Turn cancelled] The client aborted the turn: %v\n", err)
		return nil
	} else if err != nil {
		return err
	}
	fmt.Println("\n  [The turn completed before the cancel landed.]")
	return nil
}

// cancelTheContext walks away from the stream instead of stopping the agent.
func cancelTheContext(ctx context.Context, agent *antigravity.Agent) error {
	fmt.Println("\n=== Scenario 2: cancelling the context ===")

	const prompt = "Write a very long poem about a character named interruption."
	fmt.Println("  User:", prompt)

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resp, err := agent.Chat(turnCtx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	// Closing the response is what actually releases the turn; cancelling the
	// context alone only unblocks the caller.
	defer resp.Close()

	done := make(chan error, 1)
	go func() { done <- render(resp) }()

	fmt.Println("\n  [Letting generation run for 2 seconds...]")
	select {
	case err := <-done:
		fmt.Println("\n  [The turn finished before the context was cancelled.]")
		return ignoreCancelled(err)
	case <-time.After(2 * time.Second):
	}

	fmt.Println("\n  [Cancelling the context]")
	cancel()

	if err := <-done; errors.Is(err, context.Canceled) {
		fmt.Printf("\n  [Context cancelled] The caller stopped reading: %v\n", err)
		return nil
	} else if err != nil && !errors.Is(err, antigravity.ErrCancelled) {
		return err
	}
	return nil
}

// render streams thoughts and then text, mirroring what a UI would do.
func render(resp *antigravity.ChatResponse) error {
	fmt.Print("  Agent thoughts: ")
	for thought, err := range resp.Thoughts() {
		if err != nil {
			return err
		}
		fmt.Print(thought)
	}

	fmt.Print("\n  Agent response: ")
	for text, err := range resp.Text() {
		if err != nil {
			return err
		}
		fmt.Print(text)
	}
	fmt.Println()
	return nil
}

func ignoreCancelled(err error) error {
	if errors.Is(err, antigravity.ErrCancelled) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
