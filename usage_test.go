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

import "testing"

func TestUsageAdd(t *testing.T) {
	a := UsageMetadata{
		PromptTokenCount:        100,
		CachedContentTokenCount: 40,
		CandidatesTokenCount:    20,
		ThoughtsTokenCount:      5,
		TotalTokenCount:         125,
		ServiceTier:             ServiceTierStandard,
	}
	b := UsageMetadata{
		PromptTokenCount:        10,
		CachedContentTokenCount: 4,
		CandidatesTokenCount:    2,
		ThoughtsTokenCount:      1,
		TotalTokenCount:         13,
		ServiceTier:             ServiceTierStandard,
	}

	got := a.Add(b)
	want := UsageMetadata{
		PromptTokenCount:        110,
		CachedContentTokenCount: 44,
		CandidatesTokenCount:    22,
		ThoughtsTokenCount:      6,
		TotalTokenCount:         138,
		ServiceTier:             ServiceTierStandard,
	}
	if got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}

	// Neither operand is modified, so a running total can be accumulated by
	// reassignment without aliasing surprises.
	if a.TotalTokenCount != 125 || b.TotalTokenCount != 13 {
		t.Error("Add modified one of its operands")
	}
}

func TestUsageSub(t *testing.T) {
	// Two cumulative snapshots subtract to one turn's consumption, which is how
	// LastTurnUsage is derived.
	after := UsageMetadata{
		PromptTokenCount:        100,
		CachedContentTokenCount: 40,
		CandidatesTokenCount:    20,
		ThoughtsTokenCount:      5,
		TotalTokenCount:         125,
		ServiceTier:             ServiceTierPriority,
	}
	before := UsageMetadata{
		PromptTokenCount:        60,
		CachedContentTokenCount: 30,
		CandidatesTokenCount:    8,
		ThoughtsTokenCount:      2,
		TotalTokenCount:         70,
	}

	got := after.Sub(before)
	want := UsageMetadata{
		PromptTokenCount:        40,
		CachedContentTokenCount: 10,
		CandidatesTokenCount:    12,
		ThoughtsTokenCount:      3,
		TotalTokenCount:         55,
		ServiceTier:             ServiceTierPriority,
	}
	if got != want {
		t.Errorf("Sub = %+v, want %+v", got, want)
	}
}

func TestUsageSubTakesTheTierFromEitherSide(t *testing.T) {
	// A snapshot taken before any model call has no tier of its own, so the
	// other side's tier stands rather than being erased.
	got := UsageMetadata{}.Sub(UsageMetadata{ServiceTier: ServiceTierPriority})
	if got.ServiceTier != ServiceTierPriority {
		t.Errorf("ServiceTier = %q, want the tier of the value being subtracted", got.ServiceTier)
	}
}

func TestMergeTiers(t *testing.T) {
	tests := []struct {
		name string
		a, b ServiceTier
		want ServiceTier
	}{
		{"agreement", ServiceTierPriority, ServiceTierPriority, ServiceTierPriority},
		{"neither set", "", "", ""},
		{"only the left set", ServiceTierPriority, "", ServiceTierPriority},
		{"only the right set", "", ServiceTierPriority, ServiceTierPriority},
		// A total spanning two tiers belongs to neither, so it reports the
		// baseline rather than claiming the more expensive one.
		{"disagreement", ServiceTierPriority, ServiceTierStandard, ServiceTierStandard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeTiers(tt.a, tt.b); got != tt.want {
				t.Errorf("mergeTiers(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
