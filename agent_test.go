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
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// The localharness binary is not something these tests can rely on, so New is
// exercised only up to the point where it would launch it. Everything past
// that point is covered through the fake transport instead.

func TestNewRejectsAnInvalidConfiguration(t *testing.T) {
	// Validation has to happen before the subprocess is launched, so a
	// misconfiguration never leaves a harness running.
	_, err := New(t.Context(), WithPolicies())
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsAnUnusablePolicySet(t *testing.T) {
	// An ask-user policy with no handler cannot be honored, and the enforcer
	// is built before the harness is started.
	_, err := New(t.Context(), WithPolicies(One(Policy{
		Tool:     "run_command",
		Decision: DecisionAskUser,
	})))
	if err == nil {
		t.Fatal("an ask-user policy with no handler was accepted")
	}
}

func TestNewReportsAMissingHarness(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")

	_, err := New(t.Context(),
		WithPolicies(One(AllowAll())),
		WithBinaryPath(filepath.Join(t.TempDir(), "localharness")),
	)
	if err == nil {
		t.Fatal("New succeeded with a binary path that does not exist")
	}
}

func TestNewReportsAnUndiscoverableHarness(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "test-key")
	// Point the discovery variable at nothing, so neither it nor PATH can
	// produce a binary.
	t.Setenv("PATH", t.TempDir())
	t.Setenv(harness.PathEnvVar, filepath.Join(t.TempDir(), "absent"))

	_, err := New(t.Context(), WithPolicies(One(AllowAll())))
	if !errors.Is(err, ErrHarnessNotFound) {
		t.Fatalf("error = %v, want ErrHarnessNotFound", err)
	}
}

// TestInitializeErrorReportsStderrOnce guards the presentation of the one error
// most users will ever see from New. Both layers carry the harness's stderr, so
// nesting them printed it twice and pushed the explanation off the top of the
// message.
func TestInitializeErrorReportsStderrOnce(t *testing.T) {
	const stderr = "Failed to parse initial message"

	err := initializeError(&harness.StartError{
		Err:    errors.New("waiting for the initialize response: EOF"),
		Stderr: stderr,
	})
	if !errors.Is(err, ErrConnection) {
		t.Errorf("error = %v, want it to match ErrConnection", err)
	}
	if got := strings.Count(err.Error(), stderr); got != 1 {
		t.Errorf("the harness stderr appears %d times in %q, want once", got, err)
	}
	if !strings.Contains(err.Error(), "waiting for the initialize response") {
		t.Errorf("error = %q, want the underlying failure kept", err)
	}
}

func TestNewIgnoresNilOptions(t *testing.T) {
	// Callers build option slices conditionally; a nil entry should not panic.
	t.Setenv("GEMINI_API_KEY", "test-key")

	_, err := New(t.Context(), nil, WithBinaryPath(filepath.Join(t.TempDir(), "absent")))
	if err == nil {
		t.Fatal("New succeeded without a harness binary")
	}
}

func TestAgentBeforeStartup(t *testing.T) {
	// A zero Agent has no conversation, which every entry point must say
	// rather than dereference.
	var a Agent

	if _, err := a.Chat(t.Context(), Text("hello")); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Chat = %v, want ErrNotStarted", err)
	}
	if err := a.Send(t.Context(), Text("hello")); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Send = %v, want ErrNotStarted", err)
	}
	if a.Conversation() != nil {
		t.Error("Conversation is non-nil before startup")
	}
}

// fakeAgent assembles an Agent over a fake transport, skipping the subprocess
// launch and handshake that New would perform.
func fakeAgent(t *testing.T) (*Agent, *harness.FakeTransport) {
	t.Helper()

	fake := harness.NewFakeTransport()
	cfg := mustResolve(t, WithAPIKey("test-key"))
	cfg.logger = slog.New(slog.DiscardHandler)

	proc := newEventProcessor(fake, nil, nil, cfg.hooks, cfg.logger)
	a := &Agent{
		cfg:       cfg,
		proc:      proc,
		conv:      newConversation(proc, nil, cfg.maxHistory),
		transport: fake,
		cascadeID: "conv-1",
	}
	proc.start(context.WithoutCancel(t.Context()))
	return a, fake
}

func TestAgentClose(t *testing.T) {
	a, fake := fakeAgent(t)
	// Queued before the request, since the fake replays in order and Close
	// waits for the acknowledgement.
	fake.Push(pb.OutputEvent_builder{SessionEndResponse: proto.Bool(true)}.Build())

	done := make(chan error, 1)
	go func() { done <- a.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned")
	}

	// The session end is requested before the transport goes away, so hooks
	// run and state is flushed.
	var requested bool
	for _, ev := range fake.Sent() {
		if ev.GetSessionEndRequest() {
			requested = true
		}
	}
	if !requested {
		t.Error("Close did not ask the harness to end the session")
	}

	// Closing again is a no-op rather than a second teardown.
	if err := a.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestAgentCloseAfterTheHarnessHungUp(t *testing.T) {
	// A harness that has already gone away cannot acknowledge anything, and
	// Close must tear down regardless rather than wait for it.
	a, fake := fakeAgent(t)
	fake.Close()

	done := make(chan error, 1)
	go func() { done <- a.Close() }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung waiting for an acknowledgement that never came")
	}
}

func TestAgentConversationID(t *testing.T) {
	a, fake := fakeAgent(t)
	t.Cleanup(func() { fake.Close(); a.proc.stop() })

	if a.ConversationID() != "conv-1" {
		t.Errorf("ConversationID = %q, want the id the harness assigned", a.ConversationID())
	}
	if a.Conversation() == nil {
		t.Error("Conversation is nil for a started agent")
	}
}

func TestCollectTools(t *testing.T) {
	// A subagent's tool call arrives on the same connection, so the dispatch
	// table has to include it.
	cfg := newConfig()
	WithTools(noopTool(t, "root"))(cfg)
	WithSubagents(SubagentConfig{
		Name:  "researcher",
		Tools: []Tool{noopTool(t, "nested"), nil},
	})(cfg)

	tools := collectTools(cfg)
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want the root and subagent tools", tools)
	}
	for _, name := range []string{"root", "nested"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q is missing from the dispatch table", name)
		}
	}
}

func TestMCPServerNames(t *testing.T) {
	got := mcpServerNames([]MCPServer{
		NewMCPStdioServer("weather", "weatherd"),
		NewMCPHTTPServer("docs", "https://mcp.example"),
	})
	if len(got) != 2 || got[0] != "weather" || got[1] != "docs" {
		t.Errorf("names = %v, want both servers in order", got)
	}
	if got := mcpServerNames(nil); len(got) != 0 {
		t.Errorf("names = %v, want none", got)
	}
}

func TestRestoredHistory(t *testing.T) {
	if got := restoredHistory(pb.InitializeConversationResponse_builder{}.Build()); got != nil {
		t.Errorf("restoredHistory = %v, want nil for a fresh session", got)
	}

	got := restoredHistory(pb.InitializeConversationResponse_builder{
		History: []*pb.StepUpdate{
			textStep("main", 0, "earlier", true),
			textStep("main", 1, "later", true),
		},
	}.Build())
	if len(got) != 2 || got[0].Content != "earlier" || got[1].Content != "later" {
		t.Errorf("history = %+v, want both replayed steps in order", got)
	}
}

func TestRestoredTrajectoryUsage(t *testing.T) {
	if got := restoredTrajectoryUsage(pb.InitializeConversationResponse_builder{}.Build()); got != nil {
		t.Errorf("usage = %v, want nil for a fresh session", got)
	}

	got := restoredTrajectoryUsage(pb.InitializeConversationResponse_builder{
		TrajectoryUsage: []*pb.TrajectoryUsageEntry{
			pb.TrajectoryUsageEntry_builder{
				TrajectoryId: proto.String("main"),
				Usage:        pb.UsageMetadata_builder{TotalTokenCount: proto.Uint64(120)}.Build(),
			}.Build(),
			// An entry with no usage carries nothing to restore.
			pb.TrajectoryUsageEntry_builder{TrajectoryId: proto.String("sub")}.Build(),
		},
	}.Build())

	if len(got) != 1 {
		t.Fatalf("usage = %v, want only the entry that reported any", got)
	}
	if got["main"].TotalTokenCount != 120 {
		t.Errorf("usage = %v, want the restored total", got)
	}
}
