# Examples

Runnable programs covering the SDK, from a five-line first turn to
multi-agent applications.

## Prerequisites

```sh
export GEMINI_API_KEY="your-api-key"
export ANTIGRAVITY_HARNESS_PATH=/path/to/localharness   # or put it on PATH
```

The SDK drives the `localharness` binary, which runs the agentic loop. Without
it, every example fails at startup with `ErrHarnessNotFound`. See the top-level
[README](../README.md).

Run everything from the repository root:

```sh
go run ./examples/getting_started/hello_world
```

Some examples read files from `examples/skills/` or `examples/resources/`, so
the working directory matters.

## Layout

### [`getting_started/`](getting_started/)

**Start here.** Twenty-three programs, one feature each: agents, streaming,
tools, policies, hooks, structured output, skills, MCP, subagents,
persistence, and more. Every one is short enough to read in a sitting.

See the [getting started README](getting_started/README.md) for the index.

### [`deep_dives/`](deep_dives/)

Nine programs that combine features into realistic applications.

| Example | What it demonstrates |
|---|---|
| [interactive_cli](deep_dives/interactive_cli/main.go) | A terminal client with a custom tool, an MCP server, per-call confirmation, and telemetry. |
| [agent_middleware](deep_dives/agent_middleware/main.go) | Hooks as middleware: rate limiting, audit logging, and error recovery the agent never sees. |
| [host_tool_hooks](deep_dives/host_tool_hooks/main.go) | Every lifecycle hook wired at once, reading the session as steps. |
| [round_based_chat](deep_dives/round_based_chat/main.go) | A three-agent panel in synchronized rounds, with triggers and an opt-out tool. |
| [async_chat](deep_dives/async_chat/main.go) | The same panel with no rounds: goroutines, a broadcast channel, emergent ordering. |
| [multimodal_pipeline](deep_dives/multimodal_pipeline/main.go) | Generate an image, then have a blind second agent describe it. |
| [doc_maintenance_agent](deep_dives/doc_maintenance_agent/main.go) | An autonomous documentation agent fenced to `.md` files by policy. |
| [doc_comment_maintenance_agent](deep_dives/doc_comment_maintenance_agent/main.go) | The same for Go doc comments, with destructive tools removed outright. |
| [observability_otel](deep_dives/observability_otel/) | OpenTelemetry tracing. Has its own `go.mod`. |

See the [deep dives README](deep_dives/README.md) for details.

### [`resources/`](resources/)

Shared assets: sample image, document, and audio files, plus
[`piratemath`](resources/piratemath/), a dependency-free MCP server used by the
MCP examples over both stdio and Streamable HTTP.

### [`skills/`](skills/)

A sample `SKILL.md`, loaded by the skills examples.
