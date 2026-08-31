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
	"fmt"
)

// Sentinel errors for conditions callers are likely to branch on. Match them
// with [errors.Is] rather than comparing strings.
var (
	// ErrConnection reports that a connection to the harness could not be
	// established, or hit a fatal protocol-level error.
	ErrConnection = errors.New("antigravity: connection error")

	// ErrCancelled reports that an active turn was cancelled by the client.
	ErrCancelled = errors.New("antigravity: request cancelled")

	// ErrExecution reports that the agent loop terminated on a fatal error,
	// such as a failed model call or a violated system constraint, and cannot
	// continue.
	ErrExecution = errors.New("antigravity: agent execution failed")

	// ErrInvalidConfig reports a configuration rejected before the session
	// started.
	ErrInvalidConfig = errors.New("antigravity: invalid configuration")

	// ErrInvalidPrompt reports a prompt with no usable content.
	ErrInvalidPrompt = errors.New("antigravity: invalid prompt")

	// ErrNotStarted reports use of an agent or conversation that is not
	// running, typically because it was already closed.
	ErrNotStarted = errors.New("antigravity: session not started")

	// ErrHarnessNotFound reports that the localharness binary could not be
	// located.
	ErrHarnessNotFound = errors.New("antigravity: localharness binary not found")
)

// ToolError reports a failed tool execution, carrying enough metadata to
// identify which call failed.
type ToolError struct {
	// ToolName is the tool that failed.
	ToolName string
	// ServerName is the MCP server owning the tool, when applicable.
	ServerName string
	// CallID identifies the specific invocation.
	CallID string
	// StepID identifies the step the call belongs to.
	StepID string
	// Err is the underlying failure.
	Err error
}

func (e *ToolError) Error() string {
	if e.ServerName != "" {
		return fmt.Sprintf("antigravity: tool %q on server %q failed: %v", e.ToolName, e.ServerName, e.Err)
	}
	return fmt.Sprintf("antigravity: tool %q failed: %v", e.ToolName, e.Err)
}

// Unwrap exposes the underlying failure to [errors.Is] and [errors.As].
func (e *ToolError) Unwrap() error { return e.Err }

// ConfigError reports an invalid configuration, naming the field at fault.
type ConfigError struct {
	// Field is the configuration field that failed validation.
	Field string
	// Err describes why it failed.
	Err error
}

func (e *ConfigError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("antigravity: invalid configuration: %v", e.Err)
	}
	return fmt.Sprintf("antigravity: invalid configuration for %s: %v", e.Field, e.Err)
}

func (e *ConfigError) Unwrap() error { return e.Err }

// Is reports ConfigError as matching [ErrInvalidConfig], so callers can test
// for any configuration problem without enumerating fields.
func (e *ConfigError) Is(target error) bool { return target == ErrInvalidConfig }

// HarnessError reports a failure to start or communicate with the harness
// subprocess. Stderr is included because it is usually the only diagnostic
// available when a handshake fails.
type HarnessError struct {
	// Op names the operation that failed, such as "handshake" or "dial".
	Op string
	// Stderr is whatever the harness wrote to standard error, possibly empty.
	Stderr string
	// Err is the underlying failure.
	Err error
}

func (e *HarnessError) Error() string {
	msg := fmt.Sprintf("antigravity: harness %s failed: %v", e.Op, e.Err)
	if e.Stderr != "" {
		msg += "\nharness stderr:\n" + e.Stderr
	}
	return msg
}

func (e *HarnessError) Unwrap() error { return e.Err }

// Is reports HarnessError as matching [ErrConnection].
func (e *HarnessError) Is(target error) bool { return target == ErrConnection }
