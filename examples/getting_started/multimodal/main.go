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

// Command multimodal sends images, documents, and audio to the agent, asks it
// to generate an image, and has a custom tool hand it one back.
//
// Media travels as [antigravity.Content] alongside text in the same prompt, and
// a tool's return value is scanned for media so attachments reach the model as
// attachments rather than as base64 buried in a JSON string.
//
// Run from the repository root, so the relative resource paths resolve:
//
//	go run ./examples/getting_started/multimodal
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

const resourcesDir = "examples/resources"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	imagePath := filepath.Join(resourcesDir, "example_image.png")

	// FromFile sniffs the MIME type from the extension and picks the matching
	// media kind, so one call covers all three inputs below.
	inputs := []struct {
		label       string
		path        string
		description string
		prompt      string
	}{
		{"image", imagePath, "an example image", "What is in this image?"},
		{"document", filepath.Join(resourcesDir, "sample_doc.txt"), "a sample document", "Summarize this document"},
		{"audio", filepath.Join(resourcesDir, "sample_audio.wav"), "a sample audio clip", "Transcribe or describe this audio clip"},
	}
	for _, in := range inputs {
		fmt.Printf("  --- Multimodal input: %s ---\n", in.label)
		media, err := antigravity.FromFile(in.path, in.description)
		if err != nil {
			return err
		}
		if err := oneShot(ctx, in.prompt, []antigravity.Option{},
			antigravity.Text(in.prompt), media); err != nil {
			return err
		}
	}

	// Image generation is off unless generate_image is enabled. Enabling a tool
	// explicitly also disables every other builtin, which is what makes this
	// agent single-purpose.
	fmt.Println("  --- Multimodal output: image generation ---")
	const genPrompt = "Generate an image of a futuristic city with a 16:9 " +
		"aspect ratio, name it 'future_city'. Please provide the file path to " +
		"the generated image."
	err := oneShot(ctx, genPrompt, []antigravity.Option{
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnabledTools: []antigravity.BuiltinTool{antigravity.ToolGenerateImage},
		}),
	}, antigravity.Text(genPrompt))
	if err != nil {
		return err
	}

	// A tool can return media too. The SDK lifts it out of the result and
	// delivers it as a real attachment, so the model sees the image within the
	// same turn instead of needing a follow-up.
	fmt.Println("  --- Multimodal tool output: a tool returns an image ---")
	const toolPrompt = "Call load_example_image, then describe what is in the image."
	return oneShot(ctx, toolPrompt, []antigravity.Option{
		antigravity.WithTools(antigravity.MustNewTool("load_example_image",
			"Loads the example image so you can see it.", loadExampleImage(imagePath))),
	}, antigravity.Text(toolPrompt))
}

// imageResult is a tool return value that mixes text and media.
type imageResult struct {
	Note  string             `json:"note"`
	Image *antigravity.Image `json:"image"`
}

func loadExampleImage(path string) func(context.Context, struct{}) (imageResult, error) {
	return func(_ context.Context, _ struct{}) (imageResult, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return imageResult{}, err
		}
		image, err := antigravity.NewImage(data, "image/png", "the example image")
		if err != nil {
			return imageResult{}, err
		}
		return imageResult{Note: "Here is the requested image.", Image: image}, nil
	}
}

// oneShot runs a single prompt against a fresh agent, since each part of this
// example wants a different configuration.
func oneShot(ctx context.Context, label string, opts []antigravity.Option, prompt ...antigravity.Content) error {
	agent, err := antigravity.New(ctx, opts...)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Println("  User:", label)
	resp, err := agent.Chat(ctx, prompt...)
	if err != nil {
		return err
	}
	text, err := resp.Wait()
	if err != nil {
		return err
	}
	fmt.Printf("  Agent: %s\n\n", text)
	return nil
}
