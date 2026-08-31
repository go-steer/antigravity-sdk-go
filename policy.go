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
	"log/slog"
	"slices"
	"strings"
)

// wildcard matches every tool.
const wildcard = "*"

// workspaceOnlyPolicyName marks policies whose enforcement happens in the
// harness rather than in this process. The in-process evaluator skips them.
const workspaceOnlyPolicyName = "workspace_only"

// Decision is the outcome a policy produces when it matches a tool call.
type Decision string

const (
	// DecisionApprove permits the call.
	DecisionApprove Decision = "APPROVE"
	// DecisionDeny rejects the call and tells the model why.
	DecisionDeny Decision = "DENY"
	// DecisionAskUser defers to a human, via the policy's handler.
	DecisionAskUser Decision = "ASK_USER"
)

// Predicate decides whether a policy applies to a particular call. Returning
// an error fails closed: the call is denied.
type Predicate func(ctx context.Context, call ToolCall) (bool, error)

// AskUserHandler asks a human whether to permit a call, returning true to
// approve. Returning an error fails closed.
type AskUserHandler func(ctx context.Context, call ToolCall) (bool, error)

// Policy is one tool-call rule.
//
// Policies are evaluated in priority order, not declaration order: more
// specific targets beat wildcards, and within equal specificity, deny beats
// ask-user beats approve. The first policy whose target and predicate both
// match decides the call. See [Enforcer].
type Policy struct {
	// Tool is the target: a tool name, "*" for all tools, "server/tool" for a
	// specific MCP tool, or "server/*" for every tool on an MCP server.
	Tool string

	// Decision is the outcome when this policy matches.
	Decision Decision

	// When further narrows the policy by inspecting the call's arguments. Nil
	// matches any call to the target.
	When Predicate

	// AskUser is invoked for [DecisionAskUser] policies. It is required for
	// them, and validated when the [Enforcer] is built.
	AskUser AskUserHandler

	// Name labels the policy in logs and denial messages. It defaults to the
	// target when empty.
	Name string
}

// label returns the policy's display name, falling back to its target.
func (p Policy) label() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Tool
}

// PolicyOption configures a policy built by [Allow], [Deny], or [AskUser].
type PolicyOption func(*Policy)

// When attaches a predicate, narrowing the policy to calls whose arguments
// satisfy it.
func When(pred Predicate) PolicyOption {
	return func(p *Policy) { p.When = pred }
}

// Named sets the policy's label for logs and denial messages.
func Named(name string) PolicyOption {
	return func(p *Policy) { p.Name = name }
}

func newPolicy(tool string, d Decision, opts []PolicyOption) Policy {
	p := Policy{Tool: tool, Decision: d}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

// Allow approves calls to tool.
func Allow(tool string, opts ...PolicyOption) Policy {
	return newPolicy(tool, DecisionApprove, opts)
}

// Deny rejects calls to tool.
func Deny(tool string, opts ...PolicyOption) Policy {
	return newPolicy(tool, DecisionDeny, opts)
}

// AskUser defers calls to tool to the supplied handler.
func AskUser(tool string, handler AskUserHandler, opts ...PolicyOption) Policy {
	p := newPolicy(tool, DecisionAskUser, opts)
	p.AskUser = handler
	return p
}

// AllowTool approves calls to a builtin tool. It is [Allow] with a typed
// target.
func AllowTool(tool BuiltinTool, opts ...PolicyOption) Policy {
	return Allow(string(tool), opts...)
}

// DenyTool rejects calls to a builtin tool.
func DenyTool(tool BuiltinTool, opts ...PolicyOption) Policy {
	return Deny(string(tool), opts...)
}

// AskUserTool defers calls to a builtin tool to the supplied handler.
func AskUserTool(tool BuiltinTool, handler AskUserHandler, opts ...PolicyOption) Policy {
	return AskUser(string(tool), handler, opts...)
}

// mcpPolicies builds policies targeting an MCP server. With no tool names it
// produces a single server-wide rule; otherwise one rule per named tool.
func mcpPolicies(d Decision, server string, tools []string, handler AskUserHandler, opts []PolicyOption) []Policy {
	if len(tools) == 0 {
		p := newPolicy(server+"/*", d, opts)
		p.AskUser = handler
		if p.Name == "" {
			p.Name = fmt.Sprintf("%s_%s_all", strings.ToLower(string(d)), server)
		}
		return []Policy{p}
	}

	out := make([]Policy, 0, len(tools))
	for _, t := range tools {
		p := newPolicy(server+"/"+t, d, opts)
		p.AskUser = handler
		if p.Name == "" {
			p.Name = fmt.Sprintf("%s_%s_%s", strings.ToLower(string(d)), server, t)
		} else {
			p.Name = p.Name + "_" + t
		}
		out = append(out, p)
	}
	return out
}

// AllowMCP approves tools on an MCP server. Naming no tools covers the whole
// server.
func AllowMCP(server string, tools []string, opts ...PolicyOption) []Policy {
	return mcpPolicies(DecisionApprove, server, tools, nil, opts)
}

// DenyMCP rejects tools on an MCP server. Naming no tools covers the whole
// server.
func DenyMCP(server string, tools []string, opts ...PolicyOption) []Policy {
	return mcpPolicies(DecisionDeny, server, tools, nil, opts)
}

// AskUserMCP defers tools on an MCP server to the supplied handler. Naming no
// tools covers the whole server.
func AskUserMCP(server string, tools []string, handler AskUserHandler, opts ...PolicyOption) []Policy {
	return mcpPolicies(DecisionAskUser, server, tools, handler, opts)
}

// AllowAll approves every tool call without confirmation.
//
// Intended for autonomous agents and local development. This disables the
// SDK's safety gate entirely, so prefer a narrower policy set when the agent
// can run shell commands.
func AllowAll() Policy {
	return Allow(wildcard, Named("allow_all"))
}

// DenyAll rejects every tool call.
//
// Use it as a base rule with specific [Allow] overrides for a deny-by-default
// posture. Specific policies outrank wildcards, so
// []Policy{DenyAll(), Allow("view_file")} permits only view_file.
func DenyAll() Policy {
	return Deny(wildcard, Named("deny_all"))
}

// SafeDefaults approves read-only tools and routes everything else to handler.
func SafeDefaults(handler AskUserHandler) []Policy {
	ro := ReadOnlyTools()
	out := make([]Policy, 0, len(ro)+1)
	for _, t := range ro {
		out = append(out, AllowTool(t))
	}
	return append(out, AskUser(wildcard, handler))
}

// ConfirmRunCommand approves every tool except run_command, which is gated.
//
// With a nil handler, run_command is denied outright: the model still sees the
// tool, but calls are rejected. With a handler, run_command calls become an
// ask-user flow instead.
//
// This is the default policy set for a new [Agent], which is why a default
// agent can edit files but not run shell commands.
func ConfirmRunCommand(handler AskUserHandler) []Policy {
	const name = "confirm_run_command"
	gate := DenyTool(ToolRunCommand, Named(name))
	if handler != nil {
		gate = AskUserTool(ToolRunCommand, handler, Named(name))
	}
	return []Policy{gate, Allow(wildcard, Named(name))}
}

// WorkspaceOnly restricts file tools to the given workspace directories,
// denying reads, writes, and creates that target paths outside them.
//
// The returned policies are markers: containment is enforced by the harness at
// the platform layer, which can canonicalize paths reliably, so the in-process
// evaluator deliberately skips them. They are still forwarded to the harness
// as part of the session's policy configuration.
func WorkspaceOnly(workspaces []string) []Policy {
	// workspaces is accepted for symmetry with the harness-side configuration
	// and to keep call sites self-documenting; the paths themselves travel in
	// the session config, not in these marker policies.
	_ = workspaces

	ft := FileTools()
	out := make([]Policy, 0, len(ft))
	for _, t := range ft {
		out = append(out, DenyTool(t, Named(workspaceOnlyPolicyName)))
	}
	return out
}

// FlattenPolicies concatenates policies and policy groups into a single slice,
// so that builders returning []Policy compose with those returning Policy.
//
//	antigravity.FlattenPolicies(
//		antigravity.One(antigravity.DenyAll()),
//		antigravity.ConfirmRunCommand(nil),
//	)
func FlattenPolicies(groups ...[]Policy) []Policy {
	var out []Policy
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// One lifts a single policy into a group, for use with [FlattenPolicies].
func One(policies ...Policy) []Policy { return policies }

// matchesTarget reports whether a policy target selects this call.
func matchesTarget(target string, call ToolCall) bool {
	if target == wildcard {
		return true
	}
	if call.ServerName != "" {
		if rest, ok := strings.CutSuffix(target, "/*"); ok {
			return rest == call.ServerName
		}
		return target == call.ServerName+"/"+call.Name
	}
	return target == call.Name
}

// policyScope ranks a target by specificity: exact tools first, then
// server-wide MCP rules, then the global wildcard.
func policyScope(target string) int {
	switch {
	case target == wildcard:
		return 2
	case strings.HasSuffix(target, "/*"):
		return 1
	default:
		return 0
	}
}

// decisionRank orders decisions so the most restrictive wins ties.
func decisionRank(d Decision) int {
	switch d {
	case DecisionDeny:
		return 0
	case DecisionAskUser:
		return 1
	case DecisionApprove:
		return 2
	default:
		return 3
	}
}

// PolicyResult is the outcome of evaluating a call against a policy set.
type PolicyResult struct {
	// Allow reports whether the call may proceed.
	Allow bool
	// Message explains a denial, and is surfaced to the model.
	Message string
	// Policy names the rule that decided, and is empty when no rule matched.
	Policy string
}

// Enforcer evaluates tool calls against an ordered policy set.
//
// Build one with [NewEnforcer], which sorts the policies into priority order
// and validates them.
type Enforcer struct {
	// policies is the priority-ordered set used for in-process evaluation.
	policies []Policy
	// declared preserves the caller's original order, which is what the rule
	// ids sent to the harness index into.
	declared []Policy
	// dynamic maps a rule id to the policy the harness must ask us about.
	dynamic map[string]Policy
	log     *slog.Logger
}

// NewEnforcer sorts and validates policies, returning an evaluator.
//
// Policies are ordered by specificity and then restrictiveness; ties keep
// declaration order, so a caller can rely on it to break genuine ambiguity.
//
// It reports an error when an ask-user policy has no handler, or when a policy
// targets an MCP server that was not registered. The latter check fails closed:
// a typo in a server name would otherwise silently match nothing and let calls
// through.
func NewEnforcer(policies []Policy, registeredMCPServers []string) (*Enforcer, error) {
	sorted := slices.Clone(policies)
	slices.SortStableFunc(sorted, func(a, b Policy) int {
		if d := policyScope(a.Tool) - policyScope(b.Tool); d != 0 {
			return d
		}
		return decisionRank(a.Decision) - decisionRank(b.Decision)
	})

	for _, p := range sorted {
		if p.Decision == DecisionAskUser && p.AskUser == nil {
			return nil, fmt.Errorf(
				"%w: ask-user policy %q has no handler; pass one to AskUser",
				ErrInvalidConfig, p.label())
		}
		server, _, isMCP := strings.Cut(p.Tool, "/")
		if !isMCP || p.Tool == wildcard {
			continue
		}
		if !slices.Contains(registeredMCPServers, server) {
			return nil, fmt.Errorf(
				"%w: policy %q targets MCP server %q, which is not registered; "+
					"register it with WithMCPServers or correct the policy target",
				ErrInvalidConfig, p.label(), server)
		}
	}

	return &Enforcer{
		policies: sorted,
		declared: slices.Clone(policies),
		dynamic:  map[string]Policy{},
		log:      slog.Default(),
	}, nil
}

// Policies returns the enforcer's rules in evaluation order.
func (e *Enforcer) Policies() []Policy { return slices.Clone(e.policies) }

// Evaluate decides whether call may proceed.
//
// The first policy whose target and predicate both match determines the
// result. A call matching no policy is allowed, so a deny-by-default posture
// needs an explicit [DenyAll].
//
// Evaluation fails closed: if a predicate or ask-user handler returns an
// error, the call is denied and the error is reported in the result message
// rather than propagated, so that one broken rule cannot crash a session.
func (e *Enforcer) Evaluate(ctx context.Context, call ToolCall) PolicyResult {
	for _, p := range e.policies {
		// Workspace containment is enforced by the harness, which can
		// canonicalize paths; evaluating it here would double-deny.
		if p.Name == workspaceOnlyPolicyName {
			continue
		}
		if !matchesTarget(p.Tool, call) {
			continue
		}

		if p.When != nil {
			ok, err := p.When(ctx, call)
			if err != nil {
				e.log.Error("policy predicate failed, denying",
					"policy", p.label(), "tool", call.Name, "error", err)
				return PolicyResult{
					Allow:   false,
					Message: fmt.Sprintf("Policy evaluation failed for policy %q: %v", p.label(), err),
					Policy:  p.label(),
				}
			}
			if !ok {
				continue
			}
		}

		switch p.Decision {
		case DecisionDeny:
			e.log.Info("policy denied tool", "policy", p.label(), "tool", call.Name)
			return PolicyResult{
				Allow:   false,
				Message: fmt.Sprintf("Denied by policy %q.", p.label()),
				Policy:  p.label(),
			}

		case DecisionApprove:
			e.log.Debug("policy approved tool", "policy", p.label(), "tool", call.Name)
			return PolicyResult{Allow: true, Policy: p.label()}

		case DecisionAskUser:
			e.log.Info("policy requesting user approval", "policy", p.label(), "tool", call.Name)
			approved, err := p.AskUser(ctx, call)
			if err != nil {
				e.log.Error("ask-user handler failed, denying",
					"policy", p.label(), "tool", call.Name, "error", err)
				return PolicyResult{
					Allow:   false,
					Message: fmt.Sprintf("Policy evaluation failed for policy %q: %v", p.label(), err),
					Policy:  p.label(),
				}
			}
			if approved {
				return PolicyResult{Allow: true, Policy: p.label()}
			}
			return PolicyResult{
				Allow:   false,
				Message: fmt.Sprintf("User denied tool %q (policy %q).", call.Name, p.label()),
				Policy:  p.label(),
			}
		}
	}

	return PolicyResult{Allow: true}
}
