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

// Command agent_middleware stacks hooks into a middleware chain.
//
// Hooks are to an agent what interceptors are to a gRPC server: composable,
// transparent, and testable on their own. The agent calls tools the way it
// always does, while the hooks below rate-limit them, write an audit trail,
// and turn one specific failure into advice the model can act on. None of that
// appears in the prompt.
//
// Ordering matters, and one consequence is worth knowing: the tool error hook
// runs on the failure path instead of the post-tool-call hook, not before it,
// so recovered errors never reach the audit log.
//
//	go run ./examples/deep_dives/agent_middleware
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Tools, kept trivial so the hooks are what stands out
// ---------------------------------------------------------------------------

type lookupArgs struct {
	Email string `json:"email" jsonschema:"description=The user's email address."`
}

func lookupUser(_ context.Context, args lookupArgs) (string, error) {
	return fmt.Sprintf("User profile for %s: name=Alice, role=engineer, team=infra", args.Email), nil
}

type notifyArgs struct {
	To      string `json:"to" jsonschema:"description=The recipient's email address."`
	Message string `json:"message" jsonschema:"description=The notification body."`
}

func sendNotification(_ context.Context, args notifyArgs) (string, error) {
	return fmt.Sprintf("Notification sent to %s: %s", args.To, args.Message), nil
}

type unknownArgs struct {
	Name    string `json:"name" jsonschema:"description=The recipient's display name."`
	Message string `json:"message" jsonschema:"description=The message body."`
}

// sendToUnknown always fails, which is the point: it is what the fallback hook
// recovers from.
func sendToUnknown(_ context.Context, args unknownArgs) (string, error) {
	return "", fmt.Errorf("could not resolve %q to an email address", args.Name)
}

// ---------------------------------------------------------------------------
// Middleware 1: rate limiting, as a pre-tool-call hook
// ---------------------------------------------------------------------------

// rateLimiter caps how often each tool may run inside a sliding window. Hooks
// are invoked from the connection's read loop, so the state is mutex-guarded.
type rateLimiter struct {
	max    int
	window time.Duration

	mu    sync.Mutex
	calls map[string][]time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, calls: map[string][]time.Time{}}
}

func (r *rateLimiter) hook(_ context.Context, _ *antigravity.HookContext, call antigravity.ToolCall) (antigravity.ToolDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	kept := r.calls[call.Name][:0]
	for _, t := range r.calls[call.Name] {
		if now.Sub(t) < r.window {
			kept = append(kept, t)
		}
	}
	r.calls[call.Name] = kept

	if len(kept) >= r.max {
		reason := fmt.Sprintf("Rate limit exceeded: %s called %d times in %s",
			call.Name, r.max, r.window)
		fmt.Printf("  [RateLimit] denied %s\n", call.Name)
		// Denying returns the reason to the model in place of a result, so it
		// can explain the gap rather than retrying forever.
		return antigravity.ToolDecision{Deny: true, Reason: reason}, nil
	}

	r.calls[call.Name] = append(kept, now)
	return antigravity.ToolDecision{}, nil
}

// ---------------------------------------------------------------------------
// Middleware 2: the audit trail, as a post-tool-call hook
// ---------------------------------------------------------------------------

type auditEntry struct {
	tool   string
	result string
	err    error
}

type auditLog struct {
	mu      sync.Mutex
	entries []auditEntry
}

func (a *auditLog) hook(_ context.Context, _ *antigravity.HookContext, result antigravity.ToolResult) error {
	entry := auditEntry{
		tool:   result.Name,
		result: fmt.Sprint(result.Result),
		err:    result.Err,
	}

	a.mu.Lock()
	a.entries = append(a.entries, entry)
	a.mu.Unlock()

	fmt.Printf("  [Audit] %s %s: %s\n", status(entry.err), entry.tool, entry.result)
	return nil
}

func (a *auditLog) snapshot() []auditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]auditEntry(nil), a.entries...)
}

func status(err error) string {
	if err != nil {
		return "[fail]"
	}
	return "[ok]  "
}

// ---------------------------------------------------------------------------
// Middleware 3: error recovery, as a tool error hook
// ---------------------------------------------------------------------------

// fallback replaces the error the model would otherwise see. It cannot retry
// the call, but it can point the model at the tool that would have worked.
// Returning "" leaves the harness's default formatting in place.
func fallback(_ context.Context, _ *antigravity.HookContext, err *antigravity.ToolError) (string, error) {
	fmt.Printf("  [Fallback] caught: %v\n", err.Err)

	if strings.Contains(err.Err.Error(), "could not resolve") {
		return "[Could not find that user. Use the lookup_user tool with " +
			"their email address instead of their display name.]", nil
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func run(ctx context.Context) error {
	limiter := newRateLimiter(3, time.Minute)
	audit := &auditLog{}

	agent, err := antigravity.New(ctx,
		antigravity.WithSystemPrompt("You have access to user lookup, "+
			"notification, and diagnostic tools. Use them as needed. Keep "+
			"responses under 2 sentences."),
		antigravity.WithTools(
			antigravity.MustNewTool("lookup_user",
				"Look up a user by email address and return their profile.", lookupUser),
			antigravity.MustNewTool("send_notification",
				"Send a notification message to a user.", sendNotification),
			antigravity.MustNewTool("send_to_unknown",
				"Send a message to a user by display name. May fail if the name is ambiguous.", sendToUnknown),
		),
		antigravity.WithPreToolCallHook(limiter.hook),
		antigravity.WithPostToolCallHook(audit.hook),
		antigravity.WithToolErrorHook(fallback),
	)
	if err != nil {
		return err
	}
	defer agent.Close()

	prompts := []struct {
		title  string
		prompt string
	}{{
		"Normal tool use, audit logged",
		"Send a notification to bob@company.org saying 'Welcome aboard!'.",
	}, {
		"Error recovery",
		"Send a message to 'Charlie' saying 'Hey, are you free tomorrow?'",
	}, {
		// Four lookups against a limit of three: the last one is denied.
		"Rate limiting",
		"Look up user1@test.com, then user2@test.com, then user3@test.com, " +
			"then user4@test.com. Use the lookup_user tool for each one.",
	}}

	for _, p := range prompts {
		fmt.Printf("\n%s\n%s\n%s\n", strings.Repeat("=", 60), p.title, strings.Repeat("=", 60))

		resp, err := agent.Chat(ctx, antigravity.Text(p.prompt))
		if err != nil {
			return err
		}
		text, err := resp.Wait()
		if err != nil {
			return err
		}
		fmt.Printf("\n  Agent: %s\n", strings.TrimSpace(text))
	}

	entries := audit.snapshot()
	fmt.Printf("\n%s\nAudit log (%d entries)\n%s\n", strings.Repeat("=", 60), len(entries), strings.Repeat("=", 60))
	for i, e := range entries {
		fmt.Printf("  %d. %s %s: %s\n", i+1, status(e.err), e.tool, e.result)
	}
	return nil
}
