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

import (
	"encoding/json"
	"testing"
)

func TestStepUnmarshalStructuredOutput(t *testing.T) {
	type answer struct {
		Verdict string `json:"verdict"`
	}

	step := Step{StructuredOutput: json.RawMessage(`{"verdict":"approved"}`)}

	var got answer
	ok, err := step.UnmarshalStructuredOutput(&got)
	if err != nil {
		t.Fatalf("UnmarshalStructuredOutput: %v", err)
	}
	if !ok {
		t.Fatal("the step reported no structured output")
	}
	if got.Verdict != "approved" {
		t.Errorf("output = %+v", got)
	}
}

func TestStepWithoutStructuredOutput(t *testing.T) {
	// Most steps carry none, and asking is not an error: the boolean is what
	// distinguishes "absent" from "decoded to the zero value".
	type answer struct {
		Verdict string `json:"verdict"`
	}

	var step Step
	got := answer{Verdict: "untouched"}
	ok, err := step.UnmarshalStructuredOutput(&got)
	if err != nil {
		t.Fatalf("UnmarshalStructuredOutput: %v", err)
	}
	if ok {
		t.Error("an empty step reported structured output")
	}
	if got.Verdict != "untouched" {
		t.Errorf("output = %+v, want it left alone", got)
	}
}

func TestStepStructuredOutputMismatch(t *testing.T) {
	// The schema is enforced by the model, not the SDK, so output that does not
	// fit the caller's type surfaces as a decode error rather than a panic.
	var got struct {
		Verdict string `json:"verdict"`
	}
	step := Step{StructuredOutput: json.RawMessage(`{"verdict":42}`)}
	ok, err := step.UnmarshalStructuredOutput(&got)
	if err == nil {
		t.Fatal("mismatched structured output decoded without error")
	}
	if ok {
		t.Error("a failed decode reported success")
	}
}
