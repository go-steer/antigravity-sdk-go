# Deep dives

Programs that combine several features into something closer to an
application. Each is self-contained; start with whichever resembles what you
are building.

> Get the basics running first. See [getting started](../getting_started/) and
> its [README](../getting_started/README.md).

Run these from the repository root, since two of them build the example MCP
server from a relative path.

---

## Middleware and lifecycle

### [agent_middleware](agent_middleware/main.go)

Hooks stacked into a middleware chain. The agent calls tools normally while a
pre-tool hook rate-limits them, a post-tool hook writes an audit trail, and a
tool error hook converts one specific failure into advice the model can act
on. None of it is visible from the prompt, and each piece is testable on its
own.

Covers: `WithPreToolCallHook`, `WithPostToolCallHook`, `WithToolErrorHook`,
`ToolDecision{Deny: true}`, hook state under a mutex.

```sh
go run ./examples/deep_dives/agent_middleware
```

### [host_tool_hooks](host_tool_hooks/main.go)

One of every lifecycle hook, each printing what it received. Useful as a
reference for the shape of the data at each point. It also reads the session
through `Conversation().Send` and `Conversation().Steps` rather than as
response text, which is what lets it show subagent output separately from the
agent's own.

Covers: every `With*Hook` option, `Step.ParentTrajectoryID`, `Step.Depth`,
subagents as `start_subagent` tool calls.

```sh
go run ./examples/deep_dives/host_tool_hooks
```

---

## Multi-agent chat

### [round_based_chat](round_based_chat/main.go)

Three agents debate in synchronized rounds: everyone answers at once, the
round ends when the slowest finishes, and the next round starts from a
transcript they have all seen. A `pass_turn` tool lets an agent choose silence,
which is how the discussion knows to stop, and a timer trigger nudges the
panel to wrap up.

Covers: concurrent `Chat` calls, custom tools as control flow, `Every` and
`WithNamedTrigger`, incremental prompt construction.

```sh
go run ./examples/deep_dives/round_based_chat
```

### [async_chat](async_chat/main.go)

The same three-agent idea with the synchronization removed. Each agent owns a
goroutine and speaks whenever it has something to say, so ordering is emergent.
The wake-up is the standard Go broadcast — a channel closed and replaced on
every post — which composes with `ctx.Done()` in a `select`.

Covers: reactive fan-out without rounds, broadcast channels, bounded
discussions that terminate on their own.

```sh
go run ./examples/deep_dives/async_chat
```

---

## Multimodal

### [multimodal_pipeline](multimodal_pipeline/main.go)

A generator draws an image; a completely separate agent receives only the raw
bytes and describes what it sees. Since the second agent never saw the
generation prompt, its description can only come from the pixels.

Covers: the `generate_image` builtin, `FromFile`, independent agents with
separate conversations, `WithAppDataDir` to find the artifact afterwards.

```sh
go run ./examples/deep_dives/multimodal_pipeline
```

---

## Autonomous agents

### [doc_maintenance_agent](doc_maintenance_agent/main.go)

An agent that reads code and corrects the Markdown describing it. What makes it
safe to leave running is the policy set: `edit_file` is approved only for a
`.md` file inside the target directory, and everything else is denied.

Covers: `AllowTool`/`DenyAll`, `When` predicates over `ToolCall.CanonicalPath`,
`Named` rules, workspace scoping, streaming with `resp.Text()`.

```sh
go run ./examples/deep_dives/doc_maintenance_agent -dir ./docs
```

### [doc_comment_maintenance_agent](doc_comment_maintenance_agent/main.go)

The same shape aimed at Go doc comments, and fenced twice over because editing
source is riskier than editing prose. Capabilities remove the tools that could
do anything other than read and edit; policies then restrict editing to `.go`
files under the target directory.

This is the Go counterpart of the Python SDK's `docstring_maintenance_agent.py`.

Covers: `CapabilitiesConfig.DisabledTools` versus policy denial, and why the
two are not the same mechanism.

```sh
go run ./examples/deep_dives/doc_comment_maintenance_agent -dir ./internal
```

---

## Interactive

### [interactive_cli](interactive_cli/main.go)

A terminal chat client with the loop written out by hand. The SDK's
`RunInteractive` does this in one call and is usually the right answer; this
takes it apart because a real CLI wants control over streaming, telemetry, and
flags. It is also the full-stack example: a custom tool, an MCP server, a
policy that confirms every call at the prompt, and a hook that routes the
agent's questions to the same terminal.

Covers: `NewMCPStdioServer`, `AskUser("*", ConfirmInTerminal())`,
`AnswerQuestionsInTerminal`, `UsageMetadata`, `Conversation().History()`.

```sh
go run ./examples/deep_dives/interactive_cli -show-usage
```

---

## Observability

### [observability_otel](observability_otel/)

OpenTelemetry tracing, producing a span tree that mirrors what the agent did:
turn, step, tool call, and a subagent's work nested under the call that spawned
it.

This example has **its own `go.mod`**. Tracing lives in a separate module
(`github.com/go-steer/antigravity-sdk-go/tracing`) so the SDK does not drag the
OpenTelemetry dependency tree along with it, and importing it from the SDK's
own module would defeat that.

Covers: `tracing.Options`, `tracing.WithAgentName`, wiring a tracer provider,
why the exporter must be shut down.

```sh
cd examples/deep_dives/observability_otel && go run .
```
