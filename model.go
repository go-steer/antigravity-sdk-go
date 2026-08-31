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
	"os"
)

// Default models used when the caller does not name one.
const (
	// DefaultModel is the text model used when none is configured.
	DefaultModel = "gemini-3.7-flash"
	// DefaultImageGenerationModel is the image model used when none is
	// configured.
	DefaultImageGenerationModel = "gemini-3.1-flash-lite-image"
)

// ThinkingLevel controls how much reasoning a Gemini model performs before
// responding. See https://ai.google.dev/gemini-api/docs/thinking#thinking-levels
type ThinkingLevel string

const (
	ThinkingMinimal   ThinkingLevel = "minimal"
	ThinkingLow       ThinkingLevel = "low"
	ThinkingMedium    ThinkingLevel = "medium"
	ThinkingHigh      ThinkingLevel = "high"
	ThinkingExtraHigh ThinkingLevel = "extra_high"
)

// ServiceTier selects the compute queue priority and rate-limit fallback
// behavior for inference.
// See https://ai.google.dev/gemini-api/docs/priority-inference
type ServiceTier string

const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierPriority ServiceTier = "priority"
	ServiceTierFlex     ServiceTier = "flex"
)

// ModelType discriminates what a configured model is for.
type ModelType string

const (
	ModelTypeText  ModelType = "text"
	ModelTypeImage ModelType = "image"
)

// GeminiModelOptions carries Gemini-specific inference options.
type GeminiModelOptions struct {
	ThinkingLevel ThinkingLevel
	ServiceTier   ServiceTier
}

// ModelEndpoint describes how to reach and authenticate against a model
// backend. The implementations are [GeminiAPIEndpoint] and [VertexEndpoint].
type ModelEndpoint interface {
	// Validate reports whether the endpoint is usable, consulting the
	// environment for credentials the caller did not supply.
	Validate() error
	isModelEndpoint()
}

// GeminiAPIEndpoint targets the Gemini Developer API.
type GeminiAPIEndpoint struct {
	// APIKey authenticates the request. When empty, GEMINI_API_KEY is used.
	APIKey string
	// BaseURL overrides the API host. Setting it disables local validation,
	// deferring to the external service.
	BaseURL string
	// HTTPHeaders are added to every request.
	HTTPHeaders map[string]string
	// Options carries Gemini-specific inference options.
	Options *GeminiModelOptions
}

func (*GeminiAPIEndpoint) isModelEndpoint() {}

// Validate reports whether an API key is available, either explicitly or via
// GEMINI_API_KEY. A custom BaseURL skips the check, since an external gateway
// may authenticate differently.
func (e *GeminiAPIEndpoint) Validate() error {
	if e.BaseURL != "" {
		return nil
	}
	if e.APIKey == "" && os.Getenv("GEMINI_API_KEY") == "" {
		return fmt.Errorf(
			"a Gemini API key is required: set the GEMINI_API_KEY environment variable " +
				"or pass WithAPIKey")
	}
	return nil
}

// VertexEndpoint targets the Gemini Enterprise Agent Platform, formerly Vertex
// AI.
//
// Two authentication modes are supported and are mutually exclusive: standard
// mode, using Project and Location with Application Default Credentials, and
// express mode, using APIKey.
type VertexEndpoint struct {
	// Project is the GCP project ID. Falls back to GOOGLE_CLOUD_PROJECT.
	Project string
	// Location is the GCP region. Falls back to GOOGLE_CLOUD_LOCATION.
	Location string
	// APIKey selects express mode. Mutually exclusive with Project/Location.
	APIKey string
	// BaseURL overrides the API host. Setting it disables local validation and
	// suppresses the environment fallbacks, matching the Google Gen AI SDK, so
	// that ambient shell project metadata does not leak into an external proxy.
	BaseURL string
	// HTTPHeaders are added to every request.
	HTTPHeaders map[string]string
	// Options carries Gemini-specific inference options.
	Options *GeminiModelOptions
}

func (*VertexEndpoint) isModelEndpoint() {}

// resolveEnv fills Project and Location from the environment. It is skipped
// when BaseURL or APIKey is set, mirroring the Python SDK.
func (e *VertexEndpoint) resolveEnv() {
	if e.BaseURL != "" || e.APIKey != "" {
		return
	}
	if e.Project == "" {
		e.Project = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}
	if e.Location == "" {
		e.Location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
}

// Validate reports whether exactly one authentication mode is fully specified.
func (e *VertexEndpoint) Validate() error {
	if e.BaseURL != "" {
		return nil
	}
	e.resolveEnv()

	regional := e.Project != "" && e.Location != ""
	anyRegional := e.Project != "" || e.Location != ""
	express := e.APIKey != ""

	if anyRegional && express {
		return fmt.Errorf(
			"cannot specify both APIKey (express mode) and Project/Location (standard mode) on VertexEndpoint")
	}
	if !regional && !express {
		return fmt.Errorf("for Vertex AI, either Project and Location, or APIKey, must be set")
	}
	return nil
}

// ModelTarget configures a single model and the endpoint that serves it.
type ModelTarget struct {
	// Name is the model identifier, such as "gemini-3.7-flash".
	Name string
	// Types declares what the model is used for. Defaults to text.
	Types []ModelType
	// Endpoint routes and authenticates requests for this model.
	Endpoint ModelEndpoint
}

func (m *ModelTarget) validate() error {
	if m.Endpoint == nil {
		return fmt.Errorf("model %q must have an endpoint configured", m.Name)
	}
	if err := m.Endpoint.Validate(); err != nil {
		return fmt.Errorf("model %q: %w", m.Name, err)
	}
	return nil
}

// modelTypes returns the model's declared types, defaulting to text.
func (m *ModelTarget) modelTypes() []ModelType {
	if len(m.Types) == 0 {
		return []ModelType{ModelTypeText}
	}
	return m.Types
}
