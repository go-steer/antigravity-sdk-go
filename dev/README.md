# dev/

Build- and test-tooling. The same scripts power both local development and
GitHub Actions CI, so a green local run is the same green run as remote.

## Quickstart

```bash
# Run every CI check locally (fast-fail order).
dev/tools/ci

# Run all checks even after a failure (collect every problem at once).
dev/tools/ci --keep-going

# Auto-fix formatting (gofmt + goimports).
dev/tools/fix-go-format
```

Missing tools (`golangci-lint`, `goimports`, `govulncheck`, `apidiff`)
auto-install into `$GOBIN` (or `$(go env GOPATH)/bin`) on first use. No setup
needed beyond a Go toolchain.

## Three modules

This repository holds three Go modules:

| Module | Why it is separate |
|:-------|:-------------------|
| `.` | The SDK. |
| `tracing/` | Keeps OpenTelemetry out of the SDK's dependency graph. |
| `examples/deep_dives/observability_otel/` | Imports `tracing/`, so it cannot live in the root module. |

The root `./...` reaches neither of the others, so a bare `go test ./...`
silently skips two thirds of the tree. Every script here loops instead —
`modules()` in `common.sh` discovers them by finding `go.mod` files, root
first, so adding a fourth module needs no edit to any script.

Two consequences worth knowing:

- `dev/tools/build` writes executables to a temp directory. `go build ./...`
  discards the result when it names several packages, but a module holding
  exactly one `package main` — the OTel example — gets its binary written into
  the working tree, which is how 15MB lands in a `git add -A`. The `-o <dir>/`
  form errors out where there is no main package at all, hence the check inside
  the script.
- `dev/tools/verify-apidiff` iterates only the *published* modules (`.` and
  `tracing`), and skips a module that is absent from the baseline worktree, so
  adding a module does not fail the diff against an older tag.

## Layout

```
dev/
├── api-breaks.txt         # acknowledged incompatible API changes
├── coverage-floors.txt    # per-package coverage floors
├── claude/                # opt-in Claude Code config sample (not live config)
├── tools/                 # entry points you run locally
│   ├── ci                 # aggregator — runs every check below
│   ├── verify-go-format   # gofmt -s + goimports check (read-only)
│   ├── fix-go-format      # gofmt -s -w + goimports -w (auto-fix)
│   ├── vet                # go vet ./...
│   ├── build              # go build ./...
│   ├── lint-go            # golangci-lint (auto-installs the pinned version)
│   ├── verify-mod-tidy    # `go mod tidy` clean check
│   ├── verify-apidiff     # exported-API diff vs the last release tag
│   ├── test-unit          # go test -race -coverprofile
│   ├── verify-coverage    # per-package floors from coverage-floors.txt
│   ├── verify-vuln        # govulncheck ./...
│   ├── add-license-headers # bulk-applier for the Apache 2.0 header
│   ├── common.sh          # shared bash helpers (modules, ensure_tool, run_step)
│   └── .golangci.yml      # linter config
└── ci/
    └── presubmits/        # thin delegators called by .github/workflows/
        ├── verify-go-format
        ├── vet
        ├── build
        ├── lint-go
        ├── verify-mod-tidy
        ├── verify-apidiff
        ├── verify-coverage
        ├── verify-vuln
        └── test-unit
```

## Adding a check

1. Drop a new script under `dev/tools/<name>` (executable, `set -euo pipefail`,
   sources `common.sh`).
2. Add it to the `STEPS` array in `dev/tools/ci`.
3. Add a one-line delegator under `dev/ci/presubmits/<name>` that
   `exec`s the tool script.

That's it. The `ci` workflow runs `dev/tools/ci`, so a new step is picked up
without touching any YAML — the delegator exists so the step can also be run
on its own, and so nothing in a workflow ever has to know what a check does.

## Live testing

No step in `dev/tools/ci` runs a real agent, but one exists outside it.

The agentic loop lives in a precompiled `localharness` binary this repository
does not build. It is obtainable, though: Google ships it inside the published
`google-antigravity` Python wheel, at `google/antigravity/bin/localharness`.

| Command | What it does |
| --- | --- |
| `dev/tools/fetch-harness` | Downloads the pinned wheel and unpacks the binary to `dev/.harness/` (gitignored). |
| `dev/tools/test-live` | Fetches the harness, then runs the `live`-tagged suite in [`livetest/`](../livetest) against it. |

`test-live` needs credentials — either `GEMINI_API_KEY`, or
`GOOGLE_GENAI_USE_VERTEXAI=true` with `GOOGLE_CLOUD_PROJECT` and
`GOOGLE_CLOUD_LOCATION` (Vertex uses Application Default Credentials). Without
them the suite skips itself and says what is missing.

It is not a required check, and should not become one: it costs money per run
and can fail for reasons no PR caused. Run it before a release, and after any
change to the wire layer — `internal/harness`, `harnessconfig.go`,
`stepconv.go`, `eventproc.go`.

Everything in the default sweep still runs against the fake harness in
`internal/harness` — a real subprocess doing the real handshake over a real
WebSocket, with a scripted agent behind it. See
[`docs/DESIGN.md`](../docs/DESIGN.md) §7.

### Harness version skew

The `.proto` this repo vendors is ahead of the newest published harness. The
pin in `fetch-harness` records which harness the live suite was last verified
against; bump it in the same commit that adapts the SDK to a newer one.

## Coverage floors

`dev/coverage-floors.txt` names a minimum for each package; `verify-coverage`
fails when one drops below it. Floors ratchet up, never down — raise the line
in the same PR that raises the number.

`examples/` and `internal/genproto` are excluded from measurement
(`COVERAGE_EXCLUDE` in the script). Both are large bodies of deliberately
untested code — 30-odd example `main`s and generated bindings — and flooring
them at zero would defeat the property worth having: that no hand-written
package arrives unmeasured.

## CI on PRs

`.github/workflows/ci.yml` runs the whole sweep in one `ci` job on every push
to `main` and every PR. No path filters, deliberately: a required status check
on a path-filtered workflow never reports on a PR the filter excludes, and
GitHub parks that PR on "Expected — Waiting for status" forever. The Go
pipeline here is fast enough that docs-only PRs simply run it too.

`apidiff` is its own workflow rather than a step in that job, for two reasons:
it needs `fetch-depth: 0` to see the baseline tag, which the main job should
not pay for; and it is deliberately **not** a required check. It depends on a
tag being fetchable, so a mirror hiccup or a moved tag would turn into a red
required check on unrelated PRs. Its verdict is visible on every PR; promote
it to required once it has a release cycle of clean history behind it.

`ci.yml` sets `ANTIGRAVITY_CI_SKIP=apidiff` so the aggregator omits the step
that runs in that separate job. The step list itself stays in one place —
`dev/tools/ci` — so the local sweep cannot drift from the remote one.

Branch protection on `main` requires:

- `ci`
- `review-gate`

## review-gate

`.github/workflows/review-gate.yml` fails any PR that touches `.go` files
whose body has no "Adversarial review" section. It is the tool-agnostic
backstop behind the convention in [`AGENTS.md`](../AGENTS.md) "How we develop";
`dev/claude/settings-review-gate.json` is an opt-in local hook that catches the
same thing before CI ever sees it. Docs-only and bot PRs are exempt.

## License headers

Every source file carries the full Apache 2.0 header at the top, attributed to
Google LLC:

```
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
```

(`#`-prefixed for shell, YAML, and Python.) The `goheader` linter inside
`dev/tools/lint-go` enforces this on every `.go` file — CI fails if a new Go
source is missing it. For shell, YAML, and Python files, run
`dev/tools/add-license-headers` after creating new ones; the script is
idempotent and normalizes any existing header to the current canonical form.

`internal/genproto` is exempt. It is written by `protoc-gen-go`, and a header
applied there would be rewritten away by the next `buf generate`.

Its `HEADER_BODY` and the `goheader` template in `dev/tools/.golangci.yml` are
two copies of the same text and drift silently. Change one, change the other,
and run `dev/tools/lint-go` to confirm they still agree.

## Pinned tool versions

| Tool          | Version    | Source                                                     |
|---------------|------------|------------------------------------------------------------|
| golangci-lint | v2.12.1    | `dev/tools/lint-go` (`GOLANGCI_LINT_VERSION`)              |
| apidiff       | pinned     | `dev/tools/verify-apidiff` (`APIDIFF_VERSION`)             |
| goimports     | latest     | `dev/tools/fix-go-format`, `dev/tools/verify-go-format`    |
| govulncheck   | latest     | `dev/tools/verify-vuln`                                    |

`apidiff` is pinned to a pseudo-version because `golang.org/x/exp` carries no
tags. It is pinned at all — unlike govulncheck — because its classification of
a change as compatible or incompatible *is* the gate; letting it float would
let an upstream reclassification turn a green PR red with no local diff to
explain it.

Bump deliberately — new linter releases can introduce findings that block CI.
When you bump golangci-lint, run `dev/tools/lint-go` locally first to fix
anything new before pushing.

The workflows pin `actions/checkout@v7` and `actions/setup-go@v7`. The
setup-go major matters: since v6 it installs the `toolchain` directive rather
than the `go` directive and sets `GOTOOLCHAIN=local`, so CI runs on the pinned
toolchain and no `go` command silently fetches another. A module that raised
its `go` directive above the pinned toolchain would fail loudly instead —
which is the behavior we want.

## Regenerating protobuf bindings

Not a CI step: the bindings are checked in, and `buf` is not installed in CI.

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
cd proto && buf generate
```

`localharness.proto` is `edition = "2024"` and `content.proto` is
`edition = "2023"`, so this needs a recent protobuf-go; v1.36.12 handles both.
See [`docs/DESIGN.md`](../docs/DESIGN.md) §3.
