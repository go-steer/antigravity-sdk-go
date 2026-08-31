# Design

The design of record for `github.com/go-steer/antigravity-sdk-go`. It covers
what the module is, the protocol it speaks, why the package layout is shaped
the way it is, and the behavioral details that a port can get wrong without
failing to compile.

Read this before adding to the exported surface, and keep it current in the
same PR as the change it describes.

---

## 1. What this is

A Go port of the [Google Antigravity SDK for
Python](https://github.com/Google-Antigravity/antigravity-sdk-python), for
building AI agents powered by Antigravity and Gemini.

The port aims for **behavioral parity** with the Python SDK, not API
similarity. The concepts and names track Python; the shapes are Go's. Where the
two disagree about behavior, the Python source is the specification and this
module is wrong — its tests (`*_test.py`) are the most precise statement of
expected behavior in either repository.

### The SDK is a client, not an agent runtime

This is the single most important thing to understand about the module, and the
one most likely to send a contributor looking for code that does not exist.

The agentic loop — model calls, tool dispatch, prompt assembly, compaction,
subagents — is **not implemented here**. It lives in a precompiled Go binary
called `localharness`. This SDK spawns that binary and drives it over a local
WebSocket. There is no inference and no tool-dispatch loop to port; there is
only the client half of a protocol.

Consequences that shape everything else:

- **The binary is not in this repository**, and it is not on any machine that
  has only cloned this repository. The Python SDK ships it inside its
  platform-specific PyPI wheels. Without it, `New` returns
  `ErrHarnessNotFound`.
- **There are no live end-to-end tests.** Everything that touches the transport
  runs against a fake harness (§7).
- **The exported API is almost entirely configuration and observation**:
  describe what the agent may do, hand it a prompt, and read what comes back.

### Binary discovery

In order, first hit wins:

1. `ANTIGRAVITY_HARNESS_PATH` in the caller-supplied env map (`WithEnv`).
2. `ANTIGRAVITY_HARNESS_PATH` in the process environment.
3. The packaged location. Not applicable to Go — there is no wheel equivalent,
   so this step exists in the Python order and is simply absent here.
4. `localharness` on `PATH`.

`WithBinaryPath` short-circuits the search. An explicitly configured path that
does not exist is an error naming the path, rather than an obscure `exec`
failure several steps later: a stale environment variable is a common mistake
and deserves a comprehensible message.

---

## 2. The wire protocol

Undocumented outside the Python source
(`connections/local/local_connection.py`), and the most likely source of subtle
breakage. Getting a detail wrong here does not produce a compile error; it
produces a hang.

### Startup handshake

Over the subprocess's stdin/stdout:

1. Spawn the binary with stdin, stdout, and stderr piped.
2. Write `InputConfig` to stdin as **binary protobuf**, length-prefixed with a
   **4-byte little-endian uint32**.
3. Read a 4-byte little-endian length from stdout, then that many bytes, and
   parse the result as `OutputConfig`. It carries `port` and `api_key`.
4. Dial `ws://localhost:<port>/` with the header `x-goog-api-key: <api_key>`.
   Retry with exponential backoff (~5 attempts, `0.1 * 2^attempt` seconds) —
   the harness may not be listening yet. Try `localhost` first, then
   `127.0.0.1`, because some environments do not resolve `localhost`.
5. Send `InitializeConversationEvent` and read back an `OutputEvent` whose
   `initialize_conversation_response` carries the restored `history`,
   `cumulative_usage`, and `trajectory_usage`.

Steady state is an exchange of `InputEvent` and `OutputEvent` over the
WebSocket.

### Framing differs between the two channels

- **stdin/stdout**: length-prefixed **binary** protobuf.
- **WebSocket**: **protojson** text frames.

Do not mix them. They are the same message types over two different encodings,
which is exactly the kind of thing that looks like it must be a mistake and is
not.

Go's `protojson` and Python's `json_format` agree on the defaults that matter —
camelCase field names, string enum names — so the two clients are wire
compatible. Verified against a real payload:

```json
{"config":{"cascadeId":"abc","skillsPaths":["/skills"],
 "enabledHooks":["LIFECYCLE_HOOK_PRE_TOOL"],
 "agentBehavior":"AGENT_BEHAVIOR_INTERACTIVE"}}
```

Go's `protojson` deliberately emits unstable whitespace to discourage byte
comparison of its output. Compare parsed messages with `protocmp.Transform()`,
never raw JSON strings.

### Harness protocol skew

The `.proto` files under `proto/` are vendored from the Python repository's
**main branch**, which runs ahead of the newest `localharness` binary on PyPI.
The two are not versioned together and the handshake offers no way to tell them
apart: `OutputConfig` carries a port and an API key, and no harness version, so
the SDK cannot detect an old harness before it talks to one.

When the SDK sends something the harness's own bindings do not define, the
harness's protojson decoder rejects the **entire** message. One unknown enum
name is enough to take down a whole config. The harness prints the parse error,
exits, and the SDK sees an EOF where the `InitializeConversationResponse`
should have been.

**Current instance: `LIFECYCLE_HOOK_STOP`.** The vendored `LifecycleHook` enum
defines it as `9`; localharness 0.1.15, the newest release, stops at
`LIFECYCLE_HOOK_ON_COMPACTION = 8`. Registering a stop hook
([`WithStopHook`](../options.go)) puts that name in `enabled_hooks`, so against
0.1.15 the session dies at initialize:

```
antigravity: harness initialize failed: waiting for the initialize response: EOF
harness protocol skew: the harness rejected the message because it does not
understand the value "LIFECYCLE_HOOK_STOP" for field "enabledHooks", …
harness stderr:
Failed to parse initial message: proto: (line 1:1614): invalid value for enum
field enabledHooks: "LIFECYCLE_HOOK_STOP"
```

The remedies are to upgrade the harness binary, or to stop using the feature
that sets the value — here, to drop the stop hook. The Python SDK has the same
problem for the same reason: `hooks.stop` exists on its main branch and not in
the released 0.1.15 package.

`internal/harness` detects this and says so, keying off the wording
protobuf-go's decoder uses (`proto: (line L:C): invalid value for enum field …`
and its unknown-field sibling) in the captured stderr, and wrapping the
transport error in a `*ProtocolSkewError` that keeps both the original failure
and the stderr in the chain. A failure with no such evidence is left alone
rather than blamed on a version mismatch. Two details are load-bearing. The
character after `proto:` is either a space or a non-breaking space: protobuf-go
picks one from a hash of the executable, specifically to discourage this
matching, so it is fixed for a given binary but varies between builds — 0.1.15
emits the non-breaking one, and both are accepted. And the harness is killed and
its stderr awaited to EOF, or 250 ms, whichever comes first, before the buffer is
read, because it writes the explanation and exits at almost the same moment.

The column in the parse error is wherever the offending value landed in the
serialized config, so it shifts with the configuration; only the wording is
matched.

**Do not "fix" this by marshalling with
`protojson.MarshalOptions{UseEnumNumbers: true}`.** An old harness would very
likely tolerate the unknown number instead of dying — but Python sends enum
names (`json_format.MessageToJson`), and parity beats taste. Diverging on the
wire format to paper over a version skew trades a legible error for a silent
behavioral difference from the reference implementation.

### Teardown

Always kill the subprocess and drain stderr. Harness stderr is the only
diagnostic available when a handshake fails, and the Python client surfaces it
in the error message. `internal/harness` does the same, attaching captured
stderr to `*StartError`.

---

## 3. Protobuf codegen

`.proto` sources are vendored from the Python repository into
`proto/google/antigravity/proto/`. **The nested path is load-bearing**:
`localharness.proto` imports `"google/antigravity/proto/content.proto"`, so the
include root must contain that directory structure.

Regenerate with [buf](https://buf.build) (buf carries its own compiler, so no
`protoc` is required):

```sh
cd proto && buf generate
```

`protoc-gen-go` must be on `PATH`:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
```

Two things to know:

- **`localharness.proto` is `edition = "2024"`** while `content.proto` is
  `edition = "2023"`. Both need a recent protobuf-go; v1.36.12 handles them.
- **Generated code uses the Opaque API** (`protogen:"opaque.v1"`), the default
  for editions. There are no exported struct fields. Construct with builders,
  read with getters:

  ```go
  cfg := pb.InputConfig_builder{
      StorageDirectory: proto.String("/tmp/store"),
      ClientInfo:       pb.ClientInfo_builder{Language: proto.String("go")}.Build(),
  }.Build()
  cfg.GetBindAddress() // "localhost" — an editions field default, applied for you
  ```

That opacity is why the generated code sits under `internal/`: it never reaches
an SDK consumer, so it can be regenerated freely without a breaking change.

The protos carry `gaos.parsing.*` custom options that matter to the Python JSON
layer. They are inert for Go.

---

## 4. Layout

```
antigravity.go, *.go      Public API (single root package)
internal/genproto/        Generated protobuf bindings — never hand-edit
internal/                 Implementation: harness spawn, transport, events
tracing/                  OpenTelemetry integration — a separate Go module
proto/                    Vendored .proto sources + buf config
examples/                 Runnable examples, mirroring the Python examples/
docs/                     This file
dev/                      Build and CI tooling
```

### One public package

**The entire public API lives in the root `antigravity` package.** This is
deliberate.

Go forbids import cycles, and the Python SDK's types are mutually referential:
`Agent` needs policies, policies need `ToolCall`, `Conversation` needs `Step`,
hooks need all of them. Splitting them across packages by topic — the obvious
Go instinct — produces a cycle almost immediately, and breaking it means either
an artificial `types` package that holds everything anyway, or interfaces
defined for no reason other than to dodge the compiler.

One public package plus `internal/` implementation packages avoids the problem
and gives callers a single import. **Put new public types in a new file in the
root package, not a new package.**

### Three modules

`tracing/` is the one exception to the single-package rule, and it is a
*separate module* with its own `go.mod`. It imports the root package rather
than being imported by it, so there is no cycle, and keeping it outside the
main module keeps the SDK's dependency graph free of OpenTelemetry. A consumer
who does not want OTel does not get it transitively.

It reaches the session through ordinary public hooks — the same ones any caller
has, with no privileged access. It deliberately does **not** register a
pre-tool-call hook: doing so would satisfy the safety-policy gate (§6) without
actually supervising anything, turning an observability import into a silent
weakening of the agent's guardrails.

That gives three modules:

| Module | Why it is separate |
|:-------|:-------------------|
| `.` | The SDK. |
| `tracing/` | Keeps OpenTelemetry out of the SDK's dependency graph. |
| `examples/deep_dives/observability_otel/` | Imports `tracing/`, so it cannot live in the root module. |

The root `./...` reaches none of the others. Every check in `dev/tools/` loops
over all three; see `modules()` in `dev/tools/common.sh`.

### Examples

Examples are `examples/<category>/<name>/main.go`, each a `package main` in the
root module, so `go build ./...` compiles them all. That is the point: an
example that stops compiling is a broken example, and CI should say so.

The cost is that **any dependency an example adds becomes a dependency of the
SDK**. That is why `examples/resources/piratemath` is a hand-written MCP server
rather than one built on an MCP library, and why the OTel deep dive has its own
`go.mod`.

---

## 5. API style

Idiomatic Go, not a transliteration:

| Python | Go |
|:-------|:---|
| `async with Agent(config)` | `New(ctx, opts...)` returning a value with `Close()` |
| `LocalAgentConfig(...)` kwargs | functional options (`WithSystemPrompt`) |
| `async for x in stream` | `iter.Seq2` / channels |
| exceptions | `error` returns, sentinel errors, `errors.Is` |
| implicit event loop | explicit `context.Context` as the first parameter |

Every blocking or cancellable call takes a `context.Context`.

Errors are sentinels wrapped with `%w` — `ErrHarnessNotFound`,
`ErrInvalidPrompt`, `ErrExecution`, `ErrConnection` — so callers branch with
`errors.Is` rather than on message text. Structured detail rides on typed
errors (`*ConfigError`, `*HarnessError`) reachable with `errors.As`.

---

## 6. Behavior worth preserving

Parity details that are easy to miss, enforced by the Python tests, and cheap
to break by accident.

### Two different "safe defaults" — do not conflate them

The abstract `AgentConfig` defaults `capabilities` to read-only tools and
`policies` to empty. `LocalAgentConfig`, which is what callers actually use,
**overrides both**: all tools enabled, with `policy.confirm_run_command()` as
the default policy set. That policy allows everything except `run_command`,
which is denied outright unless an ask-user handler is supplied.

The net effect of the default configuration is **"everything but shell"**, not
"read-only". The Python README's claim that `Agent` is read-only by default
describes the abstract base class, not the configuration anyone constructs.

### The safety-policy gate

Starting a session with write tools or MCP servers enabled **and** no policy
**and** no pre-tool-call hook is an **error**, not a warning. See `agent.py`;
the message names the remedies.

It does not fire for a default configuration precisely because the default
policy list is non-empty. It exists to catch the caller who disabled the
default policies on purpose and then forgot to supply anything in their place —
the configuration that looks locked down and is not.

This is also why `tracing/` does not register a pre-tool-call hook (§4).

### Everything else

- **Workspaces default to the process working directory**, and file tools are
  automatically scoped to them by the policy evaluator.
- **`enabled_tools` and `disabled_tools` are mutually exclusive.** Validate;
  do not silently prefer one.
- **Explicit configuration always beats environment variables** — Vertex
  settings and `GEMINI_API_KEY` alike.
- **`chat()` rejects nil, empty, and whitespace-only prompts**, including a
  sequence whose elements are all blank strings.
- **A nameless `ModelTarget` takes the package default for its modality.**
  This is a **deliberate divergence**, and the one place a port should not copy
  Python. Python's `ModelTarget.name` is optional and never defaulted
  (`models.py`: `name: str | None = None`); `build_models_proto` sends
  `m.name or ""` (`local_connection.py`); and `_merge_models_list` keys the
  default-model fallback on `types` alone, so a nameless explicit target
  suppresses the default for its type. `models=[ModelTarget(endpoint=…)]` in
  Python would therefore hand the harness an empty model name and die mid-turn
  with `tModel: model is empty`. No Python caller trips over it in practice
  only because nobody writes that: `prioritized_inference.py` reaches for
  `LocalAgentConfig(endpoint=…)` instead — a field that does not exist on
  `LocalAgentConfig` or `AgentConfig`, so pydantic's default `extra="ignore"`
  drops it, silently discarding the service tier the example demonstrates.

  Go has no `endpoint` config field; endpoint selection lives on the target, so
  a nameless target is the natural way to say "the default model, on this
  endpoint" and has to work. Where the types admit no single default — both
  modalities on one target — `ModelTarget.validate` rejects it at `New` rather
  than guessing.

### A deliberate divergence: the ToolCall a dynamic policy sees

Python builds the `ToolCall` for a dynamic policy decision by hand
(`event_processor.py._handle_policy_decision_request`): the raw wire tool name,
the arguments straight off the wire, and no `canonical_path`. Its own pre-tool
hook path (`hook_router.py._handle_pre_tool`) does the opposite — normalizes
the path arguments, derives `canonical_path`, and maps `invoke_subagent` to
`start_subagent`. Python never notices the gap because its example predicates
take parsed typed tool arguments rather than the call's canonical path.

Go builds both from the same `PreToolArgs` with `toolCallFromArgs`, so a
predicate and a hook watching the same call see the same thing. This is a
knowing divergence from the reference rather than an oversight: a predicate
that gates on `CanonicalPath` reads an empty string under the Python behavior
and therefore never matches, which turns a path-scoped deny or ask-user rule
into a silent allow. Go's `Predicate` takes a `ToolCall` and nothing else —
there is no typed-arguments form to fall back to — so `CanonicalPath` and
`Args` are all a Go caller has. A fail-open in a security control is not parity
worth keeping, and two `ToolCall` construction sites that disagree are a trap
for the next reader. Do not "restore parity" here without reading this
paragraph.

Two smaller divergences in the same area, for the same reason:

- **An empty path argument never clears the canonical path.** Python assigns
  `canonical_path` unconditionally as it walks the path keys, so a later key
  holding `""` erases what an earlier one supplied. `normalizeArgPaths` skips
  empty values. This affects `Step.ToolCalls` too, not only policies.
- **Which path wins when a call carries several.** Python takes the first
  non-empty key of `WIRE_PATH_ARGUMENT_KEYS`, which is a `frozenset` — its
  iteration order varies with the process hash seed, so the answer is not
  stable across runs. `wirePathArgKeys` is a slice and the last non-empty key
  in its order wins, deterministically.

### Turn completion and the event queue

An implementation detail with a race in it, worth stating because it has bitten
once.

The event processor marks a trajectory idle and queues the turn's events on the
same goroutine. **The idle marker is queued before the idle gate opens**, so a
caller that observes the trajectory as idle can rely on the turn's remaining
events already being on the queue rather than racing the goroutine that emits
them.

Idleness alone therefore does not mean the previous turn is accounted for: a
turn that ended before anyone read it leaves its events queued, and one of them
may be the failure that ended it. `Conversation.Send` drains when the processor
is not idle **or** still holds pending events; draining is what surfaces that
failure instead of discarding it at the next turn's reset.

---

## 7. Testing

```sh
go build ./... && go vet ./... && go test ./...
(cd tracing && go build ./... && go vet ./... && go test ./...)
(cd examples/deep_dives/observability_otel && go build ./... && go vet ./...)
```

Or `dev/tools/ci`, which does all of it and more, looping over the three
modules for you.

### The fake harness

There is no `localharness` binary, so anything touching the transport runs
against a **fake harness**: a test helper that performs the stdin/stdout
handshake and then serves scripted `OutputEvent`s over a real local WebSocket.
It lives in `internal/harness`.

Real subprocess, real WebSocket, real framing — only the agent behind it is
scripted. A fake that stubbed out the transport would test the SDK against its
own assumptions about the protocol, which is precisely the thing most likely to
be wrong.

The Python equivalents are `connections/local/test_utils.py` and the fixtures
in `local_connection_test.py`. At 5.3k lines the latter is the richest source
of protocol edge cases in either repository; consult it before inventing a
scenario.

### Conventions

- Prefer table-driven tests.
- Compare protos with `protocmp.Transform()`, never by marshalled bytes.
- Synchronize on an observable effect, not on a sleep. The fake harness's usage
  updates make a convenient barrier: events are handled in order on one
  goroutine, so a usage total landing proves everything pushed before it has
  been processed.
- A bug fix ships a regression test that **fails on the pre-fix code**. For
  concurrency fixes this is not optional and not automatic — verify it.

### Coverage

`dev/coverage-floors.txt` holds a per-package floor, and `dev/tools/ci` fails
when a package drops below it. Floors ratchet up, never down.

`examples/` and `internal/genproto` are excluded from measurement
(`COVERAGE_EXCLUDE` in `dev/tools/verify-coverage`). Both would otherwise
contribute a large body of deliberately untested code — 30-odd example `main`s
and generated bindings — and flooring them at zero would defeat the property
worth having: that no hand-written package arrives unmeasured.

---

## 8. Go version

`go.mod` declares `go 1.24` with `toolchain go1.26.7`. The split is
intentional: the `go` directive is the **minimum version a consumer needs**, so
it stays low for compatibility, while `toolchain` pins what this repository
builds with. **Do not raise the `go` directive to get a newer toolchain** —
raise it only when the code genuinely requires a newer language feature, and
say which one in the commit.

`GOTOOLCHAIN=auto` fetches 1.26.7 automatically.

`tracing/` declares `go 1.25.0`, because the OpenTelemetry API requires it.
That is a floor on the tracing module's consumers only, which is another reason
it is a separate module.
