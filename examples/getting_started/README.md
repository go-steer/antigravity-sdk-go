# Getting started

One feature per program. Each is a standalone `main` package that you can read
top to bottom in a couple of minutes, and each is runnable on its own:

```sh
go run ./examples/getting_started/hello_world
```

Run them from the repository root. A few read files from `examples/skills/` or
`examples/resources/`, and those paths are resolved relative to the working
directory.

## Before you start

You need two things: an API key, and the `localharness` binary that runs the
agentic loop.

```sh
export GEMINI_API_KEY="your-api-key"
export ANTIGRAVITY_HARNESS_PATH=/path/to/localharness   # or put it on PATH
```

The SDK picks up `GEMINI_API_KEY` from the environment; `antigravity.WithAPIKey`
overrides it in code. See the top-level [README](../../README.md) for the full
setup.

## Your first turn

```go
package main

import (
	"context"
	"fmt"
	"log"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	ctx := context.Background()

	agent, err := antigravity.New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer agent.Close()

	resp, err := agent.Chat(ctx, antigravity.Text("Explain quantum computing in one sentence."))
	if err != nil {
		log.Fatal(err)
	}
	text, err := resp.Wait()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(text)
}
```

That is [hello_world](hello_world/main.go), give or take a comment.

---

## Index

### Core foundations

The building blocks: creating an agent, streaming from it, shaping who it is.

| Example | What it covers |
|---|---|
| [hello_world](hello_world/main.go) | Creating an agent, one turn, `Close`. |
| [streaming](streaming/main.go) | `resp.Text()` and `resp.Thoughts()` as `iter.Seq2`, and what else `Chunks()` carries. |
| [persona_config](persona_config/main.go) | `TemplatedInstructions` for structured identity, `CustomInstructions` when you want the whole prompt. |
| [prioritized_inference](prioritized_inference/main.go) | Asking for the priority service tier, and noticing when the server downgrades you. |

### Safety and governance

Deciding what the agent may do, and how much of it.

| Example | What it covers |
|---|---|
| [policies](policies/main.go) | Deny by default, then allowlist; `When` predicates, `AskUser` handlers, named rules. |
| [budget_limits](budget_limits/main.go) | Caps on model calls, tool calls, and tokens, and reading the resulting `StopReason`. |
| [human_in_the_loop](human_in_the_loop/main.go) | Letting the agent ask a question mid-turn and answering it from the terminal. |
| [autonomous_shell](autonomous_shell/main.go) | `AllowAll`, the deliberate opt-out from the default shell denial. |

### Structured and multimodal

Inputs that are not text, and outputs that are not prose.

| Example | What it covers |
|---|---|
| [structured_output](structured_output/main.go) | `WithResponseSchema[T]` and decoding the result into a Go struct. |
| [multimodal](multimodal/main.go) | Sending images, documents, and audio; generating an image; returning one from a tool. |

### Tools, skills, and delegation

Giving the agent something to do, and someone to hand it to.

| Example | What it covers |
|---|---|
| [custom_tools](custom_tools/main.go) | Go functions as tools, schemas from struct tags, and a stateful tool over a mutex. |
| [agent_skills](agent_skills/main.go) | Loading a `SKILL.md` from disk with `WithSkillsPaths`. |
| [mcp_tools](mcp_tools/main.go) | An MCP server over stdio and over Streamable HTTP, plus two ways to narrow it. |
| [subagents](subagents/main.go) | Self-delegation, a named subagent with its own tools, and a bounded three-tier hierarchy. |
| [web_tools](web_tools/main.go) | The builtin `search_web` and `read_url_content` tools. |
| [slash_commands](slash_commands/main.go) | Sending `/plan` as a prompt part and finding the plan it wrote. |

### Lifecycle, proactivity, and observability

Watching a session, steering it, and keeping it across restarts.

| Example | What it covers |
|---|---|
| [hooks](hooks/main.go) | Every hook the SDK offers, wired at once, including a stop hook that resumes a turn. |
| [triggers](triggers/main.go) | Background work that talks to a live conversation, with `Every` and with a hand-rolled loop. |
| [cancellation](cancellation/main.go) | `resp.Cancel` versus cancelling the context, and telling the two errors apart. |
| [error_handler](error_handler/main.go) | Recovering from a failing tool, and a tour of the SDK's sentinel errors. |
| [observability](observability/main.go) | `slog` output, an audit hook, and reading `Conversation().Usage()`. |
| [persistence](persistence/main.go) | Resuming a conversation by ID from a save directory. |
| [app_data_dir_override](app_data_dir_override/main.go) | Relocating the agent's artifacts with `WithAppDataDir`. |

Ready for something bigger? See the [deep dives](../deep_dives/).
