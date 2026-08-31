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

// Command observability_otel traces an agent session with OpenTelemetry.
//
// Tracing lives in its own module, github.com/go-steer/antigravity-sdk-go/tracing,
// so that the SDK proper stays free of the OpenTelemetry dependency tree. That
// is why this example has its own go.mod: importing the tracing module from
// the SDK's own module would push those dependencies onto everyone.
//
// tracing.Options returns ordinary SDK options, so instrumenting a session is
// one line at construction. The spans it emits form a tree that mirrors what
// the agent actually did — turn, step, tool call, subagent — which is the
// point: a subagent's work appears nested under the call that spawned it,
// rather than as unrelated activity somewhere else in the log.
//
// The exporter here prints to stdout, which is verbose and meant for reading.
// A real deployment would swap in OTLP and change nothing else.
//
// Run from this directory:
//
//	go run .
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	antigravity "github.com/go-steer/antigravity-sdk-go"
	"github.com/go-steer/antigravity-sdk-go/tracing"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

type weatherArgs struct {
	Location string `json:"location" jsonschema:"description=The city to report on."`
}

func getWeather(_ context.Context, args weatherArgs) (string, error) {
	return fmt.Sprintf("The weather in %s is sunny.", args.Location), nil
}

func run(ctx context.Context) error {
	shutdown, err := setupTracing()
	if err != nil {
		return err
	}
	// Spans are buffered until the provider is shut down, so a missed
	// shutdown looks exactly like a session that produced no traces.
	defer shutdown(ctx)

	if err := traceBasicAgent(ctx); err != nil {
		return err
	}
	return traceSubagents(ctx)
}

// setupTracing installs a global tracer provider that prints to stdout.
func setupTracing() (func(context.Context) error, error) {
	exporter, err := stdouttrace.New(
		stdouttrace.WithWriter(os.Stdout),
		stdouttrace.WithPrettyPrint(),
	)
	if err != nil {
		return nil, err
	}

	// A simple processor exports each span the moment it ends, which keeps
	// the output in step with the narration. Batch it in production.
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)),
	)
	otel.SetTracerProvider(provider)

	return provider.Shutdown, nil
}

// traceBasicAgent traces a single turn that calls one custom tool.
func traceBasicAgent(ctx context.Context) error {
	fmt.Println("\n--- Basic agent tracing ---")

	opts := []antigravity.Option{
		antigravity.WithTools(antigravity.MustNewTool("get_weather",
			"Gets the current weather for a location.", getWeather)),
	}
	// tracing.Options registers the observer and hooks that build the span
	// tree. Each hook costs a round trip to the harness, so a traced session
	// is chattier than an untraced one.
	opts = append(opts, tracing.Options(tracing.WithAgentName("weather-agent"))...)

	agent, err := antigravity.New(ctx, opts...)
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "What is the weather in Paris?")
}

// traceSubagents traces a delegation, which is where the span tree earns its
// keep: the poet's steps hang off the start_subagent span.
func traceSubagents(ctx context.Context) error {
	fmt.Println("\n--- Subagent tracing ---")

	opts := []antigravity.Option{
		antigravity.WithSystemPrompt("You are a poet manager. Delegate the " +
			"poem writing to a specialized 'Poet' subagent."),
		antigravity.WithCapabilities(antigravity.CapabilitiesConfig{
			EnableSubagents: true,
		}),
	}
	opts = append(opts, tracing.Options(tracing.WithAgentName("poet-manager"))...)

	agent, err := antigravity.New(ctx, opts...)
	if err != nil {
		return err
	}
	defer agent.Close()

	return ask(ctx, agent, "Write a 4-line poem about space.")
}

func ask(ctx context.Context, agent *antigravity.Agent, prompt string) error {
	fmt.Println("  User:", prompt)

	resp, err := agent.Chat(ctx, antigravity.Text(prompt))
	if err != nil {
		return err
	}

	fmt.Print("  Agent: ")
	for text, err := range resp.Text() {
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		fmt.Print(text)
	}
	fmt.Println()
	return nil
}
