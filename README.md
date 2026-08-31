# Antigravity SDK for Go

A Go SDK for building AI agents powered by Antigravity and Gemini. It is a port
of the [Google Antigravity SDK for Python](https://github.com/Google-Antigravity/antigravity-sdk-python),
with the same behavior and a Go-shaped API.

The SDK is a stateful infrastructure layer over the agentic loop: you say what
the agent may do and what it is for, and the loop — model calls, tool dispatch,
subagents, compaction — runs behind an interface that is a handful of methods
wide.

```go
agent, err := antigravity.New(ctx,
	antigravity.WithSystemPrompt("You are a careful code reviewer."),
)
if err != nil {
	return err
}
defer agent.Close()

resp, err := agent.Chat(ctx, antigravity.Text("What does main.go do?"))
if err != nil {
	return err
}
text, err := resp.Wait()
```

## Installation

```sh
go get github.com/go-steer/antigravity-sdk-go
```

> [!IMPORTANT]
> The SDK does not implement the agentic loop. That lives in a compiled binary
> called `localharness`, which the SDK launches and drives over a local
> WebSocket. **The module alone is not enough to run an agent.** The Python
> SDK ships the binary inside its platform-specific PyPI wheels; obtain it from
> there, or from your own distribution, and make it discoverable:
>
> ```sh
> export ANTIGRAVITY_HARNESS_PATH=/path/to/localharness
> ```
>
> Failing that, the SDK looks for `localharness` on `PATH`, or you can point at
> it with `antigravity.WithBinaryPath`. Without it, `New` returns
> `ErrHarnessNotFound`.

## Authentication

For the Gemini Developer API, set a key in the environment:

```sh
export GEMINI_API_KEY="your-api-key"
```

or pass it explicitly with `antigravity.WithAPIKey("…")`, which takes
precedence.

For the Gemini Enterprise Agent Platform (formerly Vertex AI), use Application
Default Credentials:

```sh
gcloud auth application-default login
```

```go
agent, err := antigravity.New(ctx,
	antigravity.WithVertex("your-gcp-project", "us-central1"))
```

Empty arguments fall back to `GOOGLE_CLOUD_PROJECT` and
`GOOGLE_CLOUD_LOCATION`. Explicit options always beat environment variables.

## Quickstart

```sh
export GEMINI_API_KEY="your-api-key"
go run ./examples/getting_started/hello_world
```

Then browse [`examples/`](examples/): twenty-three
[getting started](examples/getting_started/) programs, one feature each, and
nine [deep dives](examples/deep_dives/) that combine them into applications.

## The API

### Agent and conversation

`New` starts a session; `Close` ends it. One `Agent` owns one `Conversation`,
which holds the history and is reachable through `agent.Conversation()` when
you need more than a plain exchange — streaming steps, usage totals, or
cancelling a turn from elsewhere.

Every blocking call takes a `context.Context`, and cancelling it ends the turn.

### Streaming

`Chat` returns as soon as the turn starts. The response is a set of iterators,
so ordinary `range` works:

```go
resp, err := agent.Chat(ctx, antigravity.Text("Write a short poem about space."))
if err != nil {
	return err
}
for text, err := range resp.Text() {
	if err != nil {
		return err
	}
	fmt.Print(text)
}
```

`resp.Thoughts()` streams the model's reasoning, `resp.ToolCalls()` the calls it
dispatches, and `resp.Chunks()` everything interleaved in arrival order.
`resp.Wait()` collects the text and blocks until the turn ends.

See [streaming](examples/getting_started/streaming/main.go).

### Custom tools

A tool is a Go function. Its parameter struct becomes the schema the model
sees, with names from `json` tags and descriptions from `jsonschema` tags:

```go
type weatherArgs struct {
	City string `json:"city" jsonschema:"description=The city to report on."`
}

func getWeather(ctx context.Context, args weatherArgs) (string, error) {
	return "It's sunny in " + args.City + ".", nil
}

agent, err := antigravity.New(ctx,
	antigravity.WithTools(antigravity.MustNewTool("get_weather",
		"Returns the current weather for a city.", getWeather)))
```

There is no per-call context object: a stateful tool is a method on a struct
you own, which also makes the locking explicit. See
[custom_tools](examples/getting_started/custom_tools/main.go).

### Multimodal input

```go
image, err := antigravity.FromFile("diagram.png", "Architecture blueprint")
if err != nil {
	return err
}

resp, err := agent.Chat(ctx,
	antigravity.Text("What does this diagram get wrong?"),
	image,
)
```

`FromFile` infers the kind and MIME type; `NewImage`, `NewDocument`, `NewAudio`,
and `NewVideo` take bytes you already hold. See
[multimodal](examples/getting_started/multimodal/main.go).

### Structured output

```go
type Summary struct {
	Title       string   `json:"title"`
	ActionItems []string `json:"action_items"`
}

agent, err := antigravity.New(ctx, antigravity.WithResponseSchema[Summary]())
```

The schema is derived from the type, and `resp.StructuredOutput()` returns JSON
you can unmarshal back into it. See
[structured_output](examples/getting_started/structured_output/main.go).

### Policies

Policies decide what a tool call is allowed to do, evaluated per call:

```go
agent, err := antigravity.New(ctx,
	antigravity.WithPolicies(antigravity.One(
		antigravity.DenyAll(),
		antigravity.AllowTool(antigravity.ToolViewFile),
		antigravity.AskUserTool(antigravity.ToolRunCommand, antigravity.ConfirmInTerminal()),
	)))
```

Specific rules beat the catch-all regardless of order. `When(pred)` narrows a
rule to calls whose arguments satisfy a predicate, which is how you express
"may edit Markdown, but only under this directory". See
[policies](examples/getting_started/policies/main.go).

### Hooks

Hooks observe and steer the session: session start and end, before and after
each turn, before and after each tool call, on a tool error, on compaction, on
an interaction request, and on stop. A pre-tool hook can deny a call or rewrite
its arguments; a stop hook can send the agent back for another pass.

```go
agent, err := antigravity.New(ctx,
	antigravity.WithPreToolCallHook(func(ctx context.Context, hc *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
		log.Println("calling", call.Name)
		return antigravity.ToolDecision{}, nil
	}))
```

See [hooks](examples/getting_started/hooks/main.go) for all of them, and
[agent_middleware](examples/deep_dives/agent_middleware/main.go) for stacking
them into something useful.

### MCP

```go
agent, err := antigravity.New(ctx,
	antigravity.WithMCPServers(
		antigravity.NewMCPStdioServer("my_server", "npx", "my-mcp-server"),
		antigravity.NewMCPHTTPServer("remote", "https://example.com/mcp"),
	))
```

`EnabledTools` and `DisabledTools` filter a server's tools before the model
ever sees them; `AllowMCP` and `DenyMCP` gate them at call time instead. See
[mcp_tools](examples/getting_started/mcp_tools/main.go).

### Triggers

A trigger is a goroutine that can push a message into a live conversation,
which is how an agent reacts to something other than a user:

```go
agent, err := antigravity.New(ctx,
	antigravity.WithNamedTrigger("deploy-watch",
		antigravity.Every(time.Minute, func(ctx context.Context, tc *antigravity.TriggerContext) error {
			return tc.Send(ctx, "Check the deployment status.")
		})))
```

See [triggers](examples/getting_started/triggers/main.go).

### Subagents

Delegation buys a clean context window and a smaller blast radius: a subagent
gets its own instructions and only the tools you grant it.

```go
reviewer := antigravity.SubagentConfig{
	Name:         "code_reviewer",
	Description:  "Audits source files and reports missing doc comments.",
	Instructions: antigravity.CustomInstructions{Text: "You are a code reviewer…"},
	Tools:        []antigravity.Tool{badge},
}

agent, err := antigravity.New(ctx, antigravity.WithSubagents(reviewer))
```

See [subagents](examples/getting_started/subagents/main.go).

### Interactive loop

For a terminal session with no loop to write:

```go
err := antigravity.RunInteractive(ctx,
	antigravity.WithSystemPrompt("You are a helpful pair programmer."))
```

It switches the agent to interactive behavior, answers the agent's questions
from stdin, and turns the default `run_command` denial into a confirmation
prompt. When you want the loop itself, see
[interactive_cli](examples/deep_dives/interactive_cli/main.go).

## Safety defaults

An agent is not read-only by default, and it is not unrestricted either. Every
builtin tool is enabled, and `run_command` is denied by the default
`ConfirmRunCommand` policy. Supply an ask-user handler to turn that denial into
a prompt, or pass `AllowAll()` for a fully autonomous agent with shell access.

File tools are scoped to the configured workspaces, which default to the
process working directory.

Starting a session with write tools or MCP servers enabled, no policies, and no
pre-tool-call hook is an error rather than a silent grant: an agent that can
write needs someone watching it.

## Tracing

OpenTelemetry support lives in a separate module so the SDK's own dependency
graph stays small:

```sh
go get github.com/go-steer/antigravity-sdk-go/tracing
```

```go
opts := []antigravity.Option{antigravity.WithSystemPrompt("…")}
opts = append(opts, tracing.Options(tracing.WithAgentName("my-agent"))...)

agent, err := antigravity.New(ctx, opts...)
```

The spans mirror the agent's structure — turn, step, tool call, subagent — and
follow the OpenTelemetry semantic conventions for generative AI. See
[observability_otel](examples/deep_dives/observability_otel/).

## Layout

```
*.go                  Public API — one package, github.com/go-steer/antigravity-sdk-go
internal/             Harness spawn, transport, event processing, generated protobuf
tracing/              OpenTelemetry integration — a separate module
proto/                Vendored .proto sources
examples/             Runnable programs
```

The whole public API is one package. Go forbids import cycles and these types
are mutually referential, so splitting them across packages would mean either
cycles or an artificial layering; a single import is also less to remember.

## Go version

`go.mod` declares `go 1.24` with `toolchain go1.26.7`. The `go` directive is the
minimum a consumer needs, so it stays low; the toolchain line pins what this
repository builds with, and `GOTOOLCHAIN=auto` fetches it. The `tracing` module
requires `go 1.25`, since the OpenTelemetry API does.

## Development

```sh
dev/tools/ci               # the full presubmit sweep, across all three modules
dev/tools/ci --keep-going  # run everything, collect all failures
```

The root `./...` does not reach `tracing/` or the OTel example, which are
separate modules; the scripts under [`dev/`](dev/) loop over all three. See
[`dev/README.md`](dev/README.md).

Tests run against a fake harness that performs the real handshake and serves
scripted events over a local WebSocket, so no `localharness` binary is needed
to run the suite.

## Documentation

- [`docs/DESIGN.md`](docs/DESIGN.md) — the design of record: the client/harness
  split, the wire protocol, the package layout, and the parity details that
  matter.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — contributor flow, commit conventions,
  and how to change the exported API.
- [`AGENTS.md`](AGENTS.md) — instructions for AI agents working in this
  repository.
- [`CHANGELOG.md`](CHANGELOG.md) — what has changed.

## License

Apache 2.0. See [`LICENSE`](LICENSE).
