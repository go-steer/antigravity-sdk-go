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
	"slices"
	"testing"
	"time"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// harnessProto resolves a configuration and renders it for the harness, which
// is the one message a session is configured by.
func harnessProto(t *testing.T, opts ...Option) *pb.HarnessConfig {
	t.Helper()

	c := mustResolve(t, opts...)
	return c.harnessConfig(nil)
}

func TestHarnessConfigCarriesTheWholeConfiguration(t *testing.T) {
	dir := t.TempDir()
	cfg := harnessProto(t,
		WithConversationID("conv-7"),
		WithSessionContinuation(ResumeSession),
		WithSystemPrompt("be terse"),
		WithTools(noopTool(t, "lookup")),
		WithWorkspaces(dir),
		WithSkillsPaths("/skills"),
		WithAppDataDir(dir),
		WithResponseSchemaJSON(`{"type":"object"}`),
		WithSubagents(SubagentConfig{Name: "researcher", Description: "reads"}),
	)

	if cfg.GetCascadeId() != "conv-7" {
		t.Errorf("CascadeId = %q, want the conversation id", cfg.GetCascadeId())
	}
	if got := cfg.GetSessionContinuationMode(); got != pb.HarnessConfig_RESUME {
		t.Errorf("SessionContinuationMode = %v, want RESUME", got)
	}
	if cfg.GetSystemInstructions() == nil {
		t.Error("SystemInstructions is unset")
	}
	if len(cfg.GetTools()) != 1 || cfg.GetTools()[0].GetName() != "lookup" {
		t.Errorf("Tools = %v, want the registered tool", cfg.GetTools())
	}
	if len(cfg.GetWorkspaces()) != 1 ||
		cfg.GetWorkspaces()[0].GetFilesystemWorkspace().GetDirectory() != dir {
		t.Errorf("Workspaces = %v, want %q", cfg.GetWorkspaces(), dir)
	}
	if !slices.Equal(cfg.GetSkillsPaths(), []string{"/skills"}) {
		t.Errorf("SkillsPaths = %v, want the configured path", cfg.GetSkillsPaths())
	}
	if cfg.GetAppDataDir() != dir {
		t.Errorf("AppDataDir = %q, want %q", cfg.GetAppDataDir(), dir)
	}
	if cfg.GetFinishToolSchemaJson() != `{"type":"object"}` {
		t.Errorf("FinishToolSchemaJson = %q", cfg.GetFinishToolSchemaJson())
	}
	if len(cfg.GetCustomSubagents()) != 1 {
		t.Errorf("CustomSubagents = %v, want the registered subagent", cfg.GetCustomSubagents())
	}
	if got := cfg.GetAgentBehavior(); got != pb.AgentBehavior_AGENT_BEHAVIOR_AUTONOMOUS {
		t.Errorf("AgentBehavior = %v, want autonomous by default", got)
	}
	// Models always cover both modalities, even unconfigured.
	if len(cfg.GetModels()) != 2 {
		t.Errorf("Models = %v, want a text and an image model", cfg.GetModels())
	}
}

func TestHarnessConfigOmitsWhatWasNotConfigured(t *testing.T) {
	cfg := harnessProto(t)

	if cfg.GetSystemInstructions() != nil {
		t.Error("SystemInstructions is set; the harness should keep its defaults")
	}
	if cfg.GetTools() != nil {
		t.Errorf("Tools = %v, want none", cfg.GetTools())
	}
	if cfg.GetCustomSubagents() != nil {
		t.Errorf("CustomSubagents = %v, want none", cfg.GetCustomSubagents())
	}
	if cfg.GetMcpServers() != nil {
		t.Errorf("McpServers = %v, want none", cfg.GetMcpServers())
	}
	if cfg.GetRetryConfig() != nil {
		t.Error("RetryConfig is set; the harness should keep its defaults")
	}
	if cfg.GetBudgetConfig() != nil {
		t.Error("BudgetConfig is set; an unconfigured session has no cap")
	}
	if cfg.GetEnabledHooks() != nil {
		t.Errorf("EnabledHooks = %v, want none registered", cfg.GetEnabledHooks())
	}
	if cfg.GetPolicyConfig() != nil {
		t.Error("PolicyConfig is set even though no enforcer was supplied")
	}
}

func TestHarnessConfigCarriesThePolicyConfig(t *testing.T) {
	c := mustResolve(t)
	enforcer, err := NewEnforcer(c.policies, nil)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	if c.harnessConfig(enforcer).GetPolicyConfig() == nil {
		t.Error("PolicyConfig is unset even though an enforcer was supplied")
	}
}

func TestContinuationModeProto(t *testing.T) {
	tests := []struct {
		mode SessionContinuationMode
		want pb.HarnessConfig_SessionContinuationMode
	}{
		{ResumeSession, pb.HarnessConfig_RESUME},
		{CreateOrResumeSession, pb.HarnessConfig_CREATE_OR_RESUME},
		{CreateOnlySession, pb.HarnessConfig_CREATE_ONLY},
		{"", pb.HarnessConfig_SESSION_CONTINUATION_MODE_UNSPECIFIED},
		{SessionContinuationMode("nonsense"), pb.HarnessConfig_SESSION_CONTINUATION_MODE_UNSPECIFIED},
	}

	for _, tt := range tests {
		if got := continuationModeProto(tt.mode); got != tt.want {
			t.Errorf("continuationModeProto(%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestBehaviorProto(t *testing.T) {
	tests := []struct {
		behavior AgentBehavior
		want     pb.AgentBehavior
	}{
		{BehaviorInteractive, pb.AgentBehavior_AGENT_BEHAVIOR_INTERACTIVE},
		{BehaviorAutonomous, pb.AgentBehavior_AGENT_BEHAVIOR_AUTONOMOUS},
		// An unset behavior is autonomous, the SDK's default posture.
		{"", pb.AgentBehavior_AGENT_BEHAVIOR_AUTONOMOUS},
	}

	for _, tt := range tests {
		if got := behaviorProto(tt.behavior); got != tt.want {
			t.Errorf("behaviorProto(%q) = %v, want %v", tt.behavior, got, tt.want)
		}
	}
}

func TestModelTypeProto(t *testing.T) {
	tests := []struct {
		kind ModelType
		want pb.ModelType
	}{
		{ModelTypeText, pb.ModelType_MODEL_TYPE_TEXT},
		{ModelTypeImage, pb.ModelType_MODEL_TYPE_IMAGE},
		{ModelType("video"), pb.ModelType_MODEL_TYPE_UNSPECIFIED},
	}

	for _, tt := range tests {
		if got := modelTypeProto(tt.kind); got != tt.want {
			t.Errorf("modelTypeProto(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestInstructionsProto(t *testing.T) {
	t.Run("nothing configured", func(t *testing.T) {
		for _, si := range []SystemInstructions{
			nil,
			CustomInstructions{},
			TemplatedInstructions{},
		} {
			if got := instructionsProto(si); got != nil {
				t.Errorf("instructionsProto(%#v) = %v, want nil so the harness keeps its defaults", si, got)
			}
		}
	})

	t.Run("custom replaces", func(t *testing.T) {
		got := instructionsProto(CustomInstructions{Text: "you are a bird"})
		parts := got.GetCustom().GetPart()
		if len(parts) != 1 || parts[0].GetText() != "you are a bird" {
			t.Errorf("parts = %v, want the custom text", parts)
		}
		if got.GetAppended() != nil {
			t.Error("custom instructions also produced appended sections")
		}
	})

	t.Run("templated appends", func(t *testing.T) {
		got := instructionsProto(TemplatedInstructions{
			Identity: "a careful reviewer",
			Sections: []InstructionSection{
				{Title: "style", Content: "be terse"},
				{Content: "cite sources"},
			},
		})
		appended := got.GetAppended()
		if appended.GetCustomIdentity() != "a careful reviewer" {
			t.Errorf("CustomIdentity = %q", appended.GetCustomIdentity())
		}
		sections := appended.GetAppendedSections()
		if len(sections) != 2 {
			t.Fatalf("got %d sections, want 2", len(sections))
		}
		if sections[0].GetTitle() != "style" || sections[0].GetContent() != "be terse" {
			t.Errorf("section 0 = %v", sections[0])
		}
		// An untitled section still needs a title on the wire.
		if sections[1].GetTitle() != defaultSectionTitle {
			t.Errorf("section 1 title = %q, want %q", sections[1].GetTitle(), defaultSectionTitle)
		}
	})

	t.Run("identity alone is enough", func(t *testing.T) {
		got := instructionsProto(TemplatedInstructions{Identity: "a bird"})
		if got.GetAppended().GetCustomIdentity() != "a bird" {
			t.Errorf("instructionsProto = %v, want the identity rendered", got)
		}
	})
}

func TestBuiltinToolsProtoSwitchesEveryToolExplicitly(t *testing.T) {
	// The harness must never fall back to a default for a tool the caller
	// resolved, so each switch is set either way.
	caps := DefaultCapabilities()
	caps.EnabledTools = []BuiltinTool{ToolViewFile, ToolStartSubagent}
	tools := caps.harnessSideTools()

	enabled := map[string]bool{
		"run_command":      tools.GetRunCommand().GetEnabled(),
		"find_file":        tools.GetFind().GetEnabled(),
		"ask_question":     tools.GetUserQuestions().GetEnabled(),
		"edit_file":        tools.GetFileEdit().GetEnabled(),
		"view_file":        tools.GetViewFile().GetEnabled(),
		"create_file":      tools.GetWriteToFile().GetEnabled(),
		"search_directory": tools.GetGrepSearch().GetEnabled(),
		"list_directory":   tools.GetListDir().GetEnabled(),
		"generate_image":   tools.GetGenerateImage().GetEnabled(),
		"search_web":       tools.GetSearchWeb().GetEnabled(),
		"read_url_content": tools.GetReadUrlContent().GetEnabled(),
	}
	for name, on := range enabled {
		if want := name == "view_file"; on != want {
			t.Errorf("%s enabled = %v, want %v", name, on, want)
		}
	}
	if !tools.GetSubagents().GetEnabled() {
		t.Error("subagents are disabled even though start_subagent is allowed")
	}
}

func TestHarnessSideToolsCarriesRunCommandSettings(t *testing.T) {
	caps := DefaultCapabilities()
	caps.RunCommand = &RunCommandConfig{
		EnableDaemons: true,
		EnableSandbox: true,
		Timeout:       90 * time.Second,
	}

	got := caps.harnessSideTools().GetRunCommand()
	if !got.GetEnabled() || !got.GetEnableDaemonCommands() || !got.GetEnableSandbox() {
		t.Errorf("run_command = %v, want every switch on", got)
	}
	if got.GetMaxTimeoutMs() != 90_000 {
		t.Errorf("MaxTimeoutMs = %d, want 90000", got.GetMaxTimeoutMs())
	}
}

func TestHarnessSideToolsSubagentDepthAndAllowlist(t *testing.T) {
	caps := DefaultCapabilities()
	caps.MaxSubagentDepth = 3
	caps.AllowedSubagents = []string{"researcher"}

	got := caps.harnessSideTools().GetSubagents()
	if got.GetMaxNestingDepth() != 3 {
		t.Errorf("MaxNestingDepth = %d, want 3", got.GetMaxNestingDepth())
	}
	if !slices.Equal(got.GetAllowedSubagents(), []string{"researcher"}) {
		t.Errorf("AllowedSubagents = %v", got.GetAllowedSubagents())
	}

	// An unset depth is left to the harness rather than pinned to zero.
	caps.MaxSubagentDepth = 0
	if caps.harnessSideTools().GetSubagents().HasMaxNestingDepth() {
		t.Error("MaxNestingDepth is set even though no depth was configured")
	}
}

func TestHarnessSideToolsDisabledSubagents(t *testing.T) {
	caps := DefaultCapabilities()
	caps.EnableSubagents = false

	got := caps.harnessSideTools().GetSubagents()
	if got.GetEnabled() {
		t.Error("subagents are enabled even though EnableSubagents is false")
	}
}

func TestSubagentHarnessSideToolsDefaultsToReadOnly(t *testing.T) {
	// A subagent with no capabilities gets the safe default, so a nil pointer
	// never means "everything".
	var caps *SubagentCapabilities
	got := caps.harnessSideTools()

	if got.GetRunCommand().GetEnabled() || got.GetFileEdit().GetEnabled() ||
		got.GetWriteToFile().GetEnabled() {
		t.Errorf("a nil subagent capability set enabled write tools: %v", got)
	}
	if !got.GetViewFile().GetEnabled() || !got.GetListDir().GetEnabled() {
		t.Errorf("a nil subagent capability set disabled read tools: %v", got)
	}
	if got.GetSubagents().GetEnabled() {
		t.Error("a nil subagent capability set allowed further delegation")
	}
}

func TestSubagentProtos(t *testing.T) {
	got := subagentProtos([]SubagentConfig{{
		Name:         "researcher",
		Description:  "reads things",
		Instructions: TemplatedInstructions{Sections: []InstructionSection{{Content: "cite"}}},
		Capabilities: &SubagentCapabilities{
			Behavior:      BehaviorInteractive,
			EnabledTools:  []BuiltinTool{ToolViewFile},
			DisabledTools: nil,
		},
		Tools: []Tool{noopTool(t, "lookup")},
	}})

	if len(got) != 1 {
		t.Fatalf("got %d subagents, want 1", len(got))
	}
	sub := got[0]
	if sub.GetName() != "researcher" || sub.GetDescription() != "reads things" {
		t.Errorf("subagent = %v, want the declared name and description", sub)
	}
	if sub.GetSystemInstructions() == nil {
		t.Error("the subagent's instructions were dropped")
	}
	if len(sub.GetTools()) != 1 || sub.GetTools()[0].GetName() != "lookup" {
		t.Errorf("Tools = %v, want the subagent's own tool", sub.GetTools())
	}
	if sub.GetAgentBehavior() != pb.AgentBehavior_AGENT_BEHAVIOR_INTERACTIVE {
		t.Errorf("AgentBehavior = %v, want interactive", sub.GetAgentBehavior())
	}
	if sub.GetHarnessSideTools().GetRunCommand().GetEnabled() {
		t.Error("run_command is enabled for a subagent that only allows view_file")
	}
}

func TestSubagentProtosDefaultsBehaviorToAutonomous(t *testing.T) {
	got := subagentProtos([]SubagentConfig{{Name: "researcher"}})
	if got[0].GetAgentBehavior() != pb.AgentBehavior_AGENT_BEHAVIOR_AUTONOMOUS {
		t.Errorf("AgentBehavior = %v, want autonomous", got[0].GetAgentBehavior())
	}
}

func TestMCPProtos(t *testing.T) {
	stdio := NewMCPStdioServer("weather", "weatherd", "--verbose")
	stdio.Env = map[string]string{"TOKEN": "secret"}
	stdio.Timeout = 30 * time.Second
	stdio.EnabledTools = []string{"forecast"}

	http := NewMCPHTTPServer("docs", "https://mcp.example/api")
	http.Headers = map[string]string{"Authorization": "Bearer x"}
	http.DisabledTools = []string{"delete"}

	got := mcpProtos([]MCPServer{stdio, http})
	if len(got) != 2 {
		t.Fatalf("got %d servers, want 2", len(got))
	}

	if got[0].GetName() != "weather" {
		t.Errorf("name = %q, want weather", got[0].GetName())
	}
	transport := got[0].GetStdio()
	if transport.GetCommand() != "weatherd" || !slices.Equal(transport.GetArgs(), []string{"--verbose"}) {
		t.Errorf("stdio transport = %v", transport)
	}
	if transport.GetEnv()["TOKEN"] != "secret" {
		t.Errorf("env = %v, want the configured variable", transport.GetEnv())
	}
	if got[0].GetTimeoutSeconds() != 30 {
		t.Errorf("TimeoutSeconds = %d, want 30", got[0].GetTimeoutSeconds())
	}
	if !slices.Equal(got[0].GetEnabledTools(), []string{"forecast"}) {
		t.Errorf("EnabledTools = %v", got[0].GetEnabledTools())
	}

	if got[1].GetHttp().GetUrl() != "https://mcp.example/api" {
		t.Errorf("http url = %q", got[1].GetHttp().GetUrl())
	}
	if got[1].GetHttp().GetHeaders()["Authorization"] != "Bearer x" {
		t.Errorf("headers = %v", got[1].GetHttp().GetHeaders())
	}
	if !slices.Equal(got[1].GetDisabledTools(), []string{"delete"}) {
		t.Errorf("DisabledTools = %v", got[1].GetDisabledTools())
	}
	if got[1].GetStdio() != nil {
		t.Error("an HTTP server also rendered a stdio transport")
	}
}

func TestModelProtos(t *testing.T) {
	got := modelProtos([]ModelTarget{
		{
			Name:  "text-model",
			Types: []ModelType{ModelTypeText},
			Endpoint: &GeminiAPIEndpoint{
				APIKey:      "key",
				BaseURL:     "https://proxy.example",
				HTTPHeaders: map[string]string{"X-Trace": "1"},
				Options: &GeminiModelOptions{
					ThinkingLevel: ThinkingHigh,
					ServiceTier:   ServiceTierPriority,
				},
			},
		},
		{
			Name:     "vertex-model",
			Endpoint: &VertexEndpoint{Project: "proj", Location: "us-central1"},
		},
		{
			Name:     "local-model",
			Endpoint: &GemmaEndpoint{BaseURL: "http://localhost:11434/v1"},
		},
	})

	if len(got) != 3 {
		t.Fatalf("got %d models, want 3", len(got))
	}

	gemini := got[0].GetGeminiApiEndpoint()
	if gemini.GetApiKey() != "key" || gemini.GetBaseUrl() != "https://proxy.example" {
		t.Errorf("gemini endpoint = %v", gemini)
	}
	if gemini.GetHttpHeaders()["X-Trace"] != "1" {
		t.Errorf("headers = %v", gemini.GetHttpHeaders())
	}
	if opts := gemini.GetOptions(); opts.GetThinkingLevel() != string(ThinkingHigh) ||
		opts.GetServiceTier() != string(ServiceTierPriority) {
		t.Errorf("options = %v", gemini.GetOptions())
	}
	if !slices.Equal(got[0].GetTypes(), []pb.ModelType{pb.ModelType_MODEL_TYPE_TEXT}) {
		t.Errorf("types = %v, want text", got[0].GetTypes())
	}

	vertex := got[1].GetVertexEndpoint()
	if vertex.GetProject() != "proj" || vertex.GetLocation() != "us-central1" {
		t.Errorf("vertex endpoint = %v", vertex)
	}
	if got[1].GetGeminiApiEndpoint() != nil {
		t.Error("a Vertex model also rendered a Gemini endpoint")
	}
	// An undeclared modality defaults to text rather than unspecified.
	if !slices.Equal(got[1].GetTypes(), []pb.ModelType{pb.ModelType_MODEL_TYPE_TEXT}) {
		t.Errorf("types = %v, want text by default", got[1].GetTypes())
	}

	gemma := got[2].GetGemmaEndpoint()
	if gemma.GetBaseUrl() != "http://localhost:11434/v1" {
		t.Errorf("gemma endpoint = %v", gemma)
	}
	if got[2].GetGeminiApiEndpoint() != nil || got[2].GetVertexEndpoint() != nil {
		t.Error("a local model also rendered a hosted endpoint")
	}
}

func TestGeminiOptionsProto(t *testing.T) {
	for _, opts := range []*GeminiModelOptions{nil, {}} {
		if got := geminiOptionsProto(opts); got != nil {
			t.Errorf("geminiOptionsProto(%#v) = %v, want nil so the backend defaults apply", opts, got)
		}
	}
	got := geminiOptionsProto(&GeminiModelOptions{ThinkingLevel: ThinkingLow})
	if got.GetThinkingLevel() != string(ThinkingLow) {
		t.Errorf("ThinkingLevel = %q, want %q", got.GetThinkingLevel(), ThinkingLow)
	}
}

func TestRetryProto(t *testing.T) {
	for _, cfg := range []*RetryConfig{nil, {}} {
		if got := retryProto(cfg); got != nil {
			t.Errorf("retryProto(%#v) = %v, want nil so the harness defaults apply", cfg, got)
		}
	}

	got := retryProto(&RetryConfig{
		APIRetry: &ModelAPIRetryConfig{
			MaxRetries:            5,
			InitialSleepMS:        250,
			ExponentialMultiplier: 2,
			JitterRange:           0.1,
		},
		OutputRetry: &ModelOutputRetryConfig{MaxRetries: 2},
	})
	api := got.GetApiRetry()
	if api.GetMaxRetries() != 5 || api.GetInitialSleepDurationMs() != 250 ||
		api.GetExponentialMultiplier() != 2 || api.GetJitterRange() != 0.1 {
		t.Errorf("api retry = %v", api)
	}
	if got.GetModelOutputRetry().GetMaxRetries() != 2 {
		t.Errorf("output retry = %v", got.GetModelOutputRetry())
	}

	// Half a configuration still renders, with the other half left unset.
	partial := retryProto(&RetryConfig{OutputRetry: &ModelOutputRetryConfig{MaxRetries: 1}})
	if partial.GetApiRetry() != nil {
		t.Error("ApiRetry is set even though only output retries were configured")
	}
}

func TestBudgetProto(t *testing.T) {
	for _, cfg := range []*BudgetConfig{nil, {}} {
		if got := budgetProto(cfg); got != nil {
			t.Errorf("budgetProto(%#v) = %v, want nil so no cap is imposed", cfg, got)
		}
	}

	got := budgetProto(&BudgetConfig{
		MaxModelCalls:   10,
		MaxToolCalls:    20,
		MaxInputTokens:  1000,
		MaxOutputTokens: 2000,
		MaxTotalTokens:  3000,
	})
	if got.GetMaxModelCalls() != 10 || got.GetMaxToolCalls() != 20 ||
		got.GetMaxInputTokens() != 1000 || got.GetMaxOutputTokens() != 2000 ||
		got.GetMaxTotalTokens() != 3000 {
		t.Errorf("budget = %v, want every cap carried", got)
	}
}

func TestHarnessConfigEnabledHooks(t *testing.T) {
	cfg := harnessProto(t,
		WithSessionStartHook(func(context.Context, *HookContext) error { return nil }),
		WithStopHook(func(context.Context, *HookContext, StopArgs) (StopDecision, error) {
			return StopDecision{}, nil
		}),
		// An interaction hook is answered through step updates, not a hook
		// call, so it must not appear here.
		WithInteractionHook(func(context.Context, *HookContext, QuestionRequest) (*QuestionAnswers, error) {
			return nil, nil
		}),
	)

	want := []pb.LifecycleHook{
		pb.LifecycleHook_LIFECYCLE_HOOK_ON_SESSION_START,
		pb.LifecycleHook_LIFECYCLE_HOOK_STOP,
	}
	if !slices.Equal(cfg.GetEnabledHooks(), want) {
		t.Errorf("EnabledHooks = %v, want %v", cfg.GetEnabledHooks(), want)
	}
}

func TestWorkspaceProtos(t *testing.T) {
	if got := workspaceProtos(nil); got != nil {
		t.Errorf("workspaceProtos(nil) = %v, want nil", got)
	}
	got := workspaceProtos([]string{"/a", "/b"})
	if len(got) != 2 || got[1].GetFilesystemWorkspace().GetDirectory() != "/b" {
		t.Errorf("workspaceProtos = %v", got)
	}
}

func TestToolProtos(t *testing.T) {
	if got := toolProtos(nil); got != nil {
		t.Errorf("toolProtos(nil) = %v, want nil", got)
	}
	tool := noopTool(t, "lookup")
	got := toolProtos([]Tool{tool})
	if len(got) != 1 {
		t.Fatalf("got %d tools, want 1", len(got))
	}
	if got[0].GetName() != "lookup" || got[0].GetDescription() != tool.Description() {
		t.Errorf("tool = %v, want the registered name and description", got[0])
	}
	if got[0].GetParametersJsonSchema() != string(tool.ParametersSchema()) {
		t.Errorf("schema = %q, want the tool's own", got[0].GetParametersJsonSchema())
	}
}

func TestRetryAndBudgetReachTheProto(t *testing.T) {
	got := harnessProto(t,
		WithRetryConfig(BenchmarkRetryConfig()),
		WithBudget(BudgetConfig{MaxToolCalls: 12}),
	)

	if got.GetRetryConfig().GetApiRetry().GetMaxRetries() != maxUint32 {
		t.Errorf("retry = %v, want the benchmark preset", got.GetRetryConfig())
	}
	if got.GetBudgetConfig().GetMaxToolCalls() != 12 {
		t.Errorf("budget = %v, want the configured cap", got.GetBudgetConfig())
	}
}
