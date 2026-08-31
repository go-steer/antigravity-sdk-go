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

import "math"

// maxUint32 is the largest value a protobuf uint32 wire field can carry.
const maxUint32 = math.MaxUint32

// ModelAPIRetryConfig tunes retry behavior for transient model API errors,
// using exponential backoff with jitter.
//
// Zero-valued fields fall back to the harness defaults, so a partially
// populated config overrides only what it sets.
type ModelAPIRetryConfig struct {
	// MaxRetries caps retry attempts for transient API errors.
	MaxRetries uint32
	// InitialSleep is the first backoff delay, in milliseconds.
	InitialSleepMS uint32
	// ExponentialMultiplier scales the delay after each attempt.
	ExponentialMultiplier float64
	// JitterRange randomizes the delay to avoid thundering herds.
	JitterRange float64
}

// ModelOutputRetryConfig tunes how often malformed model output is retried.
type ModelOutputRetryConfig struct {
	// MaxRetries caps retry attempts for output that fails validation.
	MaxRetries uint32
}

// RetryConfig combines retry behavior for model API calls and for output
// validation.
//
// Omitting it entirely, or leaving fields zero, lets the harness apply its
// built-in interactive defaults. Configure it only to override those.
type RetryConfig struct {
	// APIRetry governs transient API failures.
	APIRetry *ModelAPIRetryConfig
	// OutputRetry governs malformed model output.
	OutputRetry *ModelOutputRetryConfig
}

// BenchmarkRetryConfig returns a configuration tuned for evaluation suites,
// automated benchmarks, and load testing.
//
// It sets an effectively unbounded retry tolerance for transient API errors
// such as 429 rate limits and 503 throttling, so quota pressure does not crash
// a long-running eval. Model output retries are left at the harness default so
// measured behavior still matches production.
func BenchmarkRetryConfig() *RetryConfig {
	return &RetryConfig{
		APIRetry: &ModelAPIRetryConfig{
			MaxRetries:     maxUint32,
			InitialSleepMS: 1000,
		},
	}
}
