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
	"testing"
)

func TestDispatchStepNotifiesEveryObserver(t *testing.T) {
	r := newHookRunner()

	var seen []string
	r.step = []StepObserver{
		func(_ context.Context, _ *HookContext, s Step) { seen = append(seen, "first:"+s.ID) },
		func(_ context.Context, _ *HookContext, s Step) { seen = append(seen, "second:"+s.ID) },
	}
	r.dispatchStep(t.Context(), Step{ID: "s1"})

	want := []string{"first:s1", "second:s1"}
	if len(seen) != len(want) {
		t.Fatalf("got %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

func TestDispatchStepSharesTheTurnScope(t *testing.T) {
	r := newHookRunner()

	// A pre-turn hook opens the scope; an observer must land in that same one,
	// so state set during the turn is visible to it.
	r.preTurn = []PreTurnHook{
		func(_ context.Context, hc *HookContext, _ []Content) (TurnDecision, error) {
			hc.Set("marker", 42)
			return TurnDecision{}, nil
		},
	}
	var got any
	r.step = []StepObserver{
		func(_ context.Context, hc *HookContext, _ Step) { got, _ = hc.Get("marker") },
	}

	r.dispatchPreTurn(t.Context(), []Content{Text("hi")})
	r.dispatchStep(t.Context(), Step{})

	if got != 42 {
		t.Errorf("the observer saw %v, want the turn state set by the pre-turn hook", got)
	}
}

func TestDispatchStepContainsAPanic(t *testing.T) {
	r := newHookRunner()

	reached := false
	r.step = []StepObserver{
		func(context.Context, *HookContext, Step) { panic("kaboom") },
		func(context.Context, *HookContext, Step) { reached = true },
	}

	// A panicking observer runs on the read loop; letting it escape would take
	// down the session.
	r.dispatchStep(t.Context(), Step{})

	if !reached {
		t.Error("the second observer did not run after the first panicked")
	}
}

func TestDispatchStepWithNoObservers(t *testing.T) {
	r := newHookRunner()
	r.dispatchStep(t.Context(), Step{})
}

func TestStepObserversAreNotLifecycleHooks(t *testing.T) {
	// The harness pauses the agent for each enabled lifecycle hook. Observers
	// are client-side only, so registering one must not add a round trip.
	r := newHookRunner()
	r.step = []StepObserver{func(context.Context, *HookContext, Step) {}}

	if got := r.enabledHooks(); len(got) != 0 {
		t.Errorf("enabledHooks = %v, want none", got)
	}
}

func TestHookContextUpdate(t *testing.T) {
	hc := &HookContext{}

	// An unset key reads as nil, so a counter can start from nothing without
	// the caller seeding it first.
	if got := hc.Update("calls", func(current any) any {
		if current == nil {
			return 1
		}
		return current.(int) + 1
	}); got != 1 {
		t.Errorf("Update = %v, want 1 for an unset key", got)
	}

	if got := hc.Update("calls", func(current any) any { return current.(int) + 1 }); got != 2 {
		t.Errorf("Update = %v, want the incremented value", got)
	}
	if got, _ := hc.Get("calls"); got != 2 {
		t.Errorf("Get = %v, want the stored value", got)
	}
}

func TestHookContextUpdateShadowsItsParent(t *testing.T) {
	parent := &HookContext{}
	parent.Set("calls", 5)
	child := &HookContext{parent: parent}

	// The child sees the inherited value but writes its own, so one subagent's
	// bookkeeping cannot corrupt the session's.
	if got := child.Update("calls", func(current any) any { return current.(int) + 1 }); got != 6 {
		t.Errorf("Update = %v, want the inherited value incremented", got)
	}
	if got, _ := parent.Get("calls"); got != 5 {
		t.Errorf("the parent now reads %v, want it left alone", got)
	}
}
