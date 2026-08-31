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

package antigravity

// Chunk is one semantic event in a streaming turn: a [TextChunk], a
// [ThoughtChunk], or a [ToolCall].
//
// The interface is closed, so a type switch over those three is exhaustive.
type Chunk interface {
	isChunk()
}

// TextChunk is an increment of the model's user-facing response.
//
// Chunks are deltas, not snapshots: concatenating them in order reproduces the
// full response.
type TextChunk struct {
	// StepIndex is the position of the producing step in its trajectory.
	StepIndex int
	// Text is the newly generated text.
	Text string
}

// ThoughtChunk is an increment of the model's internal reasoning.
type ThoughtChunk struct {
	// StepIndex is the position of the producing step in its trajectory.
	StepIndex int
	// Text is the newly generated reasoning.
	Text string
}

func (TextChunk) isChunk()    {}
func (ThoughtChunk) isChunk() {}
func (ToolCall) isChunk()     {}
