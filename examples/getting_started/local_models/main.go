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

// Command local_models runs the agent against a model on your own machine, with
// no API key and nothing leaving the host.
//
//	ollama serve
//	ollama pull qwen3
//	go run ./examples/getting_started/local_models
//
// Any server speaking the OpenAI completions API works; Ollama just gets a
// shorthand. Pass -base-url to use a different one — LM Studio, llama.cpp's
// server, vLLM — and -model to name the model it serves.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	baseURL := flag.String("base-url", "",
		"an OpenAI-compatible API root, such as http://localhost:1234/v1; empty means use Ollama")
	model := flag.String("model", "qwen3", "the model the server should run")
	flag.Parse()

	if err := run(context.Background(), *baseURL, *model); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, baseURL, model string) error {
	// WithOllama finds the server at OLLAMA_HOST, or localhost:11434, and
	// checks before we go any further that it is running and has the model.
	// WithOpenAIEndpoint is the same thing without the guesswork: you supply
	// the address, and nothing is contacted until the first turn.
	endpoint := antigravity.WithOllama(model)
	if baseURL != "" {
		endpoint = antigravity.WithOpenAIEndpoint(baseURL, model)
	}

	// A local model is the only model here, so there are no Gemini credentials
	// to supply and no image generation available. Naming a Gemini model
	// alongside this one would bring both back.
	agent, err := antigravity.New(ctx, endpoint)
	if err != nil {
		// A local server that is not running, or a model that was never
		// pulled, arrives as a configuration error saying which.
		var cfgErr *antigravity.ConfigError
		if errors.As(err, &cfgErr) {
			return fmt.Errorf("this example needs a model server: %w", err)
		}
		return err
	}
	defer agent.Close()

	const prompt = "In one sentence: what is the advantage of running a model locally?"
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
