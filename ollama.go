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

package antigravity

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultOllamaHost is where Ollama listens unless OLLAMA_HOST says otherwise.
const DefaultOllamaHost = "http://localhost:11434"

// WithOllama runs the agent against a model served by Ollama on this machine.
//
// The address comes from OLLAMA_HOST — either as set by [WithEnv] or, failing
// that, from the process environment — and defaults to [DefaultOllamaHost].
// A bare "host:port" is accepted, as Ollama's own CLI accepts one.
//
// [New] checks the server before starting anything: it must be running and it
// must already have the model. Both are ordinary mistakes with a one-line fix,
// and finding out at construction beats finding out mid-turn.
//
// Like [WithOpenAIEndpoint], this suppresses the Gemini defaults, so no Gemini
// credentials are needed. Image generation is not available: Ollama serves the
// text model and nothing fills that modality.
//
//	agent, err := antigravity.New(ctx, antigravity.WithOllama("qwen3"))
func WithOllama(model string) Option {
	return func(c *config) { c.ollamaModel = model }
}

// resolveOllama turns the shorthand into a target and queues the preflight.
//
// It runs during resolve, so it sees the environment [WithEnv] configured no
// matter what order the options were given in.
func (c *config) resolveOllama() (ModelTarget, error) {
	if c.ollamaModel == "" {
		return ModelTarget{}, fmt.Errorf("WithOllama needs a model name, such as \"qwen3\"")
	}
	host := ollamaHost(c.env)
	if err := validOllamaHost(host); err != nil {
		return ModelTarget{}, err
	}

	model := c.ollamaModel
	c.preflight = append(c.preflight, func(ctx context.Context) error {
		return checkOllama(ctx, host, model)
	})

	return ModelTarget{
		Name:     model,
		Types:    []ModelType{ModelTypeText},
		Endpoint: &GemmaEndpoint{BaseURL: ollamaBaseURL(host)},
	}, nil
}

// ollamaPreflightTimeout bounds the readiness check. It is a request to a
// process on the same machine that either answers immediately or is not
// running, so the only thing this really guards against is a host that
// accepts the connection and then says nothing.
const ollamaPreflightTimeout = 5 * time.Second

// ollamaHost returns the server root, without a trailing slash.
//
// OLLAMA_HOST is Ollama's own variable and its own format: the CLI accepts a
// bare "host:port" as readily as a URL, so a value copied from a shell profile
// often has no scheme. Supplying one is the difference between working and a
// baffling parse error, and http is right because the value addresses a local
// process.
func ollamaHost(env map[string]string) string {
	raw := env["OLLAMA_HOST"]
	if raw == "" {
		raw = os.Getenv("OLLAMA_HOST")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultOllamaHost
	}
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, ":") {
			// A bare ":11434" means the default host on that port, matching
			// how Ollama itself reads the variable.
			raw = "localhost" + raw
		}
		raw = "http://" + raw
	}
	return strings.TrimSuffix(raw, "/")
}

// ollamaTag is one entry of an /api/tags response. Ollama reports a model
// under both keys; older versions populated only name.
type ollamaTag struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// matches reports whether the tag is the model the caller asked for.
//
// Ollama resolves a bare name to its :latest tag, so "qwen3" and
// "qwen3:latest" name the same model and a caller may reasonably write either.
// Nothing looser than that: "qwen3" must not match "qwen3:0.6b", which is a
// different model with different behavior.
func (t ollamaTag) matches(model string) bool {
	for _, have := range []string{t.Name, t.Model} {
		if have == "" {
			continue
		}
		if have == model || have == model+":latest" || strings.TrimSuffix(have, ":latest") == model {
			return true
		}
	}
	return false
}

// checkOllama reports whether the server is reachable and the model is pulled.
//
// Both failures are ordinary and both have a one-line remedy, which is the
// whole reason this runs: without it the first symptom is an error from deep
// inside the harness, mid-turn, after the agent has already started.
func checkOllama(ctx context.Context, host, model string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaPreflightTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/api/tags", nil)
	if err != nil {
		return fmt.Errorf("could not address the Ollama server at %s: %w", host, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("no Ollama server at %s: is `ollama serve` running? (%w)", host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the Ollama server at %s answered %s for /api/tags", host, resp.Status)
	}

	var body struct {
		Models []ollamaTag `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// Something is listening on Ollama's port but is not Ollama. Say so,
		// rather than reporting the model as missing.
		return fmt.Errorf("the server at %s did not answer /api/tags like Ollama: %w", host, err)
	}

	for _, tag := range body.Models {
		if tag.matches(model) {
			return nil
		}
	}
	return fmt.Errorf("the Ollama server at %s has no model %q; run: ollama pull %s%s",
		host, model, model, availableModels(body.Models))
}

// maxListedModels bounds what a missing-model error names, so a well-stocked
// server does not answer a typo with a wall of text.
const maxListedModels = 8

// availableModels renders what the server does have, so a typo is obvious
// without a second command.
func availableModels(tags []ollamaTag) string {
	names := make([]string, 0, len(tags))
	for _, t := range tags {
		if name := cmp.Or(t.Name, t.Model); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return " (it has no models pulled)"
	}
	if extra := len(names) - maxListedModels; extra > 0 {
		names = append(names[:maxListedModels:maxListedModels],
			fmt.Sprintf("and %d more", extra))
	}
	return " (it has: " + strings.Join(names, ", ") + ")"
}

// ollamaBaseURL turns a server root into the OpenAI-compatible API root.
func ollamaBaseURL(host string) string {
	return host + "/v1"
}

// validOllamaHost reports whether the resolved host is dialable, so a
// malformed OLLAMA_HOST is named as such rather than surfacing as a failed
// connection to a nonsense address.
func validOllamaHost(host string) error {
	u, err := url.Parse(host)
	if err != nil {
		return fmt.Errorf("OLLAMA_HOST %q is not a valid URL: %w", host, err)
	}
	if u.Host == "" {
		return fmt.Errorf("OLLAMA_HOST %q names no host", host)
	}
	return nil
}
