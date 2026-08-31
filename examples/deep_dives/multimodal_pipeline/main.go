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

// Command multimodal_pipeline runs an image through two agents that share
// nothing.
//
// The generator draws a picture from a written prompt. The discriminator is a
// separate agent, with its own conversation and no access to the first one's
// history, and it receives only raw bytes — no filename, no caption, no hint
// of what was asked for. Whatever it says about the image therefore comes from
// looking at the pixels, which is what makes this a real test of multimodal
// input rather than a game of telephone.
//
//	go run ./examples/deep_dives/multimodal_pipeline
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

const imageName = "birman_birthday"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Pointing the app data directory at a temporary tree keeps the generated
	// image somewhere known, instead of somewhere under the user's home.
	appData, err := os.MkdirTemp("", "multimodal_pipeline_")
	if err != nil {
		return err
	}
	defer os.RemoveAll(appData)

	if err := generate(ctx, appData); err != nil {
		return err
	}
	return discriminate(ctx, appData)
}

// generate asks the first agent for a picture.
func generate(ctx context.Context, appData string) error {
	header("Phase 1: generator, creating the image")

	agent, err := antigravity.New(ctx,
		antigravity.WithAppDataDir(appData),
		antigravity.WithSystemPrompt("You are an image generation assistant. "+
			"When asked to generate an image, use the 'generate_image' tool. "+
			"Once the image exists, give the user its name and a one-line "+
			"confirmation. Do not describe the image."),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnabledTools: []antigravity.BuiltinTool{antigravity.ToolGenerateImage},
		}),
		antigravity.WithPolicies(antigravity.One(
			antigravity.AllowTool(antigravity.ToolGenerateImage, antigravity.Named("allow-gen")))),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	prompt := fmt.Sprintf("Generate an image of a white and orange Birman cat "+
		"sitting in front of a fish-shaped birthday cake with lit candles. "+
		"Name it %q.", imageName)
	fmt.Printf(">>> %s\n\n", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}
	return stream(resp)
}

// discriminate hands the bytes to a fresh agent and asks what they show.
func discriminate(ctx context.Context, appData string) error {
	header("Phase 2: discriminator, describing the image")

	path, err := findImage(appData, imageName)
	if err != nil {
		return err
	}
	if path == "" {
		// Generation can be rate limited or unavailable, and there is nothing
		// to describe if it was.
		fmt.Println("  No generated image found on disk; skipping phase 2.")
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("  Found image: %s\n  Size: %d bytes\n", path, info.Size())

	// FromFile reads the bytes and infers the MIME type. The description is
	// what the model is told the attachment is, so it stays generic here — a
	// caption would give the game away.
	image, err := antigravity.FromFile(path, "an image")
	if err != nil {
		return err
	}

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt("You are a visual analysis assistant. You "+
			"will receive an image with no prior context. Describe exactly what "+
			"you see: subject matter, colors, lighting, mood, and any notable "+
			"details. Be specific and vivid."),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	fmt.Print(">>> Sending the raw image to a fresh agent...\n\n")

	resp, err := agent.Chat(ctx,
		antigravity.Text("What do you see in this image? Describe it in detail."),
		image,
	)
	if err != nil {
		return err
	}
	return stream(resp)
}

// findImage walks the app data tree for the newest image whose name starts
// with the requested one. The generate_image tool appends a timestamp and an
// extension, and writes into a conversation-specific brain directory, so
// neither the exact name nor the exact location is known up front.
func findImage(root, name string) (string, error) {
	var newest string
	var newestMod time.Time

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), name) {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".jpeg":
		default:
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = path, info.ModTime()
		}
		return nil
	})
	return newest, err
}

// stream prints text as it arrives and announces each tool call.
func stream(resp *antigravity.ChatResponse) error {
	for chunk, err := range resp.Chunks() {
		if err != nil {
			return err
		}
		switch c := chunk.(type) {
		case antigravity.TextChunk:
			fmt.Print(c.Text)
		case antigravity.ToolCall:
			fmt.Printf("\n  [tool] %s(%s)\n", c.Name, c.Args)
		}
	}
	fmt.Println()
	return nil
}

func header(title string) {
	bar := strings.Repeat("=", 60)
	fmt.Printf("\n%s\n  %s\n%s\n", bar, title, bar)
}
