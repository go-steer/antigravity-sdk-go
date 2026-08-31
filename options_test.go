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
	"slices"
	"strings"
	"testing"
	"time"
)

// resolved applies options to a fresh config and resolves it, which is what
// [New] does before it touches the harness.
//
// The ambient environment decides both the default platform and whether a
// Gemini endpoint validates, so it is pinned here: otherwise these tests would
// pass or fail depending on the developer's shell.
func resolved(t *testing.T, opts ...Option) (*config, error) {
	t.Helper()

	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
	t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "")

	c := newConfig()
	for _, opt := range opts {
		opt(c)
	}
	return c, c.resolve()
}

// mustResolve fails the test if the configuration is rejected.
func mustResolve(t *testing.T, opts ...Option) *config {
	t.Helper()

	c, err := resolved(t, opts...)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return c
}

// noopTool is a registered tool the model could call.
func noopTool(t *testing.T, name string) Tool {
	t.Helper()

	tool, err := NewTool(name, "does nothing", func(context.Context, struct{}) (string, error) {
		return "", nil
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	return tool
}

func TestDefaultConfigIsUsable(t *testing.T) {
	c := mustResolve(t)

	// A default agent can read and write files but cannot run commands
	// unsupervised.
	if len(c.policies) == 0 {
		t.Error("the default configuration has no policies")
	}
	if c.maxHistory <= 0 {
		t.Errorf("maxHistory = %d, want a positive default", c.maxHistory)
	}
	if c.logger == nil {
		t.Error("logger is nil after resolve")
	}
	if !filepath.IsAbs(c.appDataDir) {
		t.Errorf("appDataDir = %q, want an absolute path", c.appDataDir)
	}
	if len(c.workspaces) != 1 {
		t.Errorf("workspaces = %v, want the working directory", c.workspaces)
	}
}

func TestOptionsAccumulateAndReplace(t *testing.T) {
	c := mustResolve(t,
		WithTools(noopTool(t, "first")),
		WithTools(noopTool(t, "second")),
		WithSkillsPaths("/a"),
		WithSkillsPaths("/b"),
		WithModel("first-model"),
		WithModel("second-model"),
		WithMaxHistory(11),
	)

	// Registrations accumulate.
	if len(c.tools) != 2 {
		t.Errorf("got %d tools, want both registrations", len(c.tools))
	}
	if !slices.Equal(c.skillsPaths, []string{"/a", "/b"}) {
		t.Errorf("skillsPaths = %v, want both", c.skillsPaths)
	}
	// Single values replace.
	if c.model != "second-model" {
		t.Errorf("model = %q, want the last one set", c.model)
	}
	if c.maxHistory != 11 {
		t.Errorf("maxHistory = %d, want 11", c.maxHistory)
	}
}

func TestWithMaxHistoryZeroRestoresTheDefault(t *testing.T) {
	c := mustResolve(t, WithMaxHistory(0))
	if c.maxHistory != defaultMaxHistory {
		t.Errorf("maxHistory = %d, want the default %d", c.maxHistory, defaultMaxHistory)
	}
}

func TestWithEnvMerges(t *testing.T) {
	c := mustResolve(t,
		WithEnv(map[string]string{"A": "1", "B": "2"}),
		WithEnv(map[string]string{"B": "overridden", "C": "3"}),
	)

	want := map[string]string{"A": "1", "B": "overridden", "C": "3"}
	for k, v := range want {
		if c.env[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, c.env[k], v)
		}
	}
}

func TestWithSystemPromptAppendsASection(t *testing.T) {
	c := mustResolve(t, WithSystemPrompt("be terse"))

	templated, ok := c.instructions.(TemplatedInstructions)
	if !ok {
		t.Fatalf("instructions are %T, want TemplatedInstructions", c.instructions)
	}
	if len(templated.Sections) != 1 || templated.Sections[0].Content != "be terse" {
		t.Errorf("sections = %v, want the prompt as one section", templated.Sections)
	}
}

func TestResolveRejectsDuplicateTools(t *testing.T) {
	_, err := resolved(t, WithTools(noopTool(t, "same"), noopTool(t, "same")))
	if err == nil {
		t.Fatal("two tools with one name were accepted")
	}
	assertConfigField(t, err, "tools")
}

func TestResolveRejectsANilTool(t *testing.T) {
	_, err := resolved(t, WithTools(nil))
	if err == nil {
		t.Fatal("a nil tool was accepted")
	}
	assertConfigField(t, err, "tools")
}

func TestResolveRejectsDuplicateSubagents(t *testing.T) {
	sub := SubagentConfig{Name: "researcher", Description: "reads"}
	_, err := resolved(t, WithSubagents(sub, sub))
	if err == nil {
		t.Fatal("two subagents with one name were accepted")
	}
	assertConfigField(t, err, "subagents")
}

func TestResolveRejectsAnUnnamedSubagent(t *testing.T) {
	_, err := resolved(t, WithSubagents(SubagentConfig{Description: "nameless"}))
	if err == nil {
		t.Fatal("a subagent with no name was accepted")
	}
	assertConfigField(t, err, "subagents")
}

func TestResolveRejectsAnUndeclaredAllowedSubagent(t *testing.T) {
	// A typo in an allowlist would otherwise make a subagent silently
	// unreachable.
	caps := DefaultCapabilities()
	caps.AllowedSubagents = []string{"reasercher"} //nolint:misspell // The typo is the point.

	_, err := resolved(t,
		WithSubagents(SubagentConfig{Name: "researcher", Description: "reads"}),
		WithCapabilities(caps),
	)
	if err == nil {
		t.Fatal("an allowlist naming an undeclared subagent was accepted")
	}
	if !strings.Contains(err.Error(), "researcher") {
		t.Errorf("error = %v, want it to list the declared subagents", err)
	}
}

func TestResolveAcceptsADeclaredAllowedSubagent(t *testing.T) {
	caps := DefaultCapabilities()
	caps.AllowedSubagents = []string{"researcher"}

	mustResolve(t,
		WithSubagents(SubagentConfig{Name: "researcher", Description: "reads"}),
		WithCapabilities(caps),
	)
}

func TestResolveRejectsDuplicateMCPServers(t *testing.T) {
	_, err := resolved(t,
		WithMCPServers(
			NewMCPStdioServer("weather", "weatherd"),
			NewMCPStdioServer("weather", "other"),
		),
		WithPolicies(One(AllowAll())),
	)
	if err == nil {
		t.Fatal("two MCP servers with one name were accepted")
	}
	assertConfigField(t, err, "mcpServers")
}

func TestResolveRejectsAnIncompleteMCPServer(t *testing.T) {
	_, err := resolved(t,
		WithMCPServers(&MCPStdioServer{}),
		WithPolicies(One(AllowAll())),
	)
	if err == nil {
		t.Fatal("an MCP server with no name or command was accepted")
	}
	assertConfigField(t, err, "mcpServers")
}

func TestResolveRejectsConflictingToolLists(t *testing.T) {
	_, err := resolved(t, WithCapabilities(CapabilitiesConfig{
		EnableSubagents: true,
		EnabledTools:    []BuiltinTool{ToolViewFile},
		DisabledTools:   []BuiltinTool{ToolRunCommand},
	}))
	if err == nil {
		t.Fatal("an allowlist and a denylist together were accepted")
	}
	assertConfigField(t, err, "capabilities")
}

func TestResolveWorkspaces(t *testing.T) {
	dir := t.TempDir()
	c := mustResolve(t, WithWorkspaces(dir, "file://"+dir))

	if len(c.workspaces) != 2 {
		t.Fatalf("workspaces = %v, want both", c.workspaces)
	}
	for _, w := range c.workspaces {
		if w != dir {
			t.Errorf("workspace = %q, want the resolved %q", w, dir)
		}
	}
}

func TestResolveWorkspacesDropsEmptyPaths(t *testing.T) {
	c := mustResolve(t, WithWorkspaces(""))
	if len(c.workspaces) != 0 {
		t.Errorf("workspaces = %v, want none: an empty path names nothing", c.workspaces)
	}
}

func TestResolveAppDataDirMustBeAbsolute(t *testing.T) {
	_, err := resolved(t, WithAppDataDir("relative/dir"))
	if err == nil {
		t.Fatal("a relative app data directory was accepted")
	}
	assertConfigField(t, err, "appDataDir")

	abs := filepath.Join(t.TempDir(), "state")
	c := mustResolve(t, WithAppDataDir(abs))
	if c.appDataDir != abs {
		t.Errorf("appDataDir = %q, want %q", c.appDataDir, abs)
	}
}

func TestResolveModelsFillsBothModalities(t *testing.T) {
	c := mustResolve(t, WithModel("my-text-model"))

	var text, image []string
	for _, m := range c.models {
		if m.Endpoint == nil {
			t.Errorf("model %q has no endpoint", m.Name)
		}
		for _, kind := range m.modelTypes() {
			switch kind {
			case ModelTypeText:
				text = append(text, m.Name)
			case ModelTypeImage:
				image = append(image, m.Name)
			}
		}
	}

	if !slices.Contains(text, "my-text-model") {
		t.Errorf("text models = %v, want the configured one", text)
	}
	// Naming a text model must not leave the agent without an image model.
	if len(image) != 1 || image[0] != DefaultImageGenerationModel {
		t.Errorf("image models = %v, want the package default", image)
	}
}

func TestResolveModelsKeepsAnExplicitEndpoint(t *testing.T) {
	endpoint := &GeminiAPIEndpoint{APIKey: "explicit"}
	c := mustResolve(t,
		WithAPIKey("shorthand"),
		WithModels(ModelTarget{
			Name:     "custom",
			Types:    []ModelType{ModelTypeText},
			Endpoint: endpoint,
		}),
	)

	for _, m := range c.models {
		if m.Name != "custom" {
			continue
		}
		if m.Endpoint != endpoint {
			t.Errorf("endpoint = %v, want the one the caller supplied", m.Endpoint)
		}
		return
	}
	t.Error("the configured model is missing from the resolved set")
}

func TestResolveModelsUsesVertexWhenSelected(t *testing.T) {
	c := mustResolve(t, WithVertex("my-project", "us-central1"))

	for _, m := range c.models {
		vertex, ok := m.Endpoint.(*VertexEndpoint)
		if !ok {
			t.Fatalf("model %q has a %T endpoint, want *VertexEndpoint", m.Name, m.Endpoint)
		}
		if vertex.Project != "my-project" || vertex.Location != "us-central1" {
			t.Errorf("endpoint = %+v, want the configured project and location", vertex)
		}
	}
}

func TestVertexFromEnv(t *testing.T) {
	for _, name := range []string{"GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_ENTERPRISE"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "")
			t.Setenv("GOOGLE_GENAI_USE_ENTERPRISE", "")

			t.Setenv(name, "TRUE")
			if !vertexFromEnv() {
				t.Errorf("%s=TRUE did not select the enterprise platform", name)
			}
			t.Setenv(name, "no")
			if vertexFromEnv() {
				t.Errorf("%s=no selected the enterprise platform", name)
			}
		})
	}
}

func TestCheckSupervision(t *testing.T) {
	tests := []struct {
		name    string
		opts    []Option
		wantErr bool
	}{
		{
			name:    "write tools with no supervision",
			opts:    []Option{WithPolicies()},
			wantErr: true,
		},
		{
			name: "read-only needs none",
			opts: []Option{WithPolicies(), WithCapabilities(ReadOnlyCapabilities())},
		},
		{
			name: "a pre-tool hook supervises",
			opts: []Option{
				WithPolicies(),
				WithPreToolCallHook(func(context.Context, *HookContext, ToolCall) (ToolDecision, error) {
					return ToolDecision{}, nil
				}),
			},
		},
		{
			name: "policies supervise",
			opts: []Option{WithPolicies(One(AllowAll()))},
		},
		{
			// An MCP server's tools are as unbounded as a write tool.
			name: "read-only with an MCP server still needs supervision",
			opts: []Option{
				WithPolicies(),
				WithCapabilities(ReadOnlyCapabilities()),
				WithMCPServers(NewMCPStdioServer("weather", "weatherd")),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolved(t, tt.opts...)
			if tt.wantErr && err == nil {
				t.Fatal("an unsupervised agent was accepted")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("resolve: %v", err)
			}
		})
	}
}

func TestWithResponseSchema(t *testing.T) {
	type answer struct {
		Verdict string `json:"verdict"`
	}
	c := mustResolve(t, WithResponseSchema[answer]())

	if !strings.Contains(c.capabilities.FinishToolSchemaJSON, "verdict") {
		t.Errorf("schema = %s, want the struct's fields", c.capabilities.FinishToolSchemaJSON)
	}
}

func TestWithResponseSchemaReportsAnUnrepresentableType(t *testing.T) {
	// An Option cannot return an error, so the failure has to reach the caller
	// from resolve instead of being swallowed.
	_, err := resolved(t, WithResponseSchema[func()]())
	if err == nil {
		t.Fatal("a type with no JSON schema was accepted")
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("error = %T, want *ConfigError", err)
	}
}

func TestWithResponseSchemaJSON(t *testing.T) {
	const raw = `{"type":"object"}`
	c := mustResolve(t, WithResponseSchemaJSON(raw))

	if c.capabilities.FinishToolSchemaJSON != raw {
		t.Errorf("schema = %s, want it passed through", c.capabilities.FinishToolSchemaJSON)
	}
}

func TestHookOptionsRegisterInOrder(t *testing.T) {
	c := newConfig()
	for _, opt := range []Option{
		WithSessionStartHook(func(context.Context, *HookContext) error { return nil }),
		WithSessionEndHook(func(context.Context, *HookContext) error { return nil }),
		WithPreTurnHook(func(context.Context, *HookContext, []Content) (TurnDecision, error) {
			return TurnDecision{}, nil
		}),
		WithPostTurnHook(func(context.Context, *HookContext, string) error { return nil }),
		WithPreToolCallHook(func(context.Context, *HookContext, ToolCall) (ToolDecision, error) {
			return ToolDecision{}, nil
		}),
		WithPostToolCallHook(func(context.Context, *HookContext, ToolResult) error { return nil }),
		WithToolErrorHook(func(context.Context, *HookContext, *ToolError) (string, error) {
			return "", nil
		}),
		WithInteractionHook(func(context.Context, *HookContext, QuestionRequest) (*QuestionAnswers, error) {
			return nil, nil
		}),
		WithCompactionHook(func(context.Context, *HookContext, Step) error { return nil }),
		WithStopHook(func(context.Context, *HookContext, StopArgs) (StopDecision, error) {
			return StopDecision{}, nil
		}),
		WithStepObserver(func(context.Context, *HookContext, Step) {}),
	} {
		opt(c)
	}

	counts := map[string]int{
		"sessionStart": len(c.hooks.sessionStart),
		"sessionEnd":   len(c.hooks.sessionEnd),
		"preTurn":      len(c.hooks.preTurn),
		"postTurn":     len(c.hooks.postTurn),
		"preTool":      len(c.hooks.preTool),
		"postTool":     len(c.hooks.postTool),
		"toolError":    len(c.hooks.toolError),
		"interaction":  len(c.hooks.interaction),
		"compaction":   len(c.hooks.compaction),
		"stop":         len(c.hooks.stop),
		"step":         len(c.hooks.step),
	}
	for name, n := range counts {
		if n != 1 {
			t.Errorf("%s has %d hooks, want 1", name, n)
		}
	}
}

func TestResolveGivesHooksTheLogger(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	c := mustResolve(t, WithLogger(logger))

	if c.hooks.logger != logger {
		t.Error("the hook runner did not receive the configured logger")
	}
}

func TestResolveRejectsANegativeCommandTimeout(t *testing.T) {
	caps := DefaultCapabilities()
	caps.RunCommand = &RunCommandConfig{Timeout: -time.Second}

	_, err := resolved(t, WithCapabilities(caps))
	if err == nil {
		t.Fatal("a negative command timeout was accepted")
	}
	assertConfigField(t, err, "capabilities")
}

func TestWithSaveDirAndBinaryPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "localharness")

	c := mustResolve(t, WithSaveDir(dir), WithBinaryPath(bin),
		WithConversationID("conv-1"), WithSessionContinuation(ResumeSession))

	if c.saveDir != dir || c.binaryPath != bin {
		t.Errorf("saveDir = %q, binaryPath = %q", c.saveDir, c.binaryPath)
	}
	if c.conversationID != "conv-1" || c.continuation != ResumeSession {
		t.Errorf("conversation = %q, continuation = %q", c.conversationID, c.continuation)
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]bool{"c": true, "a": true, "b": true})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("sortedKeys = %v, want them sorted", got)
	}
}

// assertConfigField checks that err is a ConfigError naming the given field.
func assertConfigField(t *testing.T, err error, field string) {
	t.Helper()

	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("error = %T (%v), want *ConfigError", err, err)
	}
	if cfgErr.Field != field {
		t.Errorf("field = %q, want %q", cfgErr.Field, field)
	}
}

func TestWithVertexExpress(t *testing.T) {
	// Express mode has no project or location: the key alone identifies the
	// account, so the endpoint carries neither.
	c := mustResolve(t, WithVertexExpress(), WithAPIKey("express-key"))

	for _, m := range c.models {
		vertex, ok := m.Endpoint.(*VertexEndpoint)
		if !ok {
			t.Fatalf("model %q has a %T endpoint, want *VertexEndpoint", m.Name, m.Endpoint)
		}
		if vertex.Project != "" || vertex.Location != "" {
			t.Errorf("endpoint = %+v, want no project or location", vertex)
		}
		if vertex.APIKey != "express-key" {
			t.Errorf("endpoint = %+v, want the express key", vertex)
		}
	}

	// Without a key there is nothing to authenticate with, and express mode
	// cannot fall back to application default credentials.
	if _, err := resolved(t, WithVertexExpress()); err == nil {
		t.Error("express mode was accepted with no API key")
	}
}

func TestWithRetryConfigAndBudget(t *testing.T) {
	budget := BudgetConfig{MaxToolCalls: 20, MaxTotalTokens: 100_000}
	retry := &RetryConfig{OutputRetry: &ModelOutputRetryConfig{MaxRetries: 3}}

	c := mustResolve(t, WithRetryConfig(retry), WithBudget(budget))

	if c.retry != retry {
		t.Errorf("retry = %+v, want the supplied configuration", c.retry)
	}
	if c.budget == nil || *c.budget != budget {
		t.Errorf("budget = %+v, want the supplied caps", c.budget)
	}
}

func TestBenchmarkRetryConfig(t *testing.T) {
	got := BenchmarkRetryConfig()

	if got.APIRetry == nil {
		t.Fatal("BenchmarkRetryConfig sets no API retry policy")
	}
	// An eval suite must survive quota pressure rather than crash partway
	// through, so transient API errors are retried essentially forever.
	if got.APIRetry.MaxRetries != maxUint32 {
		t.Errorf("MaxRetries = %d, want an effectively unbounded count", got.APIRetry.MaxRetries)
	}
	if got.APIRetry.InitialSleepMS == 0 {
		t.Error("InitialSleepMS = 0, want a backoff delay")
	}
	// Output retries stay at the harness default so measured behavior still
	// matches production.
	if got.OutputRetry != nil {
		t.Errorf("OutputRetry = %+v, want the harness default left alone", got.OutputRetry)
	}
}
