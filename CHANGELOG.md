# Changelog

All notable changes to antigravity-sdk-go are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Because the SDK is consumed as a Go module, entries under **Changed** and
**Removed** are the release notes a consumer reads before upgrading. An
incompatible change to the exported API is recorded here *and* acknowledged in
[`dev/api-breaks.txt`](./dev/api-breaks.txt) — see "Changing the exported API"
in [`CONTRIBUTING.md`](./CONTRIBUTING.md).

## [Unreleased]

### Added

- Initial Go port of the [Google Antigravity SDK for
  Python](https://github.com/Google-Antigravity/antigravity-sdk-python),
  targeting behavioral parity with a Go-shaped API: `New(ctx, opts...)` with
  functional options in place of a config object, `context.Context` on every
  blocking call, `iter.Seq2` in place of async iteration, and sentinel errors
  in place of exceptions.
- `Agent` and `Conversation`: session lifecycle, `Chat` for a whole response,
  `Send` + `Steps` for streaming a turn, history with a configurable cap,
  cumulative and per-turn usage, and `Close`.
- Configuration options covering the Python surface — model selection and
  fallbacks, Gemini Developer API and Vertex endpoints, system prompts and
  structured `SystemInstructions`, workspaces, skills paths, budgets, retry
  policy, session continuation, and structured output via
  `WithResponseSchema[T]`.
- Safety policies: `Allow` / `Deny` / `AskUser` and their builtin-tool and MCP
  variants, composed by `NewEnforcer`, which evaluates in priority order —
  specific targets beat wildcards, and deny beats ask-user beats approve. The
  default policy set is `ConfirmRunCommand`, so a default agent may edit files
  but not run shell commands.
- The safety gate: starting a session with write tools or MCP servers enabled,
  no policy, and no pre-tool-call hook is an error naming the remedies.
- Lifecycle hooks — session start/end, pre/post turn, pre/post tool call, tool
  error, interaction, compaction, and stop — plus `StepObserver`, all sharing a
  `HookContext` hierarchy scoped to the session, the turn, and the individual
  operation.
- Client-side tools via `WithTools`, with JSON Schema derived from the
  argument struct.
- MCP support over stdio and Streamable HTTP (`NewMCPStdioServer`,
  `NewMCPHTTPServer`), with per-server tool filtering.
- Subagents, triggers, and named triggers.
- `RunInteractive`, a terminal REPL that answers the agent's questions from
  stdin and turns the default `run_command` denial into a confirmation prompt.
- Content types for multi-modal prompts: `Text`, `Image`, `Audio`, `Document`,
  and `FromFile` / `FromBytes`.
- Typed tool results (`EditFileResult`, `FindFileResult`, `ListDirectoryResult`,
  `GenerateImageResult`, and the rest) rather than untyped maps.
- `tracing/`, a **separate module** wiring the session's hooks to
  OpenTelemetry. It is separate so the SDK's own dependency graph stays free of
  OTel, and it deliberately registers no pre-tool-call hook — doing so would
  satisfy the safety gate without supervising anything.
- `internal/harness`: harness discovery, subprocess lifecycle, the
  length-prefixed protobuf handshake, and the protojson WebSocket transport,
  plus the fake harness the transport tests run against.
- `internal/genproto`: protobuf bindings generated from the `.proto` sources
  vendored under `proto/`, using the Opaque API. Kept internal so regenerating
  them is never a breaking change.
- 30-odd runnable examples under `examples/`, mirroring the Python `examples/`
  tree, all compiled by `go build ./...`.

### Added (scaffold)

- Contributor / agent guardrails ported from core-agent, core-tui and purser:
  `AGENTS.md` "How we develop", `CONTRIBUTING.md` (DCO, Conventional Commits,
  no attribution, the apidiff flow), `dev/tools/*` + `dev/ci/presubmits/*` +
  `dev/tools/ci`, the `review-gate` required CI check, and the opt-in
  `dev/claude/settings-review-gate.json` hook sample. Every script loops over
  the repository's three modules rather than assuming one.
- `dev/tools/verify-apidiff` + `dev/api-breaks.txt`: the exported surface is
  the product, so every change to it is reported against the last release tag
  and incompatible ones fail unless acknowledged in the same PR. Both published
  modules — the root and `tracing/` — are covered.
- `dev/tools/verify-coverage` + `dev/coverage-floors.txt`: a per-package floor
  that ratchets up. `examples/` and `internal/genproto` are excluded from
  measurement so the floors mean something for hand-written code.
- `livetest/` + `dev/tools/fetch-harness` + `dev/tools/test-live`: an opt-in
  end-to-end suite behind `//go:build live` that drives a real `localharness`
  against a real model, plus the tooling to unpack that binary from the
  published Python wheel. Everything else tests against the fake harness,
  which never reads a schema it is sent and never calls a model. Not part of
  `dev/tools/ci` — it costs money and can fail for reasons no change caused —
  but `dev/tools/vet` type-checks it with `-tags live` so it cannot rot
  unnoticed. `.github/workflows/live-e2e.yml` runs it weekly, on wire-layer
  pushes to `main`, and on demand.
- `docs/DESIGN.md`: the design of record — the client/harness split, the wire
  protocol and its two framings, the single-public-package rule and the
  three-module layout, and the parity details a port can break without failing
  to compile.
