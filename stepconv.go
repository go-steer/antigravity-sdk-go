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
	"encoding/json"
	"strconv"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
	"github.com/go-steer/antigravity-sdk-go/internal/wire"
)

// builtinToolField maps a built-in tool to the StepUpdate field the harness
// reports it in. The order is significant: the first present field wins, so a
// step that somehow carries two actions resolves the same way every time.
var builtinToolFields = []struct {
	tool  BuiltinTool
	field func(*pb.StepUpdate) proto.Message
	has   func(*pb.StepUpdate) bool
}{
	{ToolCreateFile, func(s *pb.StepUpdate) proto.Message { return s.GetCreateFile() }, (*pb.StepUpdate).HasCreateFile},
	{ToolEditFile, func(s *pb.StepUpdate) proto.Message { return s.GetEditFile() }, (*pb.StepUpdate).HasEditFile},
	{ToolFindFile, func(s *pb.StepUpdate) proto.Message { return s.GetFindFile() }, (*pb.StepUpdate).HasFindFile},
	{ToolListDir, func(s *pb.StepUpdate) proto.Message { return s.GetListDirectory() }, (*pb.StepUpdate).HasListDirectory},
	{ToolRunCommand, func(s *pb.StepUpdate) proto.Message { return s.GetRunCommand() }, (*pb.StepUpdate).HasRunCommand},
	{ToolSearchDir, func(s *pb.StepUpdate) proto.Message { return s.GetSearchDirectory() }, (*pb.StepUpdate).HasSearchDirectory},
	{ToolViewFile, func(s *pb.StepUpdate) proto.Message { return s.GetViewFile() }, (*pb.StepUpdate).HasViewFile},
	{ToolStartSubagent, func(s *pb.StepUpdate) proto.Message { return s.GetInvokeSubagent() }, (*pb.StepUpdate).HasInvokeSubagent},
	{ToolGenerateImage, func(s *pb.StepUpdate) proto.Message { return s.GetGenerateImage() }, (*pb.StepUpdate).HasGenerateImage},
	{ToolSearchWeb, func(s *pb.StepUpdate) proto.Message { return s.GetSearchWeb() }, (*pb.StepUpdate).HasSearchWeb},
	{ToolReadURLContent, func(s *pb.StepUpdate) proto.Message { return s.GetReadUrlContent() }, (*pb.StepUpdate).HasReadUrlContent},
	{ToolFinish, func(s *pb.StepUpdate) proto.Message { return s.GetFinish() }, (*pb.StepUpdate).HasFinish},
}

// wirePathArgKeys are the tool-argument keys that carry wire-format URIs and
// must be rewritten to native paths before a caller sees them.
var wirePathArgKeys = []string{"path", "file_path", "directory_path", "TargetFile", "output_path"}

// protoArgs renders an action message as its JSON arguments, using proto field
// names so the keys match what the Python SDK exposes.
var protoArgs = protojson.MarshalOptions{UseProtoNames: true}

// makeStepID builds the identifier the SDK uses for a step.
func makeStepID(trajectoryID string, index uint32) string {
	if trajectoryID == "" {
		return strconv.FormatUint(uint64(index), 10)
	}
	return trajectoryID + ":" + strconv.FormatUint(uint64(index), 10)
}

var (
	sourceFromProto = map[pb.StepUpdate_Source]StepSource{
		pb.StepUpdate_SOURCE_SYSTEM: SourceSystem,
		pb.StepUpdate_SOURCE_USER:   SourceUser,
		pb.StepUpdate_SOURCE_MODEL:  SourceModel,
	}
	statusFromProto = map[pb.StepUpdate_State]StepStatus{
		pb.StepUpdate_STATE_ACTIVE:           StatusActive,
		pb.StepUpdate_STATE_DONE:             StatusDone,
		pb.StepUpdate_STATE_WAITING_FOR_USER: StatusWaitingForUser,
		pb.StepUpdate_STATE_ERROR:            StatusError,
	}
	targetFromProto = map[pb.StepUpdate_Target]StepTarget{
		pb.StepUpdate_TARGET_USER:        TargetUser,
		pb.StepUpdate_TARGET_ENVIRONMENT: TargetEnvironment,
		pb.StepUpdate_TARGET_UNSPECIFIED: TargetUnspecified,
	}
)

// stepFromProto converts a StepUpdate into the public [Step].
func stepFromProto(su *pb.StepUpdate) Step {
	trajectoryID := su.GetTrajectoryId()
	index := su.GetStepIndex()
	stepID := makeStepID(trajectoryID, index)

	toolName, toolArgs, serverName, callID := extractToolCall(su)

	var toolCalls []ToolCall
	if toolName != "" {
		args, canonicalPath := normalizeArgPaths(toolArgs)
		if callID == "" {
			callID = stepID
		}
		toolCalls = []ToolCall{{
			Name:          toolName,
			Args:          args,
			ID:            callID,
			StepID:        stepID,
			CanonicalPath: canonicalPath,
			ServerName:    serverName,
		}}
	}

	source := sourceFromProto[su.GetSource()]
	if source == "" {
		source = SourceUnknown
	}
	status := statusFromProto[su.GetState()]
	if status == "" {
		status = StatusUnknown
	}
	target := targetFromProto[su.GetTarget()]
	if target == "" {
		target = TargetUnknown
	}

	return Step{
		ID:                 stepID,
		Index:              int(index),
		TrajectoryID:       trajectoryID,
		ParentTrajectoryID: su.GetParentTrajectoryId(),
		Type:               stepTypeOf(su, toolName),
		Source:             source,
		Status:             status,
		Target:             target,
		Content:            su.GetText(),
		ContentDelta:       su.GetTextDelta(),
		Thinking:           su.GetThinking(),
		ThinkingDelta:      su.GetThinkingDelta(),
		ToolCalls:          toolCalls,
		Error:              su.GetError().GetErrorMessage(),
		HTTPCode:           int(su.GetError().GetHttpCode()),
		IsCompleteResponse: source == SourceModel && status == StatusDone &&
			su.GetText() != "" && target == TargetUser,
		StructuredOutput: structuredOutputOf(su),
	}
}

// extractToolCall finds the tool this step invokes, if any, and renders its
// arguments as JSON.
//
// Three shapes are possible: a built-in action field, an MCP call, or a custom
// tool. They are checked in that order, matching the Python SDK.
func extractToolCall(su *pb.StepUpdate) (name string, args json.RawMessage, serverName, callID string) {
	for _, bt := range builtinToolFields {
		if !bt.has(su) {
			continue
		}
		return string(bt.tool), marshalArgs(bt.field(su)), "", ""
	}

	if su.HasMcpTool() {
		mcp := su.GetMcpTool()
		return mcp.GetToolName(), rawArgsOrEmpty(mcp.GetArgumentsJson()), mcp.GetServerName(), ""
	}

	if su.HasCustomTool() {
		tc := su.GetCustomTool().GetToolCall()
		if tc != nil {
			return tc.GetName(), rawArgsOrEmpty(tc.GetArgumentsJson()), "", tc.GetId()
		}
	}

	return "", nil, "", ""
}

// marshalArgs renders an action message as JSON, falling back to an empty
// object rather than failing a whole step over unrenderable arguments.
func marshalArgs(m proto.Message) json.RawMessage {
	if m == nil {
		return json.RawMessage(`{}`)
	}
	data, err := protoArgs.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(data)
}

// rawArgsOrEmpty accepts an arguments string that may be empty or malformed.
func rawArgsOrEmpty(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// normalizeArgPaths rewrites wire URIs in the known path arguments to native
// paths, and reports the last one it rewrote as the step's canonical path.
//
// Arguments that are not a JSON object, or that contain no path keys, are
// returned untouched.
func normalizeArgPaths(args json.RawMessage) (json.RawMessage, string) {
	if len(args) == 0 {
		return args, ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return args, ""
	}

	var canonical string
	changed := false
	for _, key := range wirePathArgKeys {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue // not a string; leave it alone
		}
		normalized := wire.NormalizePath(s)
		canonical = normalized
		if normalized != s {
			encoded, err := json.Marshal(normalized)
			if err != nil {
				continue
			}
			obj[key] = encoded
			changed = true
		}
	}

	if !changed {
		return args, canonical
	}
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return args, canonical
	}
	return rewritten, canonical
}

// stepTypeOf classifies a step. Compaction and finish take precedence over the
// tool and text checks, because a finish step also carries text.
func stepTypeOf(su *pb.StepUpdate, toolName string) StepType {
	switch {
	case su.HasCompaction():
		return StepCompaction
	case su.HasFinish():
		return StepFinish
	case toolName != "" || hasAnyBuiltinAction(su):
		return StepToolCall
	case su.GetText() != "":
		return StepTextResponse
	case su.GetThinking() != "":
		return StepThinking
	default:
		return StepUnknown
	}
}

// hasAnyBuiltinAction reports whether the step carries a built-in action, even
// one we could not name.
func hasAnyBuiltinAction(su *pb.StepUpdate) bool {
	for _, bt := range builtinToolFields {
		if bt.has(su) {
			return true
		}
	}
	return false
}

// structuredOutputOf extracts the JSON payload of a finish step.
//
// Invalid JSON is dropped rather than surfaced: the harness may finish with a
// plain string, and a malformed payload should not fail the turn.
func structuredOutputOf(su *pb.StepUpdate) json.RawMessage {
	if !su.HasFinish() {
		return nil
	}
	out := su.GetFinish().GetOutputString()
	if out == "" || !json.Valid([]byte(out)) {
		return nil
	}
	return json.RawMessage(out)
}

// usageFromProto converts harness usage metadata, preserving the distinction
// between a reported zero and an absent count.
func usageFromProto(u *pb.UsageMetadata) *UsageMetadata {
	if u == nil {
		return nil
	}
	out := &UsageMetadata{}
	if u.HasPromptTokenCount() {
		out.PromptTokenCount = int64(u.GetPromptTokenCount())
	}
	if u.HasCachedContentTokenCount() {
		out.CachedContentTokenCount = int64(u.GetCachedContentTokenCount())
	}
	if u.HasCandidatesTokenCount() {
		out.CandidatesTokenCount = int64(u.GetCandidatesTokenCount())
	}
	if u.HasThoughtsTokenCount() {
		out.ThoughtsTokenCount = int64(u.GetThoughtsTokenCount())
	}
	if u.HasTotalTokenCount() {
		out.TotalTokenCount = int64(u.GetTotalTokenCount())
	}
	if tier := u.GetServiceTier(); tier != "" {
		out.ServiceTier = ServiceTier(tier)
	}
	return out
}

// stepsFromHistory converts the restored history the harness returns at
// startup.
func stepsFromHistory(history []*pb.StepUpdate) []Step {
	if len(history) == 0 {
		return nil
	}
	steps := make([]Step, 0, len(history))
	for _, su := range history {
		steps = append(steps, stepFromProto(su))
	}
	return steps
}
