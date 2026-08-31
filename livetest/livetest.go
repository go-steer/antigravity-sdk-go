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

// Package livetest holds the end-to-end suite that drives a real
// `localharness` binary against a real model backend.
//
// It is behind the `live` build tag and is not part of `go test ./...`. The
// unit suite proves the SDK against internal/harness's fake, which speaks
// the real protocol but answers with scripted events; this suite proves the
// two things that fake cannot — that the harness accepts the config and wire
// messages the SDK actually emits, and that a model's real replies flow back
// through the event processor intact.
//
// Run it with dev/tools/test-live, which fetches the harness and checks
// credentials first.
package livetest
