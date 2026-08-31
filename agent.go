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
	"fmt"
	"os"
	"sync"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/harness"
)

// Agent is a configured connection to the Antigravity harness, and the entry
// point to the SDK.
//
// Create one with [New], use it, and [Agent.Close] it when done:
//
//	agent, err := antigravity.New(ctx,
//		antigravity.WithSystemPrompt("You are a careful code reviewer."),
//		antigravity.WithWorkspaces("."),
//	)
//	if err != nil {
//		return err
//	}
//	defer agent.Close()
//
//	resp, err := agent.Chat(ctx, antigravity.Text("What does main.go do?"))
//
// An Agent owns one [Conversation], which holds the session's history and is
// available from [Agent.Conversation] for anything beyond a plain exchange.
// The Agent is safe for concurrent use, though turns are serialized: a second
// Chat waits for the first to finish.
type Agent struct {
	cfg      *config
	enforcer *Enforcer
	proc     *eventProcessor
	conv     *Conversation
	triggers *triggerRunner

	transport harness.Transport
	cascadeID string

	closeOnce sync.Once
	closeErr  error
}

// New starts a harness session and returns the agent driving it.
//
// It launches the localharness subprocess, completes the handshake, and sends
// the whole configuration in one message, so any misconfiguration surfaces
// here rather than mid-conversation. On failure the subprocess is cleaned up
// before returning.
//
// The default configuration enables every built-in tool but denies
// run_command by policy. See [WithCapabilities] and [WithPolicies] to change
// that, and note that an agent with write tools and no supervision at all is
// rejected: see [WithPolicies] for the ways to satisfy it.
func New(ctx context.Context, opts ...Option) (_ *Agent, err error) {
	cfg := newConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if err := cfg.resolve(); err != nil {
		return nil, err
	}

	enforcer, err := NewEnforcer(cfg.policies, mcpServerNames(cfg.mcpServers))
	if err != nil {
		return nil, err
	}
	enforcer.log = cfg.logger

	binary := cfg.binaryPath
	if binary == "" {
		binary, err = harness.FindBinary(cfg.env)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrHarnessNotFound, err)
		}
	}

	saveDir := cfg.saveDir
	if saveDir == "" {
		saveDir, err = os.MkdirTemp("", "antigravity-")
		if err != nil {
			return nil, fmt.Errorf("%w: creating a session directory: %w", ErrConnection, err)
		}
		cfg.logger.Debug("no save directory configured; using a temporary one", "path", saveDir)
	}

	proc, err := harness.Start(ctx, harness.Options{
		BinaryPath:       binary,
		StorageDirectory: saveDir,
		Env:              cfg.env,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			// Cleanup on a failed construction: the error being returned is
			// the one the caller needs, not whatever teardown reports.
			_ = proc.Close()
		}
	}()

	a := &Agent{
		cfg:       cfg,
		enforcer:  enforcer,
		transport: proc,
	}
	a.proc = newEventProcessor(proc, collectTools(cfg), enforcer, cfg.hooks, cfg.logger)

	resp, err := proc.Initialize(ctx, cfg.harnessConfig(enforcer))
	if err != nil {
		return nil, &HarnessError{Op: "initialize", Stderr: proc.Stderr(), Err: err}
	}
	a.cascadeID = resp.GetCascadeId()

	history := restoredHistory(resp)
	a.proc.seedUsage(usageFromProto(resp.GetCumulativeUsage()), restoredTrajectoryUsage(resp))
	a.conv = newConversation(a.proc, history, cfg.maxHistory)

	// Neither the read loop nor the triggers may be tied to the caller's ctx:
	// that context bounds startup, while both have to outlive it and run until
	// Close.
	sessionCtx := context.WithoutCancel(ctx)
	a.proc.start(sessionCtx)
	a.triggers = startTriggers(sessionCtx, cfg.triggers, a.conv, cfg.logger)
	return a, nil
}

// Chat sends a prompt and returns the streaming response.
//
// It is shorthand for [Conversation.Chat] on the agent's conversation, which
// is where the history lives.
func (a *Agent) Chat(ctx context.Context, prompt ...Content) (*ChatResponse, error) {
	if a.conv == nil {
		return nil, ErrNotStarted
	}
	return a.conv.Chat(ctx, prompt...)
}

// Send delivers a prompt without consuming the response, leaving the caller to
// read it from [Conversation.Steps] or [Conversation.Chunks].
func (a *Agent) Send(ctx context.Context, prompt ...Content) error {
	if a.conv == nil {
		return ErrNotStarted
	}
	return a.conv.Send(ctx, prompt...)
}

// Conversation returns the session's conversation, which carries the history,
// usage totals, and the lower-level streaming API.
func (a *Agent) Conversation() *Conversation { return a.conv }

// ConversationID is the identifier the harness assigned this session. Pass it
// to [WithConversationID], alongside the same [WithSaveDir], to resume later.
func (a *Agent) ConversationID() string { return a.cascadeID }

// Close ends the session and shuts down the harness subprocess.
//
// It stops the triggers first, so none of them sends into a session that is
// going away, then asks the harness to end the session cleanly, so
// session-end hooks run and state is flushed, then tears down the transport
// regardless of whether that succeeded. Calling it more than once is safe.
func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		a.triggers.stop()

		ctx := context.Background()
		if err := a.proc.requestSessionEnd(ctx); err != nil {
			a.cfg.logger.Debug("the harness did not acknowledge the session end", "error", err)
		}
		a.closeErr = a.transport.Close()
		a.proc.stop()
	})
	return a.closeErr
}

// ---------------------------------------------------------------------------
// Startup helpers
// ---------------------------------------------------------------------------

// collectTools indexes every custom tool the session may dispatch, from the
// root agent and from each subagent, since a subagent's tool call arrives on
// the same connection.
func collectTools(cfg *config) map[string]Tool {
	tools := make(map[string]Tool, len(cfg.tools))
	for _, t := range cfg.tools {
		tools[t.Name()] = t
	}
	for _, s := range cfg.subagents {
		for _, t := range s.Tools {
			if t != nil {
				tools[t.Name()] = t
			}
		}
	}
	return tools
}

func mcpServerNames(servers []MCPServer) []string {
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name())
	}
	return names
}

// restoredHistory converts the steps a resumed session replayed at startup.
func restoredHistory(resp *pb.InitializeConversationResponse) []Step {
	raw := resp.GetHistory()
	if len(raw) == 0 {
		return nil
	}
	steps := make([]Step, 0, len(raw))
	for _, su := range raw {
		steps = append(steps, stepFromProto(su))
	}
	return steps
}

// restoredTrajectoryUsage converts the per-trajectory usage a resumed session
// reported, so totals continue from where they left off.
func restoredTrajectoryUsage(resp *pb.InitializeConversationResponse) map[string]UsageMetadata {
	entries := resp.GetTrajectoryUsage()
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]UsageMetadata, len(entries))
	for _, e := range entries {
		if u := usageFromProto(e.GetUsage()); u != nil {
			out[e.GetTrajectoryId()] = *u
		}
	}
	return out
}
