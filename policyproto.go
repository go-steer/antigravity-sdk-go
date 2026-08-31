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
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// Most policies are static: a target and a verdict the harness can apply on
// its own, with no round trip to the client. A policy carrying a predicate or
// an ask-user handler cannot be, since only client code can evaluate it, so it
// is sent as a dynamic rule tagged with an id. The harness then asks the client
// for a decision each time the rule is reached.

// parseTarget splits a policy target into its tool and MCP server parts.
func parseTarget(target string) (tool, server string) {
	if target == wildcard {
		return wildcard, ""
	}
	if server, tool, found := strings.Cut(target, "/"); found {
		return tool, server
	}
	return target, ""
}

// isDynamic reports whether a policy must be evaluated by the client.
//
// Workspace-containment markers are excluded: the harness enforces those
// itself, and asking the client about them would deny every file operation.
func (p Policy) isDynamic() bool {
	if p.Name == workspaceOnlyPolicyName {
		return false
	}
	return p.When != nil || p.Decision == DecisionAskUser
}

var decisionToProto = map[Decision]pb.PolicyDecision{
	DecisionApprove: pb.PolicyDecision_POLICY_DECISION_ALLOW,
	DecisionDeny:    pb.PolicyDecision_POLICY_DECISION_DENY,
	DecisionAskUser: pb.PolicyDecision_POLICY_DECISION_ASK_USER,
}

// policyConfig renders the enforcer's rules for the harness and records which
// ones it must ask the client about.
//
// The rule ids index the declared order, and the same slice produces both the
// proto and the lookup map, so the two cannot drift apart.
func (e *Enforcer) policyConfig() *pb.PolicyConfig {
	rules := make([]*pb.PolicyRule, 0, len(e.declared))

	for i, p := range e.declared {
		tool, server := parseTarget(p.Tool)
		dynamic := p.isDynamic()

		ruleID := ""
		if dynamic {
			ruleID = fmt.Sprintf("rule_%d", i)
			e.dynamic[ruleID] = p
		}

		rules = append(rules, pb.PolicyRule_builder{
			Tool:       proto.String(tool),
			ServerName: proto.String(server),
			Name:       proto.String(p.label()),
			Decision:   decisionToProto[p.Decision].Enum(),
			IsDynamic:  proto.Bool(dynamic),
			RuleId:     proto.String(ruleID),
		}.Build())
	}

	return pb.PolicyConfig_builder{Rules: rules}.Build()
}

// byRuleID looks up a dynamic policy the harness is asking about.
//
// It reports false for an unknown id, which callers must treat as a denial:
// an id we cannot resolve is a rule we cannot honor.
func (e *Enforcer) byRuleID(id string) (Policy, bool) {
	p, ok := e.dynamic[id]
	return p, ok
}

var stopReasonToSDK = map[pb.TrajectoryStateUpdate_StopReason]StopReason{
	pb.TrajectoryStateUpdate_STOP_REASON_MAX_MODEL_CALLS_EXCEEDED:   StopMaxModelCalls,
	pb.TrajectoryStateUpdate_STOP_REASON_MAX_TOOL_CALLS_EXCEEDED:    StopMaxToolCalls,
	pb.TrajectoryStateUpdate_STOP_REASON_MAX_INPUT_TOKENS_EXCEEDED:  StopMaxInputTokens,
	pb.TrajectoryStateUpdate_STOP_REASON_MAX_OUTPUT_TOKENS_EXCEEDED: StopMaxOutputTokens,
	pb.TrajectoryStateUpdate_STOP_REASON_MAX_TOTAL_TOKENS_EXCEEDED:  StopMaxTotalTokens,
	pb.TrajectoryStateUpdate_STOP_REASON_QUOTA_EXHAUSTED:            StopQuotaExhausted,
}

// stopReasonFromProto maps a harness stop reason to the public enum, treating
// anything unrecognized as unspecified.
func stopReasonFromProto(r pb.TrajectoryStateUpdate_StopReason) StopReason {
	if s, ok := stopReasonToSDK[r]; ok {
		return s
	}
	return StopUnspecified
}
