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

package harness

import (
	"fmt"
	"regexp"
)

// The vendored `.proto` describes the protocol as of the Python repository's
// main branch, which can be newer than the localharness binary a user actually
// has installed. When it is, the harness parses our initialize frame with a
// protojson decoder that has never heard of a value we send, rejects the whole
// message, and exits — so the SDK sees nothing but an EOF where the
// InitializeConversationResponse should have been.
//
// There is no capability negotiation to prevent that: the startup handshake's
// OutputConfig carries only a port and an API key, with no harness version. The
// harness's own stderr is the only evidence of what went wrong, so that is what
// the diagnosis below reads.

// These match the wording protobuf-go's protojson decoder uses for the two ways
// a message can carry something the receiving side does not know: a name that
// is not in an enum, and a field that is not in the message. Keying off the
// protobuf runtime's format rather than the harness's own log line is the more
// stable of the two anchors on offer — the `proto:` prefix and the `(line L:C):`
// position come from protobuf-go itself, and requiring both is what keeps
// unrelated harness output that happens to mention an "unknown field" from
// being read as version skew. It is still string matching, so the diagnosis is
// a hint layered on an error that is reported either way, never a precondition
// for reporting one.
//
// Two details of protobuf-go's error formatting are worth naming, because both
// were found the hard way:
//
//   - The character after `proto:` is either a space or a non-breaking space
//     (U+00A0). The runtime picks one from a hash of the executable, on purpose,
//     to discourage exactly this kind of matching — so it is fixed for a given
//     binary but varies between builds. Both are accepted. localharness 0.1.15
//     happens to emit the non-breaking one.
//   - `errors.Wrap` inside protobuf-go hoists the `proto:` prefix to the front
//     of the message and strips it from the inner error, which can leave text
//     between the prefix and the position. Hence `.*?` rather than adjacency.
//
// TestSkewPatternsMatchTheProtojsonRuntime pins both patterns against errors
// produced by the linked protobuf-go, so a change to that wording fails the
// build rather than silently disabling the diagnosis.
var (
	protojsonEnumRE = regexp.MustCompile(
		`proto:[ \x{00a0}].*?\(line \d+:\d+\): invalid value for enum field ([^\s:]+): "?([^"\s]+)"?`)
	protojsonFieldRE = regexp.MustCompile(
		`proto:[ \x{00a0}].*?\(line \d+:\d+\): unknown field "([^"]+)"`)
)

// ProtocolSkewError reports that the harness rejected a message because part of
// it was unintelligible to the harness's own protobuf bindings, which is the
// signature of a localharness binary older than the protocol revision this SDK
// was generated against.
//
// It wraps the transport-level failure the SDK actually observed — usually an
// EOF — and adds the explanation, since that failure on its own says nothing
// about the cause.
type ProtocolSkewError struct {
	// Field is the protojson field name the harness rejected, such as
	// "enabledHooks".
	Field string
	// Value is the unrecognized enum value, such as "LIFECYCLE_HOOK_STOP". It
	// is empty when the harness did not recognize the field at all.
	Value string
	// Err is the failure the SDK observed on the wire.
	Err error
}

func (e *ProtocolSkewError) Error() string {
	what := fmt.Sprintf("the field %q", e.Field)
	if e.Value != "" {
		what = fmt.Sprintf("the value %q for field %q", e.Value, e.Field)
	}
	return fmt.Sprintf("%v\nharness protocol skew: the harness rejected the message because "+
		"it does not understand %s, so the localharness binary is probably older than the "+
		"protocol revision this SDK was built against. Upgrade the localharness binary, or "+
		"stop using the SDK feature that sets it. See \"Harness protocol skew\" in the SDK's "+
		"docs/DESIGN.md.", e.Err, what)
}

func (e *ProtocolSkewError) Unwrap() error { return e.Err }

// diagnoseSkew wraps err with a [*ProtocolSkewError] when stderr shows the
// harness failing to parse what we sent. Anything else is returned untouched:
// a failure with no such evidence must not be blamed on version skew.
func diagnoseSkew(err error, stderr string) error {
	if m := protojsonEnumRE.FindStringSubmatch(stderr); m != nil {
		return &ProtocolSkewError{Field: m[1], Value: m[2], Err: err}
	}
	if m := protojsonFieldRE.FindStringSubmatch(stderr); m != nil {
		return &ProtocolSkewError{Field: m[1], Err: err}
	}
	return err
}
