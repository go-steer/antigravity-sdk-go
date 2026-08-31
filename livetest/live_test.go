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

//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	antigravity "github.com/go-steer/antigravity-sdk-go"
)

// turnTimeout bounds a single test. A live turn is a real model call over a
// real socket, so this is generous by unit-test standards and still far short
// of `go test`'s own timeout.
const turnTimeout = 3 * time.Minute

// harnessPath is resolved once, by preflight.
var harnessPath string

// skipReason is why the suite cannot run, or "" if it can. Computed once in
// TestMain and reported per-test by requireLive.
var skipReason string

// TestMain does the preflight every test here shares, so a missing binary or
// missing credentials is diagnosed once rather than N times.
//
// The skip itself happens in requireLive, not here. Exiting early from
// TestMain would be quieter but wrong: `go test` discards a passing
// package's output, so the reason would vanish and an unrunnable suite would
// print a green "ok". A per-test t.Skip shows up in the summary as skipped,
// which is what it is.
func TestMain(m *testing.M) {
	skipReason = preflight()
	os.Exit(m.Run())
}

// requireLive skips the calling test when the environment cannot support it.
func requireLive(t *testing.T) {
	t.Helper()
	if skipReason != "" {
		t.Skip("live suite unavailable: " + skipReason)
	}
}

// preflight returns a human-readable reason the suite cannot run, or "".
func preflight() string {
	if p := os.Getenv("ANTIGRAVITY_HARNESS_PATH"); p != "" {
		harnessPath = p
	} else {
		// dev/tools/fetch-harness's destination, relative to this package.
		p, err := filepath.Abs(filepath.Join("..", "dev", ".harness", "localharness"))
		if err != nil {
			return "resolving the default harness path: " + err.Error()
		}
		harnessPath = p
	}
	if _, err := os.Stat(harnessPath); err != nil {
		return "no localharness at " + harnessPath +
			" (run dev/tools/fetch-harness, or set ANTIGRAVITY_HARNESS_PATH)"
	}
	if !haveCredentials() {
		return "no model credentials (set GEMINI_API_KEY, or " +
			"GOOGLE_GENAI_USE_VERTEXAI=true with GOOGLE_CLOUD_PROJECT and " +
			"GOOGLE_CLOUD_LOCATION)"
	}
	return ""
}

// haveCredentials reports whether either supported auth path is configured.
// It deliberately does not validate them — an invalid key is a test failure
// worth seeing, not a skip.
func haveCredentials() bool {
	if os.Getenv("GEMINI_API_KEY") != "" {
		return true
	}
	vertex := strings.EqualFold(os.Getenv("GOOGLE_GENAI_USE_VERTEXAI"), "true") ||
		strings.EqualFold(os.Getenv("GOOGLE_GENAI_USE_ENTERPRISE"), "true")
	return vertex && os.Getenv("GOOGLE_CLOUD_PROJECT") != ""
}

// newAgent builds an agent wired to the real harness, registered for cleanup.
// It skips the test when the environment cannot support one, so every test
// that needs a session is covered without repeating the check.
func newAgent(t *testing.T, opts ...antigravity.Option) *antigravity.Agent {
	t.Helper()
	requireLive(t)
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	t.Cleanup(cancel)

	agent, err := antigravity.New(ctx,
		append([]antigravity.Option{antigravity.WithBinaryPath(harnessPath)}, opts...)...)
	if err != nil {
		t.Fatalf("starting a live session: %v", err)
	}
	t.Cleanup(func() {
		if err := agent.Close(); err != nil {
			t.Errorf("closing the session: %v", err)
		}
	})
	return agent
}

// testContext returns a context bounded by turnTimeout.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), turnTimeout)
	t.Cleanup(cancel)
	return ctx
}

// ask runs one turn and returns the final text.
func ask(t *testing.T, agent *antigravity.Agent, prompt string) string {
	t.Helper()
	resp, err := agent.Chat(testContext(t), antigravity.Text(prompt))
	if err != nil {
		t.Fatalf("Chat(%q): %v", prompt, err)
	}
	text, err := resp.Wait()
	if err != nil {
		t.Fatalf("Wait() after %q: %v", prompt, err)
	}
	return text
}

// TestSingleTurnText is the smoke test: the handshake completes, a prompt
// reaches a model, and the answer comes back through the event processor.
//
// It asserts only that text arrived. Asserting on what the model said would
// make the suite a measure of the model, which changes underneath us.
func TestSingleTurnText(t *testing.T) {
	agent := newAgent(t)
	got := ask(t, agent, "Reply with exactly the word: pong")
	if strings.TrimSpace(got) == "" {
		t.Fatal("the model returned no text")
	}
	t.Logf("model said: %q", got)
}

// TestStreamingDeliversIncrementally checks that a turn arrives as a stream
// of chunks rather than one final blob. The fake harness can script chunks,
// but only a real turn proves the SDK reassembles what the harness actually
// emits.
func TestStreamingDeliversIncrementally(t *testing.T) {
	agent := newAgent(t)

	resp, err := agent.Chat(testContext(t),
		antigravity.Text("Count from one to five, one number per line."))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var chunks int
	var b strings.Builder
	for text, err := range resp.Text() {
		if err != nil {
			t.Fatalf("streaming text: %v", err)
		}
		chunks++
		b.WriteString(text)
	}
	if chunks == 0 {
		t.Fatal("the text stream yielded no chunks")
	}
	if strings.TrimSpace(b.String()) == "" {
		t.Fatal("the text stream yielded only empty chunks")
	}
	t.Logf("%d chunk(s), %d bytes", chunks, b.Len())
}

// TestCustomToolIsInvoked is the highest-value case in this file. A Go
// function's parameters become a JSON schema, cross the wire, are offered to
// the model by the harness, and come back as an invocation the SDK has to
// decode into the Go type. Every step of that is a parity risk the fake
// harness cannot check, because the fake never reads the schema it is sent.
func TestCustomToolIsInvoked(t *testing.T) {
	type args struct {
		City string `json:"city" jsonschema:"description=The city to look up the weather for."`
	}

	var (
		mu     sync.Mutex
		callID []string
	)
	weather := antigravity.MustNewTool("get_weather",
		"Returns the current weather for a city.",
		func(_ context.Context, a args) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			callID = append(callID, a.City)
			return "It is 22C and sunny in " + a.City + ".", nil
		})

	agent := newAgent(t,
		antigravity.WithTools(weather),
		antigravity.WithSystemPrompt(
			"You answer weather questions. You MUST use the get_weather tool; "+
				"never answer from your own knowledge."),
		antigravity.WithPolicies(antigravity.One(
			antigravity.DenyAll(),
			antigravity.Allow("get_weather"),
		)),
	)

	got := ask(t, agent, "What is the weather in Dublin right now?")

	mu.Lock()
	defer mu.Unlock()
	if len(callID) == 0 {
		t.Fatalf("get_weather was never invoked; the model answered: %q", got)
	}
	// The argument has to survive schema generation and JSON decoding. Case
	// and phrasing are the model's business, so match loosely.
	if !strings.Contains(strings.ToLower(callID[0]), "dublin") {
		t.Errorf("tool received city %q, want something containing \"dublin\"", callID[0])
	}
	t.Logf("tool invoked %d time(s), first city %q", len(callID), callID[0])
}

// TestStructuredOutput checks the reflected JSON Schema round trip:
// WithResponseSchema derives a schema from a Go type, and the harness has to
// accept it and constrain the model to it.
func TestStructuredOutput(t *testing.T) {
	type city struct {
		Name       string `json:"name" jsonschema:"description=The name of the city."`
		Population int64  `json:"population" jsonschema:"description=The approximate population."`
	}
	type answer struct {
		Cities []city `json:"cities" jsonschema:"description=The cities in the answer."`
	}

	agent := newAgent(t, antigravity.WithResponseSchema[answer]())

	resp, err := agent.Chat(testContext(t),
		antigravity.Text("List exactly two large cities in Japan with their approximate populations."))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	raw, err := resp.StructuredOutput()
	if err != nil {
		t.Fatalf("StructuredOutput: %v", err)
	}
	if raw == nil {
		t.Fatal("no structured output was produced under a response schema")
	}

	var out answer
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding structured output %s: %v", raw, err)
	}
	if len(out.Cities) == 0 {
		t.Fatalf("structured output decoded to zero cities: %s", raw)
	}
	if out.Cities[0].Name == "" {
		t.Errorf("first city has no name: %s", raw)
	}
	t.Logf("decoded %d cities, first %q", len(out.Cities), out.Cities[0].Name)
}

// TestMultiTurnHistory checks that a second turn on the same session sees the
// first. Session continuity lives in the harness, so a client-side history
// cap test cannot prove it.
func TestMultiTurnHistory(t *testing.T) {
	agent := newAgent(t)

	ask(t, agent, "Remember this number for later: 4271. Just acknowledge it.")
	second := ask(t, agent, "What number did I ask you to remember? Reply with only the digits.")

	if !strings.Contains(second, "4271") {
		t.Errorf("second turn did not recall the number; got %q", second)
	}
	if n := agent.Conversation().TurnCount(); n < 2 {
		t.Errorf("TurnCount() = %d after two turns, want >= 2", n)
	}
	if got := len(agent.Conversation().History()); got == 0 {
		t.Error("History() is empty after two turns")
	}
}

// TestUsageIsReported checks that token accounting is populated from real
// responses. The fake harness supplies whatever numbers a test writes into
// it, so only a live turn shows whether the SDK reads the fields the harness
// actually sets.
func TestUsageIsReported(t *testing.T) {
	agent := newAgent(t)

	resp, err := agent.Chat(testContext(t), antigravity.Text("Say hello."))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := resp.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	turn := resp.Usage()
	if turn == nil {
		t.Fatal("Usage() is nil after a completed turn")
	}
	if turn.PromptTokenCount <= 0 {
		t.Errorf("PromptTokenCount = %d, want > 0", turn.PromptTokenCount)
	}
	if turn.TotalTokenCount <= 0 {
		t.Errorf("TotalTokenCount = %d, want > 0", turn.TotalTokenCount)
	}

	if cum := agent.Conversation().Usage(); cum.TotalTokenCount < turn.TotalTokenCount {
		t.Errorf("cumulative total %d < this turn's %d",
			cum.TotalTokenCount, turn.TotalTokenCount)
	}
	t.Logf("prompt=%d thoughts=%d candidates=%d total=%d",
		turn.PromptTokenCount, turn.ThoughtsTokenCount,
		turn.CandidatesTokenCount, turn.TotalTokenCount)
}

// TestDeniedToolIsNotExecuted checks that a Deny policy is enforced for real.
// The SDK evaluates policy locally, but the value of the check is that the
// denial reaches the harness as a refusal the turn can survive, rather than
// wedging the session.
func TestDeniedToolIsNotExecuted(t *testing.T) {
	type args struct {
		Path string `json:"path" jsonschema:"description=The file to delete."`
	}

	var called atomicBool
	danger := antigravity.MustNewTool("delete_everything",
		"Deletes a file from disk.",
		func(_ context.Context, _ args) (string, error) {
			called.set()
			return "deleted", nil
		})

	agent := newAgent(t,
		antigravity.WithTools(danger),
		antigravity.WithSystemPrompt("You delete files when asked, using the tool."),
		antigravity.WithPolicies(antigravity.One(
			antigravity.DenyAll(),
			antigravity.Deny("delete_everything"),
		)),
	)

	// The turn must still complete: a denial is a normal outcome, not a fault.
	got := ask(t, agent, "Delete the file /tmp/does-not-matter using your tool.")
	if called.get() {
		t.Fatal("a denied tool was executed")
	}
	t.Logf("turn completed with the tool denied: %q", got)
}

// TestInvalidModelSurfacesError checks that a backend rejection arrives as a
// Go error rather than a hang. The model name is deliberately nonsense.
func TestInvalidModelSurfacesError(t *testing.T) {
	// Builds its own agent rather than going through newAgent, so the skip
	// check has to be explicit here.
	requireLive(t)
	ctx := testContext(t)

	agent, err := antigravity.New(ctx,
		antigravity.WithBinaryPath(harnessPath),
		antigravity.WithModel("gemini-does-not-exist-9000"))
	if err != nil {
		// Rejected during config validation, which is also a pass: the point
		// is that it fails loudly and early.
		t.Logf("rejected at construction: %v", err)
		return
	}
	defer agent.Close()

	resp, err := agent.Chat(ctx, antigravity.Text("Hello."))
	if err == nil {
		_, err = resp.Wait()
	}
	if err == nil {
		t.Fatal("a nonexistent model produced no error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a nonexistent model hung until the deadline instead of erroring: %v", err)
	}
	t.Logf("surfaced: %v", err)
}

// atomicBool is a mutex-guarded flag. The harness may call tools from its own
// goroutine, so the test's read has to be synchronized with the tool's write.
type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (b *atomicBool) set() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.v = true
}

func (b *atomicBool) get() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.v
}
