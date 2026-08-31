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

// UsageMetadata reports token consumption for a model call.
//
// Counts are cumulative when read from a [Conversation] and per-step when read
// from a [Step]. A [Step] that did not involve a model call carries no usage at
// all, which is distinct from a step that reported zero tokens.
type UsageMetadata struct {
	// PromptTokenCount is the number of input tokens.
	PromptTokenCount int64
	// CachedContentTokenCount is the number of input tokens served from cache.
	// These are a subset of PromptTokenCount, not an addition to it.
	CachedContentTokenCount int64

	// CandidatesTokenCount is the number of generated output tokens,
	// excluding thinking.
	CandidatesTokenCount int64
	// ThoughtsTokenCount is the number of tokens spent on reasoning.
	ThoughtsTokenCount int64

	// TotalTokenCount is prompt + candidates + thoughts.
	TotalTokenCount int64

	// ServiceTier is the tier that served the request.
	ServiceTier ServiceTier
}

// Add returns the sum of u and other.
//
// When the two values disagree on service tier, the result reports
// [ServiceTierStandard], since a mixed total cannot be attributed to a single
// tier. An unset tier on either side takes the other side's value.
func (u UsageMetadata) Add(other UsageMetadata) UsageMetadata {
	return UsageMetadata{
		PromptTokenCount:        u.PromptTokenCount + other.PromptTokenCount,
		CachedContentTokenCount: u.CachedContentTokenCount + other.CachedContentTokenCount,
		CandidatesTokenCount:    u.CandidatesTokenCount + other.CandidatesTokenCount,
		ThoughtsTokenCount:      u.ThoughtsTokenCount + other.ThoughtsTokenCount,
		TotalTokenCount:         u.TotalTokenCount + other.TotalTokenCount,
		ServiceTier:             mergeTiers(u.ServiceTier, other.ServiceTier),
	}
}

// Sub returns u minus other, which is useful for deriving a single turn's
// usage from two cumulative snapshots. The service tier is taken from u, or
// from other when u has none.
func (u UsageMetadata) Sub(other UsageMetadata) UsageMetadata {
	tier := u.ServiceTier
	if tier == "" {
		tier = other.ServiceTier
	}
	return UsageMetadata{
		PromptTokenCount:        u.PromptTokenCount - other.PromptTokenCount,
		CachedContentTokenCount: u.CachedContentTokenCount - other.CachedContentTokenCount,
		CandidatesTokenCount:    u.CandidatesTokenCount - other.CandidatesTokenCount,
		ThoughtsTokenCount:      u.ThoughtsTokenCount - other.ThoughtsTokenCount,
		TotalTokenCount:         u.TotalTokenCount - other.TotalTokenCount,
		ServiceTier:             tier,
	}
}

func mergeTiers(a, b ServiceTier) ServiceTier {
	switch {
	case a == b:
		return a
	case a == "":
		return b
	case b == "":
		return a
	default:
		return ServiceTierStandard
	}
}

// BudgetConfig caps resource consumption for a whole session. A turn that
// would exceed a cap stops early with a corresponding [StopReason].
//
// Zero means unlimited for every field.
type BudgetConfig struct {
	// MaxModelCalls caps model invocations across the session.
	MaxModelCalls int32
	// MaxToolCalls caps tool invocations across the session, from any source.
	MaxToolCalls int32
	// MaxInputTokens caps net uncached input tokens, that is prompt tokens
	// minus cached content tokens, across all turns.
	MaxInputTokens int64
	// MaxOutputTokens caps output tokens, candidates plus thoughts, across all
	// turns.
	MaxOutputTokens int64
	// MaxTotalTokens caps net uncached input plus output tokens across all
	// turns.
	MaxTotalTokens int64
}

// StopReason explains why an execution turn ended.
type StopReason string

const (
	// StopUnspecified indicates normal completion.
	StopUnspecified StopReason = "UNSPECIFIED"
	// StopMaxModelCalls indicates BudgetConfig.MaxModelCalls was exceeded.
	StopMaxModelCalls StopReason = "MAX_MODEL_CALLS_EXCEEDED"
	// StopMaxToolCalls indicates BudgetConfig.MaxToolCalls was exceeded.
	StopMaxToolCalls StopReason = "MAX_TOOL_CALLS_EXCEEDED"
	// StopMaxInputTokens indicates BudgetConfig.MaxInputTokens was exceeded.
	StopMaxInputTokens StopReason = "MAX_INPUT_TOKENS_EXCEEDED" //nolint:gosec // G101: a budget stop reason, not a credential.
	// StopMaxOutputTokens indicates BudgetConfig.MaxOutputTokens was exceeded.
	StopMaxOutputTokens StopReason = "MAX_OUTPUT_TOKENS_EXCEEDED"
	// StopMaxTotalTokens indicates BudgetConfig.MaxTotalTokens was exceeded.
	StopMaxTotalTokens StopReason = "MAX_TOTAL_TOKENS_EXCEEDED"
	// StopQuotaExhausted indicates the backend model API quota ran out.
	StopQuotaExhausted StopReason = "QUOTA_EXHAUSTED"
)
