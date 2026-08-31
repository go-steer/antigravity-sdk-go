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
	"errors"
	"strings"
	"testing"
)

func TestToolError(t *testing.T) {
	cause := errors.New("timed out")

	err := &ToolError{ToolName: "get_weather", CallID: "call-1", StepID: "step-1", Err: cause}
	if got := err.Error(); !strings.Contains(got, `"get_weather"`) || !strings.Contains(got, "timed out") {
		t.Errorf("Error = %q, want the tool and the cause", got)
	}
	// The cause stays reachable, so a caller can test for its own sentinel
	// through the wrapper.
	if !errors.Is(err, cause) {
		t.Error("the underlying failure is not reachable with errors.Is")
	}

	// An MCP tool is only identified by name plus server, so both appear.
	mcp := &ToolError{ToolName: "forecast", ServerName: "weather", Err: cause}
	if got := mcp.Error(); !strings.Contains(got, `"weather"`) {
		t.Errorf("Error = %q, want the server named", got)
	}
}

func TestConfigError(t *testing.T) {
	cause := errors.New("must not be negative")
	err := &ConfigError{Field: "MaxHistory", Err: cause}

	if got := err.Error(); !strings.Contains(got, "MaxHistory") || !strings.Contains(got, cause.Error()) {
		t.Errorf("Error = %q, want the field and the cause", got)
	}
	// Callers branch on ErrInvalidConfig without enumerating every field.
	if !errors.Is(err, ErrInvalidConfig) {
		t.Error("a ConfigError does not match ErrInvalidConfig")
	}
	if !errors.Is(err, cause) {
		t.Error("the underlying failure is not reachable with errors.Is")
	}
	var as *ConfigError
	if !errors.As(err, &as) || as.Field != "MaxHistory" {
		t.Error("the field is not recoverable with errors.As")
	}

	// Some validation failures are about the configuration as a whole.
	if got := (&ConfigError{Err: cause}).Error(); strings.Contains(got, "for ") {
		t.Errorf("Error = %q, want no field clause when there is no field", got)
	}
}

func TestHarnessError(t *testing.T) {
	cause := errors.New("unexpected EOF")
	err := &HarnessError{Op: "handshake", Err: cause}

	if got := err.Error(); !strings.Contains(got, "handshake") || !strings.Contains(got, cause.Error()) {
		t.Errorf("Error = %q, want the operation and the cause", got)
	}
	if !errors.Is(err, ErrConnection) {
		t.Error("a HarnessError does not match ErrConnection")
	}
	if !errors.Is(err, cause) {
		t.Error("the underlying failure is not reachable with errors.Is")
	}

	// Stderr is usually the only diagnostic a failed handshake leaves behind,
	// so it has to survive into the message.
	withStderr := &HarnessError{Op: "dial", Stderr: "port already in use", Err: cause}
	if got := withStderr.Error(); !strings.Contains(got, "port already in use") {
		t.Errorf("Error = %q, want the harness stderr included", got)
	}
	if got := err.Error(); strings.Contains(got, "harness stderr") {
		t.Errorf("Error = %q, want no stderr section when there is none", got)
	}
}
