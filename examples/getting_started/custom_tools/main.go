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

// Command custom_tools gives the agent two Go functions to call: a pure lookup
// and a stateful counter that accumulates across turns.
//
// Python's SDK injects a ToolContext to carry state between calls. Go does not
// need one — a tool is a closure, so state is an ordinary variable captured by
// the function, guarded by a mutex because the harness may call tools
// concurrently.
//
//	go run ./examples/getting_started/custom_tools
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// lookupArgs is the tool's parameter schema. Field names become JSON property
// names, and jsonschema tags become the descriptions the model reads, standing
// in for Python's docstring parsing.
type lookupArgs struct {
	FruitName string `json:"fruit_name" jsonschema:"description=The name of the fruit."`
}

var skus = map[string]string{
	"apple":  "SKU-APP-123",
	"banana": "SKU-BAN-456",
	"orange": "SKU-ORA-789",
}

// lookupFruitSKU is a plain function: no SDK types in its signature, so it
// stays testable on its own.
func lookupFruitSKU(_ context.Context, args lookupArgs) (string, error) {
	name := strings.ToLower(args.FruitName)
	if _, ok := skus[name]; !ok {
		name = strings.TrimSuffix(name, "s")
	}
	sku, ok := skus[name]
	if !ok {
		sku = "SKU-GEN-000"
	}
	return fmt.Sprintf("SKU for %s is %s. Order ID for restocking: ORD-%s-NEW",
		args.FruitName, sku, sku), nil
}

type recordArgs struct {
	SKU   string `json:"sku" jsonschema:"description=The SKU of the fruit."`
	Count int    `json:"count" jsonschema:"description=The number of fruits to record."`
}

// inventory is the state the recording tool accumulates.
type inventory struct {
	mu     sync.Mutex
	counts map[string]int
}

func (inv *inventory) record(_ context.Context, args recordArgs) (string, error) {
	inv.mu.Lock()
	defer inv.mu.Unlock()

	inv.counts[args.SKU] += args.Count
	return fmt.Sprintf("Recorded %d units for %s. Total count is now %d.",
		args.Count, args.SKU, inv.counts[args.SKU]), nil
}

func run(ctx context.Context) error {
	inv := &inventory{counts: map[string]int{}}

	lookup := antigravity.MustNewTool("lookup_fruit_sku",
		"Looks up the SKU for a given fruit.", lookupFruitSKU)
	record := antigravity.MustNewTool("record_fruit",
		"Records the count of fruits by SKU.", inv.record)

	agent, err := antigravity.New(ctx,
		antigravity.WithTools(lookup, record),
		antigravity.WithSystemPrompt(
			"You keep track of fruit inventory. To record fruits, you MUST "+
				"first look up the fruit's SKU using lookup_fruit_sku, and "+
				"then use that SKU with record_fruit."),
		// Deny everything, then allow back exactly these two tools. Policies
		// are evaluated most-specific-first, so the named allows win over the
		// catch-all deny.
		antigravity.WithPolicies(antigravity.One(
			antigravity.DenyAll(),
			antigravity.Allow("lookup_fruit_sku"),
			antigravity.Allow("record_fruit"),
		)),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("  === Custom tools demo ===")
	if err := ask(ctx, agent, "What is the SKU for apples? We need to order more."); err != nil {
		return err
	}

	fmt.Println("\n  === Stateful tool (fruit counter) demo ===")
	for _, turn := range []string{
		"I have 5 apples.",
		"And I just got 3 bananas.",
		"Oh, and another 2 apples.",
	} {
		if err := ask(ctx, agent, turn); err != nil {
			return err
		}
	}

	fmt.Println("\n  Final tally:", inv.counts)
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
