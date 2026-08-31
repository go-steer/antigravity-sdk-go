#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Shared helpers for dev/tools/* scripts.
#
# Source this from each tool with:
#   . "$(dirname "$0")/common.sh"
#
# Provides:
#   repo_root        — absolute path to the git working tree root
#   modules          — every Go module in the repo, repo-root-relative
#   for_each_module  — run a command inside each module, labelled
#   ensure_tool      — go install <pkg>@<ver> if the binary isn't on PATH
#   run_step         — run a command + print a "▸ name" header (for ci aggregator)

set -euo pipefail

repo_root() {
  git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel
}

# modules prints every Go module in the repo, one per line, as a path
# relative to the repo root.
#
# There are three, and no `go` command reaches past the one it is run in:
# `./...` from the root sees neither `tracing` nor the example that imports
# it. Anything that claims to check "the repo" has to loop.
#
# Discovered rather than listed, so a module added tomorrow is covered
# without editing every script. Ordered root first: it is the one most
# likely to fail, and fast-fail wants it early.
modules() {
  local root
  root="$(repo_root)"
  ( cd "$root" && find . -name go.mod -not -path './.git/*' -printf '%h\n' | sed 's|^\./||' | sort -t/ -k1,1 )
}

# for_each_module <label> <command...>
#
# Runs the command with the working directory set to each module in turn,
# printing a per-module header. Stops at the first failure.
for_each_module() {
  local label="$1"; shift
  local root mod
  root="$(repo_root)"
  while read -r mod; do
    printf '  · %s (%s)\n' "$label" "$mod"
    ( cd "$root/$mod" && "$@" )
  done < <(modules)
}

# ensure_tool <bin-name> <go-install-target>
#
# Checks for <bin-name> on PATH; if missing, installs the pinned version
# via `go install`. Honors GOBIN, falls back to $(go env GOPATH)/bin.
# After install, prepends GOBIN to PATH for the calling shell.
ensure_tool() {
  local name="$1"
  local target="$2"
  if command -v "$name" >/dev/null 2>&1; then
    return 0
  fi
  local gobin
  gobin="${GOBIN:-$(go env GOPATH)/bin}"
  # Already installed at GOBIN but not on PATH? Just expose it.
  if [[ -x "$gobin/$name" ]]; then
    export PATH="$gobin:$PATH"
    return 0
  fi
  echo "▸ $name not found — installing $target into $gobin" >&2
  GOBIN="$gobin" go install "$target"
  export PATH="$gobin:$PATH"
  if ! command -v "$name" >/dev/null 2>&1; then
    echo "ensure_tool: $name still missing after install" >&2
    return 1
  fi
}

# run_step <label> <command...>
#
# Runs the command and prints a tidy header. Used by the ci aggregator
# so each check has a visible boundary in the output. Exit code is
# propagated.
run_step() {
  local label="$1"; shift
  printf '\n\033[1m▸ %s\033[0m\n' "$label"
  "$@"
}
