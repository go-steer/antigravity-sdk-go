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
	"slices"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// The harness is configured once, at session start, by a single HarnessConfig
// message. Everything the caller declared through options — tools, models,
// policies, subagents, MCP servers — is translated here into that one proto.
//
// The translation is total: an option with no representation on the wire is a
// bug in this file, not a silent omission.
//
// The reverse is not true. Three HarnessConfig fields are deliberately left
// unset because the SDK exposes no way to reach them, matching the Python SDK:
// tool_output_truncation, initial_trajectory, and each subagent's skills_config
// (skills are configured session-wide by [WithSkillsPaths] instead).

// defaultSectionTitle names an instruction section the caller left untitled.
const defaultSectionTitle = "user_system_instructions"

// harnessConfig renders the resolved configuration for the harness.
//
// The enforcer supplies the policy rules, and is passed in rather than derived
// here because it also records which rules the harness must ask the client
// about at call time.
func (c *config) harnessConfig(enforcer *Enforcer) *pb.HarnessConfig {
	b := pb.HarnessConfig_builder{
		CascadeId:               proto.String(c.conversationID),
		SessionContinuationMode: continuationModeProto(c.continuation).Enum(),
		SystemInstructions:      instructionsProto(c.instructions),
		Tools:                   toolProtos(c.tools),
		HarnessSideTools:        c.capabilities.harnessSideTools(),
		CompactionThreshold:     proto.Uint32(c.capabilities.CompactionThreshold),
		Workspaces:              workspaceProtos(c.workspaces),
		SkillsPaths:             slices.Clone(c.skillsPaths),
		FinishToolSchemaJson:    proto.String(c.capabilities.FinishToolSchemaJSON),
		AppDataDir:              proto.String(c.appDataDir),
		McpServers:              mcpProtos(c.mcpServers),
		Models:                  modelProtos(c.models),
		EnabledHooks:            c.hooks.enabledHooks(),
		CustomSubagents:         subagentProtos(c.subagents),
		AgentBehavior:           behaviorProto(c.capabilities.Behavior).Enum(),
		RetryConfig:             retryProto(c.retry),
		BudgetConfig:            budgetProto(c.budget),
	}
	if enforcer != nil {
		b.PolicyConfig = enforcer.policyConfig()
	}
	return b.Build()
}

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

var continuationModes = map[SessionContinuationMode]pb.HarnessConfig_SessionContinuationMode{
	ResumeSession:         pb.HarnessConfig_RESUME,
	CreateOrResumeSession: pb.HarnessConfig_CREATE_OR_RESUME,
	CreateOnlySession:     pb.HarnessConfig_CREATE_ONLY,
}

// continuationModeProto maps a continuation mode, leaving an unset or
// unrecognized one to the harness's own default.
func continuationModeProto(m SessionContinuationMode) pb.HarnessConfig_SessionContinuationMode {
	if v, ok := continuationModes[m]; ok {
		return v
	}
	return pb.HarnessConfig_SESSION_CONTINUATION_MODE_UNSPECIFIED
}

// behaviorProto maps an agent behavior. An unset behavior is autonomous, which
// is the SDK's default posture.
func behaviorProto(b AgentBehavior) pb.AgentBehavior {
	if b == BehaviorInteractive {
		return pb.AgentBehavior_AGENT_BEHAVIOR_INTERACTIVE
	}
	return pb.AgentBehavior_AGENT_BEHAVIOR_AUTONOMOUS
}

func modelTypeProto(t ModelType) pb.ModelType {
	switch t {
	case ModelTypeText:
		return pb.ModelType_MODEL_TYPE_TEXT
	case ModelTypeImage:
		return pb.ModelType_MODEL_TYPE_IMAGE
	default:
		return pb.ModelType_MODEL_TYPE_UNSPECIFIED
	}
}

// ---------------------------------------------------------------------------
// System instructions
// ---------------------------------------------------------------------------

// instructionsProto renders system instructions, returning nil when there are
// none so the harness keeps its defaults.
func instructionsProto(si SystemInstructions) *pb.SystemInstructions {
	switch v := si.(type) {
	case CustomInstructions:
		if v.Text == "" {
			return nil
		}
		return pb.SystemInstructions_builder{
			Custom: pb.CustomSystemInstructions_builder{
				Part: []*pb.CustomSystemInstructions_Part{
					pb.CustomSystemInstructions_Part_builder{Text: proto.String(v.Text)}.Build(),
				},
			}.Build(),
		}.Build()

	case TemplatedInstructions:
		if v.Identity == "" && len(v.Sections) == 0 {
			return nil
		}
		sections := make([]*pb.AppendedSystemInstructions_Section, 0, len(v.Sections))
		for _, s := range v.Sections {
			title := s.Title
			if title == "" {
				title = defaultSectionTitle
			}
			sections = append(sections, pb.AppendedSystemInstructions_Section_builder{
				Title:   proto.String(title),
				Content: proto.String(s.Content),
			}.Build())
		}
		return pb.SystemInstructions_builder{
			Appended: pb.AppendedSystemInstructions_builder{
				CustomIdentity:   proto.String(v.Identity),
				AppendedSections: sections,
			}.Build(),
		}.Build()

	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

func toolProtos(tools []Tool) []*pb.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]*pb.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, pb.Tool_builder{
			Name:                 proto.String(t.Name()),
			Description:          proto.String(t.Description()),
			ParametersJsonSchema: proto.String(string(t.ParametersSchema())),
		}.Build())
	}
	return out
}

// harnessSideTools renders the built-in tool switches for the root agent.
func (c *CapabilitiesConfig) harnessSideTools() *pb.HarnessSideTools {
	active := c.activeTools()
	subagents := pb.SubagentsConfig_builder{
		Enabled:          proto.Bool(!c.subagentsDisabled()),
		AllowedSubagents: slices.Clone(c.AllowedSubagents),
	}
	if c.MaxSubagentDepth != 0 {
		subagents.MaxNestingDepth = proto.Int32(int32(c.MaxSubagentDepth))
	}
	return builtinToolsProto(active, subagents.Build(), c.RunCommand)
}

// harnessSideTools renders the built-in tool switches for a subagent. A nil
// receiver means the read-only default.
func (c *SubagentCapabilities) harnessSideTools() *pb.HarnessSideTools {
	active := c.activeTools()
	subagents := pb.SubagentsConfig_builder{
		Enabled: proto.Bool(c != nil && slices.Contains(active, ToolStartSubagent)),
	}
	var runCommand *RunCommandConfig
	if c != nil {
		subagents.AllowedSubagents = slices.Clone(c.AllowedSubagents)
		runCommand = c.RunCommand
	}
	return builtinToolsProto(active, subagents.Build(), runCommand)
}

// builtinToolsProto turns a resolved tool set into the harness's per-tool
// switches. Every built-in tool is named explicitly, so a tool absent from
// active is switched off rather than left to a default.
func builtinToolsProto(active []BuiltinTool, subagents *pb.SubagentsConfig, runCmd *RunCommandConfig) *pb.HarnessSideTools {
	on := func(t BuiltinTool) *bool { return proto.Bool(slices.Contains(active, t)) }

	runCommand := pb.RunCommandToolConfig_builder{Enabled: on(ToolRunCommand)}
	if runCmd != nil {
		runCommand.EnableDaemonCommands = proto.Bool(runCmd.EnableDaemons)
		runCommand.EnableSandbox = proto.Bool(runCmd.EnableSandbox)
		runCommand.MaxTimeoutMs = proto.Uint32(uint32(runCmd.Timeout.Milliseconds()))
	}

	return pb.HarnessSideTools_builder{
		Subagents:      subagents,
		RunCommand:     runCommand.Build(),
		Find:           pb.FindToolConfig_builder{Enabled: on(ToolFindFile)}.Build(),
		UserQuestions:  pb.UserQuestionsConfig_builder{Enabled: on(ToolAskQuestion)}.Build(),
		FileEdit:       pb.FileEditToolConfig_builder{Enabled: on(ToolEditFile)}.Build(),
		ViewFile:       pb.ViewFileToolConfig_builder{Enabled: on(ToolViewFile)}.Build(),
		WriteToFile:    pb.WriteToFileToolConfig_builder{Enabled: on(ToolCreateFile)}.Build(),
		GrepSearch:     pb.GrepSearchToolConfig_builder{Enabled: on(ToolSearchDir)}.Build(),
		ListDir:        pb.ListDirToolConfig_builder{Enabled: on(ToolListDir)}.Build(),
		GenerateImage:  pb.GenerateImageToolConfig_builder{Enabled: on(ToolGenerateImage)}.Build(),
		SearchWeb:      pb.SearchWebToolConfig_builder{Enabled: on(ToolSearchWeb)}.Build(),
		ReadUrlContent: pb.ReadUrlContentToolConfig_builder{Enabled: on(ToolReadURLContent)}.Build(),
	}.Build()
}

// ---------------------------------------------------------------------------
// Subagents
// ---------------------------------------------------------------------------

func subagentProtos(subagents []SubagentConfig) []*pb.CustomAgent {
	if len(subagents) == 0 {
		return nil
	}
	out := make([]*pb.CustomAgent, 0, len(subagents))
	for _, s := range subagents {
		behavior := BehaviorAutonomous
		if s.Capabilities != nil {
			behavior = s.Capabilities.Behavior
		}
		out = append(out, pb.CustomAgent_builder{
			Name:               proto.String(s.Name),
			Description:        proto.String(s.Description),
			SystemInstructions: instructionsProto(s.Instructions),
			HarnessSideTools:   s.Capabilities.harnessSideTools(),
			Tools:              toolProtos(s.Tools),
			AgentBehavior:      behaviorProto(behavior).Enum(),
		}.Build())
	}
	return out
}

// ---------------------------------------------------------------------------
// Workspaces, MCP, models
// ---------------------------------------------------------------------------

func workspaceProtos(paths []string) []*pb.Workspace {
	if len(paths) == 0 {
		return nil
	}
	out := make([]*pb.Workspace, 0, len(paths))
	for _, p := range paths {
		out = append(out, pb.Workspace_builder{
			FilesystemWorkspace: pb.FilesystemWorkspace_builder{
				Directory: proto.String(p),
			}.Build(),
		}.Build())
	}
	return out
}

func mcpProtos(servers []MCPServer) []*pb.McpServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]*pb.McpServerConfig, 0, len(servers))
	for _, s := range servers {
		var b pb.McpServerConfig_builder
		switch v := s.(type) {
		case *MCPStdioServer:
			b = pb.McpServerConfig_builder{
				EnabledTools:   slices.Clone(v.EnabledTools),
				DisabledTools:  slices.Clone(v.DisabledTools),
				TimeoutSeconds: proto.Int32(int32(v.Timeout.Seconds())),
				Stdio: pb.McpStdioTransport_builder{
					Command: proto.String(v.Command),
					Args:    slices.Clone(v.Args),
					Env:     v.Env,
				}.Build(),
			}
		case *MCPHTTPServer:
			b = pb.McpServerConfig_builder{
				EnabledTools:   slices.Clone(v.EnabledTools),
				DisabledTools:  slices.Clone(v.DisabledTools),
				TimeoutSeconds: proto.Int32(int32(v.Timeout.Seconds())),
				Http: pb.McpHttpTransport_builder{
					Url:     proto.String(v.URL),
					Headers: v.Headers,
				}.Build(),
			}
		default:
			// The interface is closed to this package, so an unknown
			// implementation cannot occur; skip rather than panic.
			continue
		}
		b.Name = proto.String(s.Name())
		out = append(out, b.Build())
	}
	return out
}

func modelProtos(models []ModelTarget) []*pb.ModelConfig {
	if len(models) == 0 {
		return nil
	}
	out := make([]*pb.ModelConfig, 0, len(models))
	for _, m := range models {
		types := make([]pb.ModelType, 0, len(m.Types))
		for _, t := range m.modelTypes() {
			types = append(types, modelTypeProto(t))
		}
		b := pb.ModelConfig_builder{
			Name:  proto.String(m.Name),
			Types: types,
		}
		switch e := m.Endpoint.(type) {
		case *GeminiAPIEndpoint:
			b.GeminiApiEndpoint = pb.GeminiAPIEndpoint_builder{
				BaseUrl:     proto.String(e.BaseURL),
				HttpHeaders: e.HTTPHeaders,
				ApiKey:      proto.String(e.APIKey),
				Options:     geminiOptionsProto(e.Options),
			}.Build()
		case *VertexEndpoint:
			b.VertexEndpoint = pb.VertexEndpoint_builder{
				BaseUrl:     proto.String(e.BaseURL),
				HttpHeaders: e.HTTPHeaders,
				Project:     proto.String(e.Project),
				Location:    proto.String(e.Location),
				ApiKey:      proto.String(e.APIKey),
				Options:     geminiOptionsProto(e.Options),
			}.Build()
		}
		out = append(out, b.Build())
	}
	return out
}

// geminiOptionsProto renders inference options, returning nil when none were
// set so the backend applies its own defaults.
func geminiOptionsProto(o *GeminiModelOptions) *pb.GeminiModelOptions {
	if o == nil || (o.ThinkingLevel == "" && o.ServiceTier == "") {
		return nil
	}
	return pb.GeminiModelOptions_builder{
		ThinkingLevel: proto.String(string(o.ThinkingLevel)),
		ServiceTier:   proto.String(string(o.ServiceTier)),
	}.Build()
}

// ---------------------------------------------------------------------------
// Retry and budget
// ---------------------------------------------------------------------------

// retryProto renders retry settings, returning nil when nothing was configured
// so the harness keeps its own defaults.
func retryProto(c *RetryConfig) *pb.RetryConfig {
	if c == nil || (c.APIRetry == nil && c.OutputRetry == nil) {
		return nil
	}
	var b pb.RetryConfig_builder
	if a := c.APIRetry; a != nil {
		b.ApiRetry = pb.ModelAPIRetryConfig_builder{
			MaxRetries:             proto.Uint32(a.MaxRetries),
			InitialSleepDurationMs: proto.Uint32(a.InitialSleepMS),
			ExponentialMultiplier:  proto.Float64(a.ExponentialMultiplier),
			JitterRange:            proto.Float64(a.JitterRange),
		}.Build()
	}
	if o := c.OutputRetry; o != nil {
		b.ModelOutputRetry = pb.ModelOutputRetryConfig_builder{
			MaxRetries: proto.Uint32(o.MaxRetries),
		}.Build()
	}
	return b.Build()
}

// budgetProto renders session budgets, returning nil for an empty budget so no
// cap is imposed.
func budgetProto(c *BudgetConfig) *pb.BudgetConfig {
	if c == nil || *c == (BudgetConfig{}) {
		return nil
	}
	return pb.BudgetConfig_builder{
		MaxModelCalls:   proto.Int32(c.MaxModelCalls),
		MaxToolCalls:    proto.Int32(c.MaxToolCalls),
		MaxInputTokens:  proto.Int64(c.MaxInputTokens),
		MaxOutputTokens: proto.Int64(c.MaxOutputTokens),
		MaxTotalTokens:  proto.Int64(c.MaxTotalTokens),
	}.Build()
}
