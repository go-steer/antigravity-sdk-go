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
	"strings"
	"testing"
)

func mustEnforcer(t *testing.T, policies []Policy, servers ...string) *Enforcer {
	t.Helper()
	e, err := NewEnforcer(policies, servers)
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	return e
}

func TestEnforcerSpecificBeatsWildcard(t *testing.T) {
	// Declaration order puts the wildcard first; priority ordering must still
	// let the specific rule decide.
	e := mustEnforcer(t, []Policy{DenyAll(), Allow("view_file")})

	if got := e.Evaluate(t.Context(), ToolCall{Name: "view_file"}); !got.Allow {
		t.Errorf("view_file: Allow = false, want true (%s)", got.Message)
	}
	if got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"}); got.Allow {
		t.Error("run_command: Allow = true, want false")
	}
}

func TestEnforcerDenyBeatsApproveAtSameScope(t *testing.T) {
	e := mustEnforcer(t, []Policy{Allow("run_command"), Deny("run_command")})
	if got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"}); got.Allow {
		t.Error("Allow = true, want false: deny must outrank approve at equal specificity")
	}
}

func TestEnforcerNoMatchAllows(t *testing.T) {
	e := mustEnforcer(t, []Policy{Deny("run_command")})
	if got := e.Evaluate(t.Context(), ToolCall{Name: "view_file"}); !got.Allow {
		t.Error("an unmatched call must be allowed")
	}
}

func TestConfirmRunCommandDefault(t *testing.T) {
	// The default posture: everything except run_command.
	e := mustEnforcer(t, ConfirmRunCommand(nil))

	if got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"}); got.Allow {
		t.Error("run_command must be denied when no handler is supplied")
	}
	for _, tool := range []string{"view_file", "edit_file", "create_file", "search_web"} {
		if got := e.Evaluate(t.Context(), ToolCall{Name: tool}); !got.Allow {
			t.Errorf("%s: Allow = false, want true (%s)", tool, got.Message)
		}
	}
}

func TestConfirmRunCommandWithHandler(t *testing.T) {
	var asked string
	handler := func(_ context.Context, c ToolCall) (bool, error) {
		asked = c.Name
		return true, nil
	}
	e := mustEnforcer(t, ConfirmRunCommand(handler))

	got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"})
	if !got.Allow {
		t.Errorf("Allow = false, want true (%s)", got.Message)
	}
	if asked != "run_command" {
		t.Errorf("handler saw %q, want run_command", asked)
	}
}

func TestEnforcerAskUserDenial(t *testing.T) {
	handler := func(context.Context, ToolCall) (bool, error) { return false, nil }
	e := mustEnforcer(t, []Policy{AskUser("run_command", handler)})

	got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"})
	if got.Allow {
		t.Fatal("Allow = true, want false")
	}
	if !strings.Contains(got.Message, "User denied") {
		t.Errorf("Message = %q, want it to mention the user denial", got.Message)
	}
}

func TestEnforcerPredicateFailsClosed(t *testing.T) {
	boom := errors.New("boom")
	e := mustEnforcer(t, []Policy{
		Allow("run_command", When(func(context.Context, ToolCall) (bool, error) {
			return true, boom
		})),
	})

	got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"})
	if got.Allow {
		t.Fatal("a failing predicate must deny the call, not allow it")
	}
	if !strings.Contains(got.Message, "boom") {
		t.Errorf("Message = %q, want it to surface the predicate error", got.Message)
	}
}

func TestEnforcerAskUserHandlerFailsClosed(t *testing.T) {
	e := mustEnforcer(t, []Policy{
		AskUser("run_command", func(context.Context, ToolCall) (bool, error) {
			return true, errors.New("handler exploded")
		}),
	})
	if got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"}); got.Allow {
		t.Error("a failing ask-user handler must deny the call")
	}
}

func TestEnforcerPredicateNarrowsMatch(t *testing.T) {
	// A predicate that rejects falls through to the next policy rather than
	// deciding the call.
	dangerous := func(_ context.Context, c ToolCall) (bool, error) {
		return strings.Contains(string(c.Args), "rm -rf"), nil
	}
	e := mustEnforcer(t, []Policy{
		Deny("run_command", When(dangerous), Named("no_rm")),
		Allow("run_command"),
	})

	safe := ToolCall{Name: "run_command", Args: []byte(`{"cmd":"ls"}`)}
	if got := e.Evaluate(t.Context(), safe); !got.Allow {
		t.Errorf("safe command denied: %s", got.Message)
	}

	unsafe := ToolCall{Name: "run_command", Args: []byte(`{"cmd":"rm -rf /"}`)}
	if got := e.Evaluate(t.Context(), unsafe); got.Allow {
		t.Error("dangerous command was allowed")
	}
}

func TestEnforcerMCPTargets(t *testing.T) {
	e := mustEnforcer(t, FlattenPolicies(
		DenyMCP("weather", []string{"delete_city"}),
		AllowMCP("weather", nil),
	), "weather")

	allowed := ToolCall{Name: "get_forecast", ServerName: "weather"}
	if got := e.Evaluate(t.Context(), allowed); !got.Allow {
		t.Errorf("server-wide allow failed: %s", got.Message)
	}

	denied := ToolCall{Name: "delete_city", ServerName: "weather"}
	if got := e.Evaluate(t.Context(), denied); got.Allow {
		t.Error("specific MCP deny must outrank the server-wide allow")
	}
}

func TestNewEnforcerRejectsUnregisteredMCPServer(t *testing.T) {
	_, err := NewEnforcer(DenyMCP("typo", []string{"x"}), []string{"weather"})
	if err == nil {
		t.Fatal("expected an error for an unregistered MCP server")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not match ErrInvalidConfig", err)
	}
}

func TestNewEnforcerRejectsAskUserWithoutHandler(t *testing.T) {
	_, err := NewEnforcer([]Policy{{Tool: "run_command", Decision: DecisionAskUser}}, nil)
	if err == nil {
		t.Fatal("expected an error for an ask-user policy with no handler")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not match ErrInvalidConfig", err)
	}
}

func TestWorkspaceOnlySkippedInProcess(t *testing.T) {
	// Workspace containment is the harness's job; evaluating the marker
	// policies here would deny every file operation.
	e := mustEnforcer(t, WorkspaceOnly([]string{"/repo"}))
	if got := e.Evaluate(t.Context(), ToolCall{Name: "view_file"}); !got.Allow {
		t.Errorf("workspace_only markers must not deny in-process: %s", got.Message)
	}
}

func TestSafeDefaults(t *testing.T) {
	handler := func(context.Context, ToolCall) (bool, error) { return false, nil }
	e := mustEnforcer(t, SafeDefaults(handler))

	for _, tool := range ReadOnlyTools() {
		if got := e.Evaluate(t.Context(), ToolCall{Name: string(tool)}); !got.Allow {
			t.Errorf("%s: read-only tool denied: %s", tool, got.Message)
		}
	}
	if got := e.Evaluate(t.Context(), ToolCall{Name: "run_command"}); got.Allow {
		t.Error("run_command must route to the handler, which denied")
	}
}

func TestEnforcerStableWithinEqualPriority(t *testing.T) {
	// Two rules with identical scope and decision: the first declared wins.
	e := mustEnforcer(t, []Policy{
		Allow("view_file", Named("first")),
		Allow("view_file", Named("second")),
	})
	if got := e.Evaluate(t.Context(), ToolCall{Name: "view_file"}); got.Policy != "first" {
		t.Errorf("Policy = %q, want first: sorting must be stable", got.Policy)
	}
}

func TestAskUserMCP(t *testing.T) {
	var asked []string
	handler := func(_ context.Context, c ToolCall) (bool, error) {
		asked = append(asked, c.Name)
		return c.Name == "get_forecast", nil
	}

	e := mustEnforcer(t, AskUserMCP("weather", []string{"get_forecast", "delete_city"}, handler), "weather")

	if got := e.Evaluate(t.Context(), ToolCall{Name: "get_forecast", ServerName: "weather"}); !got.Allow {
		t.Errorf("get_forecast: Allow = false, want true (%s)", got.Message)
	}
	if got := e.Evaluate(t.Context(), ToolCall{Name: "delete_city", ServerName: "weather"}); got.Allow {
		t.Error("delete_city: Allow = true, want the handler's refusal to stand")
	}
	if len(asked) != 2 {
		t.Errorf("the handler saw %v, want both calls", asked)
	}

	// Naming no tools covers the server as a whole, including tools that did
	// not exist when the policy was written.
	whole := mustEnforcer(t, AskUserMCP("weather", nil, handler), "weather")
	if got := whole.Evaluate(t.Context(), ToolCall{Name: "brand_new", ServerName: "weather"}); got.Allow {
		t.Error("a server-wide ask-user policy did not cover an unnamed tool")
	}
}

func TestEnforcerPolicies(t *testing.T) {
	// Policies reports the rules in the order they will be evaluated, which is
	// by specificity rather than by declaration.
	e := mustEnforcer(t, []Policy{DenyAll(), Allow("view_file")})

	got := e.Policies()
	if len(got) != 2 {
		t.Fatalf("Policies = %v, want both rules", got)
	}
	if got[0].Tool != "view_file" {
		t.Errorf("Policies = %v, want the specific rule first", got)
	}

	// The slice is a copy, so a caller cannot reorder the live rule set.
	got[0] = Policy{Tool: "mutated"}
	if e.Policies()[0].Tool != "view_file" {
		t.Error("Policies returned the enforcer's own slice")
	}
}
