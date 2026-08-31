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

// Command prioritized_inference routes requests to Gemini's priority tier and
// inspects the usage metadata to see which tier actually served them.
//
// Priority traffic gets high-criticality compute: predictable second-level
// latency and strict non-sheddable reliability. When priority traffic exceeds
// its dynamic rate limit, requests are gracefully downgraded to the standard
// tier instead of failing with HTTP 429 or 503, and are billed at standard
// rates. Priority requests otherwise cost more than standard ones. See
// https://ai.google.dev/gemini-api/docs/priority-inference
//
//	go run ./examples/getting_started/prioritized_inference
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
	// The tier is a property of the endpoint, so the model target has to be
	// spelled out rather than left to WithModel. Leaving Name empty takes
	// antigravity.DefaultModel, tracking whatever the SDK default becomes.
	//
	// Only the text model is configured here, so the image model the SDK adds
	// alongside it runs on a plain endpoint at the standard tier.
	agent, err := antigravity.New(ctx, antigravity.WithModels(antigravity.ModelTarget{
		Endpoint: &antigravity.GeminiAPIEndpoint{
			Options: &antigravity.GeminiModelOptions{
				ServiceTier: antigravity.ServiceTierPriority,
			},
		},
	}))
	if err != nil {
		return err
	}
	defer agent.Close()

	const prompt = "Explain quantum computing in one sentence."
	fmt.Println("Sending prompt on the priority tier:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Println("Agent:", text)

	// The served tier is reported per turn, so a downgrade is observable.
	usage := resp.Usage()
	switch {
	case usage == nil || usage.ServiceTier == "":
		fmt.Println("Served service tier: unspecified / not returned")
	case usage.ServiceTier == antigravity.ServiceTierStandard:
		fmt.Println("Served service tier:", usage.ServiceTier)
		fmt.Println("Notice: downgraded from priority to standard tier by dynamic rate limiting.")
	case usage.ServiceTier == antigravity.ServiceTierPriority:
		fmt.Println("Served service tier:", usage.ServiceTier)
		fmt.Println("Success: served on the high-criticality priority tier.")
	default:
		fmt.Println("Served service tier:", usage.ServiceTier)
	}

	return nil
}
