# Contributing to antigravity-sdk-go

Thanks for contributing. This repository follows the same contributor flow as
[core-agent](https://github.com/go-steer/core-agent) and
[purser](https://github.com/go-steer/purser) — a maintainer of one repo should
recognize the others.

## Workflow

- Single long-lived branch: `main`. Work on short-lived feature branches
  (`feat/…`, `fix/…`, `chore/…`, `docs/…`) → PR against `main` → merge
  once CI's required checks are green.
- **Rebase, don't merge.** Keep feature branches rebased on `main`;
  `git push --force-with-lease` on your own branch is normal. Never
  force-push `main`.

## Commits

- **Conventional Commits** subject lines: `feat:`, `fix:`, `docs:`,
  `chore:`, `refactor:`, `test:`, `ci:`, `build:`. Bodies explain *why*
  and call out the verification done.
- **DCO sign-off** on every commit: `git commit -s` (adds a
  `Signed-off-by:` trailer certifying the [Developer Certificate of
  Origin](https://developercertificate.org/)).
- **No `Co-Authored-By` trailer, and no assistant attribution anywhere.**
  Maintainer preference: author the work under your own name. Do not add
  `Co-Authored-By:` lines, "Generated with …" footers, or any
  tool/assistant credit to commits, PR titles/bodies, or other committed
  or published artifacts.

## Before you push

- Run `dev/tools/ci` — the full presubmit sweep, the same scripts CI
  runs (`dev/ci/presubmits/*`). A green local run is the green remote
  run. It loops over all three modules; a bare `go test ./...` from the
  root does not.
- **Adversarial-review gate** on any PR touching Go code — see
  [`AGENTS.md`](./AGENTS.md) "How we develop". Record the outcome under an
  `## Adversarial review` heading in the PR body; the `review-gate`
  required CI check enforces it.

## Parity with the Python SDK

This module is a port of the [Google Antigravity SDK for
Python](https://github.com/Google-Antigravity/antigravity-sdk-python), and the
Python source is the specification. The API shapes are Go's — functional
options, `context.Context`, `error` returns, `iter.Seq2` — but the *behavior*
is not ours to improve.

Before changing behavior, check what the Python SDK does. Its `*_test.py` files
state it more precisely than its docs do; where the two disagree, believe the
tests. A deliberate divergence needs a comment saying so and why, and a note in
[`docs/DESIGN.md`](./docs/DESIGN.md).

[`docs/DESIGN.md`](./docs/DESIGN.md) §6 collects the parity details most often
got wrong. Two worth repeating here:

- The default configuration is **"everything but shell"**, not read-only. The
  read-only default belongs to the abstract base class in Python, which no
  caller instantiates.
- The safety-policy gate is an **error**, not a warning.

## Changing the exported API

The SDK is consumed as a Go module, so the exported surface *is* the
product: a removed field or a changed signature breaks a consumer's build
at `go get`. `dev/tools/verify-apidiff` compares the exported API of both
published modules — the root and `tracing/` — against the last release tag and
fails on incompatible changes that
[`dev/api-breaks.txt`](./dev/api-breaks.txt) does not acknowledge.

`internal/` is exempt by construction, which is deliberate: the generated
protobuf bindings in `internal/genproto` use the Opaque API and are regenerated
whenever the vendored `.proto` files move, so they must never be part of the
contract.

Pre-1.0 the project reserves the right to break the surface at a minor
version — but "you may break it" is not "you may break it silently".
To make a deliberate break:

1. Run `dev/tools/verify-apidiff` locally. Copy the lines it prints under
   *Incompatible changes* into `dev/api-breaks.txt`, minus the leading `- `.
2. Add a `#` comment above them naming the PR or issue and the reason.
3. Commit both in the PR that makes the change, and record it under
   *Changed* or *Removed* in `CHANGELOG.md`.

Cutting a release empties the file: once the tag moves, those breaks sit
behind the new baseline, and a leftover entry would silently permit a
second, unrelated removal.

Prefer not to need the escape hatch. Add an option rather than a parameter;
deprecate with an alias before removing.

## Tests

Every new package ships with unit tests. A new feature without a test
is not done; a bug fix without a regression test lets the bug come back.

There is no `localharness` binary in this repository, so anything touching the
transport runs against the **fake harness** in `internal/harness`: it performs
the real stdin/stdout handshake and then serves scripted `OutputEvent`s over a
real local WebSocket. Real subprocess, real WebSocket, real framing — only the
agent behind it is scripted. Do not stub the transport itself; that tests the
SDK against its own assumptions about the protocol, which is the thing most
likely to be wrong.

Two failure modes this code has in particular:

- **Concurrency.** The read loop, the event queue, and the turn lifecycle all
  run concurrently. A race here reproduces on maybe half of runs, so a fix
  needs a *deterministic* regression test, and you need to confirm it fails
  against the parent commit. Synchronize on an observable effect — the fake
  harness's usage updates make a convenient barrier, since events are handled
  in order on one goroutine — never on a sleep.
- **Protocol details.** Compare protos with `protocmp.Transform()`, never as
  marshalled bytes: Go's `protojson` emits unstable whitespace deliberately.

`dev/coverage-floors.txt` holds a per-package floor and CI fails on a
regression. Floors ratchet up, never down. `examples/` and `internal/genproto`
are excluded from measurement.

## Regenerating the protobuf bindings

The `.proto` sources are vendored from the Python repository into
`proto/google/antigravity/proto/`. The nested path is load-bearing — the
imports depend on it.

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
cd proto && buf generate
```

Never hand-edit `internal/genproto/`.

## License

By contributing you agree your contributions are licensed under the
project's [Apache 2.0](./LICENSE) license. Every Go / shell / YAML source
file carries the Apache 2.0 header attributed to Google LLC.
