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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGemmaEndpointValidate(t *testing.T) {
	if err := (&GemmaEndpoint{BaseURL: "http://localhost:11434/v1"}).Validate(); err != nil {
		t.Errorf("a local OpenAI-compatible URL was rejected: %v", err)
	}
	// No credentials are consulted, so this holds whatever the shell says.
	t.Setenv("GEMINI_API_KEY", "")
	if err := (&GemmaEndpoint{BaseURL: "https://models.example/v1"}).Validate(); err != nil {
		t.Errorf("a Gemma endpoint asked for Gemini credentials: %v", err)
	}

	for name, url := range map[string]string{
		"empty":     "",
		"no scheme": "localhost:11434/v1",
		"not http":  "ftp://localhost/v1",
		"no host":   "http:///v1",
	} {
		t.Run(name, func(t *testing.T) {
			if err := (&GemmaEndpoint{BaseURL: url}).Validate(); err == nil {
				t.Errorf("base URL %q was accepted", url)
			}
		})
	}
}

func TestWithOpenAIEndpointSuppressesTheGeminiDefaults(t *testing.T) {
	// A caller who wants a model on their own machine must not be asked for a
	// Gemini API key by the image-model default.
	t.Setenv("GEMINI_API_KEY", "")
	c := mustResolve(t, WithOpenAIEndpoint("http://localhost:1234/v1", "local-model"))

	if len(c.models) != 1 {
		t.Fatalf("got %d models, want only the configured one: %+v", len(c.models), c.models)
	}
	if c.models[0].Name != "local-model" {
		t.Errorf("model = %q, want the configured one", c.models[0].Name)
	}
	gemma, ok := c.models[0].Endpoint.(*GemmaEndpoint)
	if !ok {
		t.Fatalf("endpoint is %T, want *GemmaEndpoint", c.models[0].Endpoint)
	}
	if gemma.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("base URL = %q, want the configured one", gemma.BaseURL)
	}
}

func TestGeminiDefaultsSurviveAMixedConfiguration(t *testing.T) {
	// Only a wholly local configuration suppresses them: naming a Gemini model
	// too means the caller does want the usual fallbacks.
	c := mustResolve(t,
		WithOpenAIEndpoint("http://localhost:1234/v1", "local-model"),
		WithModel("gemini-something"),
	)

	var image []string
	for _, m := range c.models {
		for _, kind := range m.modelTypes() {
			if kind == ModelTypeImage {
				image = append(image, m.Name)
			}
		}
	}
	if len(image) != 1 || image[0] != DefaultImageGenerationModel {
		t.Errorf("image models = %v, want the package default", image)
	}
}

func TestOllamaHost(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"unset":            {nil, DefaultOllamaHost},
		"blank":            {map[string]string{"OLLAMA_HOST": "  "}, DefaultOllamaHost},
		"full URL":         {map[string]string{"OLLAMA_HOST": "https://ollama.example"}, "https://ollama.example"},
		"trailing slash":   {map[string]string{"OLLAMA_HOST": "http://box:11434/"}, "http://box:11434"},
		"bare host:port":   {map[string]string{"OLLAMA_HOST": "box:11434"}, "http://box:11434"},
		"port only":        {map[string]string{"OLLAMA_HOST": ":9999"}, "http://localhost:9999"},
		"host with a path": {map[string]string{"OLLAMA_HOST": "http://box/ollama"}, "http://box/ollama"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ollamaHost(tc.env); got != tc.want {
				t.Errorf("ollamaHost(%v) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestOllamaHostPrefersTheConfiguredEnvironment(t *testing.T) {
	// The harness runs with the environment WithEnv describes, so that is the
	// one that decides where Ollama is — not this process's.
	t.Setenv("OLLAMA_HOST", "http://ambient:11434")
	if got := ollamaHost(map[string]string{"OLLAMA_HOST": "http://configured:11434"}); got != "http://configured:11434" {
		t.Errorf("host = %q, want the configured one", got)
	}
	if got := ollamaHost(map[string]string{"OTHER": "x"}); got != "http://ambient:11434" {
		t.Errorf("host = %q, want the ambient one", got)
	}
}

// fakeOllama serves /api/tags with the models named, as Ollama does.
func fakeOllama(t *testing.T, models ...string) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		entries := make([]string, 0, len(models))
		for _, m := range models {
			entries = append(entries, `{"name":"`+m+`","model":"`+m+`"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[` + strings.Join(entries, ",") + `]}`))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func TestCheckOllamaAcceptsAPulledModel(t *testing.T) {
	host := fakeOllama(t, "qwen3:latest", "llama3.2:1b")

	// A bare name and its :latest tag are the same model, and a caller may
	// reasonably write either.
	for _, model := range []string{"qwen3", "qwen3:latest", "llama3.2:1b"} {
		if err := checkOllama(t.Context(), host, model); err != nil {
			t.Errorf("checkOllama(%q): %v", model, err)
		}
	}
}

func TestCheckOllamaReportsAMissingModel(t *testing.T) {
	host := fakeOllama(t, "qwen3:latest")

	err := checkOllama(t.Context(), host, "llama3.2")
	if err == nil {
		t.Fatal("a model the server does not have was accepted")
	}
	// The remedy and the models it does have both belong in the message.
	for _, want := range []string{`"llama3.2"`, "ollama pull llama3.2", "qwen3:latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCheckOllamaDoesNotMatchADifferentTag(t *testing.T) {
	// qwen3:0.6b is a different model from qwen3, not an alias for it.
	host := fakeOllama(t, "qwen3:0.6b")
	if err := checkOllama(t.Context(), host, "qwen3"); err == nil {
		t.Error("a differently tagged model satisfied the check")
	}
}

func TestCheckOllamaReportsAnAbsentServer(t *testing.T) {
	server := httptest.NewServer(http.NewServeMux())
	host := server.URL
	server.Close() // Nothing is listening now.

	err := checkOllama(t.Context(), host, "qwen3")
	if err == nil {
		t.Fatal("a stopped server was accepted")
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("error %q does not say how to start the server", err)
	}
}

func TestCheckOllamaReportsSomethingElseOnThePort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from a web server"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	err := checkOllama(t.Context(), server.URL, "qwen3")
	if err == nil {
		t.Fatal("a server that is not Ollama was accepted")
	}
	// Reporting this as a missing model would send the caller off to pull one.
	if strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("error %q blames the model, not the server", err)
	}
}

func TestCheckOllamaReportsAnErrorStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if err := checkOllama(t.Context(), server.URL, "qwen3"); err == nil {
		t.Error("a failing server was accepted")
	}
}

func TestWithOllamaConfiguresOneLocalModel(t *testing.T) {
	host := fakeOllama(t, "qwen3:latest")
	t.Setenv("GEMINI_API_KEY", "")

	c := mustResolve(t, WithEnv(map[string]string{"OLLAMA_HOST": host}), WithOllama("qwen3"))

	if len(c.models) != 1 {
		t.Fatalf("got %d models, want only Ollama's: %+v", len(c.models), c.models)
	}
	if c.models[0].Name != "qwen3" {
		t.Errorf("model = %q, want %q", c.models[0].Name, "qwen3")
	}
	gemma, ok := c.models[0].Endpoint.(*GemmaEndpoint)
	if !ok {
		t.Fatalf("endpoint is %T, want *GemmaEndpoint", c.models[0].Endpoint)
	}
	if want := host + "/v1"; gemma.BaseURL != want {
		t.Errorf("base URL = %q, want %q", gemma.BaseURL, want)
	}

	// resolve stays offline; the check it queued is what talks to the server.
	if len(c.preflight) != 1 {
		t.Fatalf("got %d preflight checks, want 1", len(c.preflight))
	}
	if err := c.preflight[0](context.Background()); err != nil {
		t.Errorf("preflight: %v", err)
	}
}

func TestWithOllamaRejectsAMalformedHost(t *testing.T) {
	_, err := resolved(t, WithEnv(map[string]string{"OLLAMA_HOST": "http://"}), WithOllama("qwen3"))
	if err == nil {
		t.Fatal("a host with no address was accepted")
	}
	assertConfigField(t, err, "models")
}

func TestWithOllamaNeedsAModelName(t *testing.T) {
	// An empty name is indistinguishable from not calling WithOllama at all,
	// so resolve falls back to the usual Gemini defaults rather than
	// configuring a nameless local model.
	c := mustResolve(t, WithOllama(""))
	for _, m := range c.models {
		if _, ok := m.Endpoint.(*GemmaEndpoint); ok {
			t.Errorf("model %q was pointed at Ollama", m.Name)
		}
	}
	if _, err := c.resolveOllama(); err == nil {
		t.Error("resolveOllama accepted an empty model name")
	}
}
