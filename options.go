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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-steer/antigravity-sdk-go/internal/schema"
	"github.com/go-steer/antigravity-sdk-go/internal/wire"
)

// Option configures an [Agent].
//
// Options that set a single value replace whatever a previous option of the
// same kind set; options that register something — tools, hooks, policies,
// subagents, MCP servers — accumulate in the order given.
type Option func(*config)

// config is the accumulated result of applying every [Option], before
// defaults are filled in and validation runs.
type config struct {
	// Connection.
	binaryPath string
	env        map[string]string
	saveDir    string
	appDataDir string

	// Session identity.
	conversationID string
	continuation   SessionContinuationMode

	// Agent shape.
	instructions SystemInstructions
	capabilities CapabilitiesConfig
	tools        []Tool
	subagents    []SubagentConfig
	mcpServers   []MCPServer
	skillsPaths  []string

	workspaces    []string
	workspacesSet bool

	policies    []Policy
	policiesSet bool

	// Models. The shorthand fields build an endpoint for any model target
	// that does not carry one of its own.
	models   []ModelTarget
	model    string
	apiKey   string
	vertex   bool
	project  string
	location string

	retry  *RetryConfig
	budget *BudgetConfig

	hooks      *hookRunner
	triggers   []namedTrigger
	logger     *slog.Logger
	maxHistory int

	// deferredErrs collects failures from options, which return nothing and so
	// cannot report one themselves.
	deferredErrs []error
}

// newConfig returns the configuration a caller gets before any option runs.
//
// The defaults are deliberately usable on their own: every built-in tool is
// available, but run_command is denied by policy, so a default agent can read
// and write files without being able to execute arbitrary commands.
func newConfig() *config {
	return &config{
		capabilities: DefaultCapabilities(),
		policies:     ConfirmRunCommand(nil),
		vertex:       vertexFromEnv(),
		hooks:        newHookRunner(),
		maxHistory:   defaultMaxHistory,
	}
}

// vertexFromEnv reports whether the ambient environment selects the Gemini
// Enterprise Agent Platform, matching the Google Gen AI SDK's switches.
func vertexFromEnv() bool {
	truthy := func(name string) bool {
		switch strings.ToLower(os.Getenv(name)) {
		case "true", "1":
			return true
		default:
			return false
		}
	}
	return truthy("GOOGLE_GENAI_USE_VERTEXAI") || truthy("GOOGLE_GENAI_USE_ENTERPRISE")
}

// ---------------------------------------------------------------------------
// Connection
// ---------------------------------------------------------------------------

// WithBinaryPath sets the localharness executable to run.
//
// The default is discovered from the ANTIGRAVITY_HARNESS_PATH environment
// variable, then from PATH.
func WithBinaryPath(path string) Option {
	return func(c *config) { c.binaryPath = path }
}

// WithEnv supplies environment variables for the harness subprocess. They are
// merged over the parent environment rather than replacing it.
func WithEnv(env map[string]string) Option {
	return func(c *config) {
		if c.env == nil {
			c.env = map[string]string{}
		}
		for k, v := range env {
			c.env[k] = v
		}
	}
}

// WithSaveDir sets the directory where the harness persists session state, so
// a conversation can be resumed later. The default is a temporary directory
// discarded when the process exits.
func WithSaveDir(dir string) Option {
	return func(c *config) { c.saveDir = dir }
}

// WithAppDataDir sets the harness's application data directory, which must be
// an absolute path. The default is ~/.gemini/antigravity.
func WithAppDataDir(dir string) Option {
	return func(c *config) { c.appDataDir = dir }
}

// ---------------------------------------------------------------------------
// Session identity
// ---------------------------------------------------------------------------

// WithConversationID resumes a saved conversation by its identifier, which
// [Agent.ConversationID] reports for a running session.
//
// It needs a [WithSaveDir] pointing at the directory that session was written
// to, and usually a [WithSessionContinuation] mode as well.
func WithConversationID(id string) Option {
	return func(c *config) { c.conversationID = id }
}

// WithSessionContinuation selects how the session is established: resumed,
// created, or either.
func WithSessionContinuation(mode SessionContinuationMode) Option {
	return func(c *config) { c.continuation = mode }
}

// ---------------------------------------------------------------------------
// Agent shape
// ---------------------------------------------------------------------------

// WithInstructions sets the agent's system instructions.
//
// Pass [TemplatedInstructions] to append to the harness defaults, or
// [CustomInstructions] to replace them entirely. See [WithSystemPrompt] for
// the common case.
func WithInstructions(si SystemInstructions) Option {
	return func(c *config) { c.instructions = si }
}

// WithSystemPrompt appends a block of text to the harness's default system
// instructions. It is shorthand for a [TemplatedInstructions] with one
// section.
func WithSystemPrompt(text string) Option {
	return WithInstructions(TemplatedInstructions{
		Sections: []InstructionSection{{Content: text}},
	})
}

// WithCapabilities sets which built-in tools the agent may use and how it
// operates. It replaces the default capability set rather than merging with
// it; see [DefaultCapabilities] and [ReadOnlyCapabilities].
func WithCapabilities(cfg CapabilitiesConfig) Option {
	return func(c *config) { c.capabilities = cfg }
}

// WithTools registers custom tools with the root agent. The SDK executes them
// in-process when the model calls them.
func WithTools(tools ...Tool) Option {
	return func(c *config) { c.tools = append(c.tools, tools...) }
}

// WithSubagents registers named subagents the root agent may delegate to.
func WithSubagents(subagents ...SubagentConfig) Option {
	return func(c *config) { c.subagents = append(c.subagents, subagents...) }
}

// WithMCPServers registers Model Context Protocol servers whose tools become
// available to the agent.
func WithMCPServers(servers ...MCPServer) Option {
	return func(c *config) { c.mcpServers = append(c.mcpServers, servers...) }
}

// WithSkillsPaths adds directories to search for skills.
func WithSkillsPaths(paths ...string) Option {
	return func(c *config) { c.skillsPaths = append(c.skillsPaths, paths...) }
}

// WithWorkspaces sets the directories the agent works in. Paths are resolved
// to absolute form, with a leading ~ expanded. The default is the process's
// working directory.
//
// This does not by itself confine file tools to those directories; pair it
// with [WorkspaceOnly] for that.
func WithWorkspaces(paths ...string) Option {
	return func(c *config) {
		c.workspaces = append(c.workspaces, paths...)
		c.workspacesSet = true
	}
}

// WithPolicies replaces the default policy set with the given groups,
// flattened in order.
//
// Policy builders return groups, so they compose directly:
//
//	antigravity.WithPolicies(
//		antigravity.One(antigravity.DenyAll()),
//		antigravity.WorkspaceOnly(dirs),
//	)
//
// Calling it with no arguments removes every policy, which [New] rejects for
// an agent that has write tools or MCP servers: such an agent would run
// unsupervised.
func WithPolicies(groups ...[]Policy) Option {
	return func(c *config) {
		c.policies = FlattenPolicies(groups...)
		c.policiesSet = true
	}
}

// WithResponseSchema constrains the agent's final answer to the JSON shape of
// T, whose schema is derived by reflection the same way [NewTool] derives a
// tool's parameters.
//
// Read the result with [ChatResponse.StructuredOutput].
func WithResponseSchema[T any]() Option {
	return func(c *config) {
		var zero T
		s, err := schema.For(zero)
		if err != nil {
			// Recorded rather than returned: an Option cannot fail, so the
			// error surfaces from New with the rest of validation.
			c.capabilities.FinishToolSchemaJSON = ""
			c.deferErr("deriving a response schema", err)
			return
		}
		c.capabilities.FinishToolSchemaJSON = string(s)
	}
}

// WithResponseSchemaJSON constrains the agent's final answer to a JSON schema
// supplied directly, for a shape that does not correspond to a Go type.
func WithResponseSchemaJSON(jsonSchema string) Option {
	return func(c *config) { c.capabilities.FinishToolSchemaJSON = jsonSchema }
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

// WithModel selects the text model by name, using the credentials from
// [WithAPIKey] or [WithVertex]. Defaults to [DefaultModel].
func WithModel(name string) Option {
	return func(c *config) { c.model = name }
}

// WithModels configures model targets explicitly, each with its own endpoint.
// A target with no endpoint inherits the one built from [WithAPIKey] or
// [WithVertex]; a target with no name takes [DefaultModel] or
// [DefaultImageGenerationModel] according to its [ModelTarget.Types], so
// ModelTarget{Endpoint: …} means "the default model, served by this endpoint".
// A nameless target whose types have no single default — both modalities at
// once — is an error rather than a guess.
//
// Targets for a modality that is not configured — text or image — fall back to
// the package defaults, on the endpoint the shorthand options describe rather
// than on any endpoint named here.
//
// [WithModel] is not merged into these targets: it appends one of its own, so
// combining the two yields two text models, as it does in the Python SDK.
func WithModels(models ...ModelTarget) Option {
	return func(c *config) { c.models = append(c.models, models...) }
}

// WithAPIKey authenticates against the Gemini Developer API. When unset,
// GEMINI_API_KEY is used.
func WithAPIKey(key string) Option {
	return func(c *config) { c.apiKey = key }
}

// WithVertex targets the Gemini Enterprise Agent Platform, formerly Vertex AI,
// using Application Default Credentials.
//
// Empty arguments fall back to GOOGLE_CLOUD_PROJECT and GOOGLE_CLOUD_LOCATION.
// For express mode, combine [WithVertexExpress] with [WithAPIKey] instead.
func WithVertex(project, location string) Option {
	return func(c *config) {
		c.vertex = true
		c.project = project
		c.location = location
	}
}

// WithVertexExpress targets the Gemini Enterprise Agent Platform in express
// mode, authenticating with the key from [WithAPIKey] rather than with
// Application Default Credentials.
func WithVertexExpress() Option {
	return func(c *config) { c.vertex = true }
}

// ---------------------------------------------------------------------------
// Limits
// ---------------------------------------------------------------------------

// WithRetryConfig tunes how transient model API failures and malformed model
// output are retried. See [BenchmarkRetryConfig] for a preset suited to
// evaluation runs.
func WithRetryConfig(cfg *RetryConfig) Option {
	return func(c *config) { c.retry = cfg }
}

// WithBudget caps the resources a session may consume. A turn that would
// exceed a cap stops early, reporting the matching [StopReason].
func WithBudget(budget BudgetConfig) Option {
	return func(c *config) { c.budget = &budget }
}

// WithMaxHistory caps how many steps a conversation retains in memory. Older
// steps are dropped once the cap is reached; the harness keeps the full
// trajectory regardless. Zero restores the default.
func WithMaxHistory(n int) Option {
	return func(c *config) { c.maxHistory = n }
}

// WithLogger sets the logger the SDK writes diagnostics to. The default
// discards everything.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

// WithSessionStartHook runs fn once when the session starts.
func WithSessionStartHook(fn SessionHook) Option {
	return func(c *config) { c.hooks.sessionStart = append(c.hooks.sessionStart, fn) }
}

// WithSessionEndHook runs fn once when the session ends.
func WithSessionEndHook(fn SessionHook) Option {
	return func(c *config) { c.hooks.sessionEnd = append(c.hooks.sessionEnd, fn) }
}

// WithPreTurnHook runs fn before each turn, letting it refuse the prompt.
func WithPreTurnHook(fn PreTurnHook) Option {
	return func(c *config) { c.hooks.preTurn = append(c.hooks.preTurn, fn) }
}

// WithPostTurnHook runs fn after each turn completes.
func WithPostTurnHook(fn PostTurnHook) Option {
	return func(c *config) { c.hooks.postTurn = append(c.hooks.postTurn, fn) }
}

// WithPreToolCallHook runs fn before every tool call, letting it deny the call
// or rewrite its arguments.
//
// Registering one satisfies the safety requirement that [WithPolicies]
// otherwise covers, since it can gate every call itself.
func WithPreToolCallHook(fn PreToolCallHook) Option {
	return func(c *config) { c.hooks.preTool = append(c.hooks.preTool, fn) }
}

// WithPostToolCallHook runs fn after every tool call, successful or not.
func WithPostToolCallHook(fn PostToolCallHook) Option {
	return func(c *config) { c.hooks.postTool = append(c.hooks.postTool, fn) }
}

// WithToolErrorHook lets fn reword the failure message a tool reports to the
// model.
func WithToolErrorHook(fn OnToolErrorHook) Option {
	return func(c *config) { c.hooks.toolError = append(c.hooks.toolError, fn) }
}

// WithInteractionHook answers questions the agent asks the user. Without one,
// every question is reported unanswered.
func WithInteractionHook(fn OnInteractionHook) Option {
	return func(c *config) { c.hooks.interaction = append(c.hooks.interaction, fn) }
}

// WithCompactionHook runs fn when the harness compacts the model's context.
func WithCompactionHook(fn OnCompactionHook) Option {
	return func(c *config) { c.hooks.compaction = append(c.hooks.compaction, fn) }
}

// WithStopHook runs fn when the agent is about to go idle, letting it send the
// agent back to work.
func WithStopHook(fn StopHook) Option {
	return func(c *config) { c.hooks.stop = append(c.hooks.stop, fn) }
}

// WithStepObserver runs fn for every step the harness reports, for tracing and
// progress reporting. It runs on the read loop and must not block; see
// [StepObserver].
func WithStepObserver(fn StepObserver) Option {
	return func(c *config) { c.hooks.step = append(c.hooks.step, fn) }
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// deferErr records a problem an [Option] could not report, since options
// return nothing. resolve surfaces them all.
func (c *config) deferErr(msg string, err error) {
	c.deferredErrs = append(c.deferredErrs, fmt.Errorf("%s: %w", msg, err))
}

// resolve fills in defaults and validates the whole configuration. Everything
// downstream may assume the result is coherent.
func (c *config) resolve() error {
	if err := errors.Join(c.deferredErrs...); err != nil {
		return &ConfigError{Err: err}
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	c.hooks.logger = c.logger
	if c.maxHistory <= 0 {
		c.maxHistory = defaultMaxHistory
	}

	if err := c.resolveCapabilities(); err != nil {
		return err
	}
	if err := c.resolveTools(); err != nil {
		return err
	}
	if err := c.resolveSubagents(); err != nil {
		return err
	}
	if err := c.resolveMCPServers(); err != nil {
		return err
	}
	if err := c.resolveWorkspaces(); err != nil {
		return err
	}
	if err := c.resolveAppDataDir(); err != nil {
		return err
	}
	if err := c.resolveModels(); err != nil {
		return err
	}
	return c.checkSupervision()
}

func (c *config) resolveCapabilities() error {
	if err := c.capabilities.validate(); err != nil {
		return &ConfigError{Field: "capabilities", Err: err}
	}
	if warning := c.capabilities.interactiveToolWarning(); warning != "" {
		c.logger.Warn(warning)
	}
	return nil
}

// resolveTools rejects duplicate tool names, which the model could not tell
// apart and the dispatcher could not route.
func (c *config) resolveTools() error {
	seen := map[string]bool{}
	for _, t := range c.tools {
		if t == nil {
			return &ConfigError{Field: "tools", Err: fmt.Errorf("a registered tool is nil")}
		}
		if t.Name() == "" {
			return &ConfigError{Field: "tools", Err: fmt.Errorf("a registered tool has no name")}
		}
		if seen[t.Name()] {
			return &ConfigError{Field: "tools", Err: fmt.Errorf("tool %q is registered more than once", t.Name())}
		}
		seen[t.Name()] = true
	}
	return nil
}

// resolveSubagents validates each subagent and checks that every name
// referenced by an allowlist is actually declared, since a typo there would
// silently make a subagent unreachable.
func (c *config) resolveSubagents() error {
	declared := make(map[string]bool, len(c.subagents))
	for i := range c.subagents {
		s := &c.subagents[i]
		if err := s.validate(); err != nil {
			return &ConfigError{Field: "subagents", Err: err}
		}
		if declared[s.Name] {
			return &ConfigError{Field: "subagents", Err: fmt.Errorf("subagent %q is declared more than once", s.Name)}
		}
		declared[s.Name] = true
	}

	check := func(field string, names []string) error {
		for _, n := range names {
			if !declared[n] {
				return &ConfigError{Field: field, Err: fmt.Errorf(
					"unknown subagent %q; declared subagents are %v", n, sortedKeys(declared))}
			}
		}
		return nil
	}
	if err := check("capabilities.AllowedSubagents", c.capabilities.AllowedSubagents); err != nil {
		return err
	}
	for i := range c.subagents {
		s := &c.subagents[i]
		if s.Capabilities == nil {
			continue
		}
		field := fmt.Sprintf("subagent %q AllowedSubagents", s.Name)
		if err := check(field, s.Capabilities.AllowedSubagents); err != nil {
			return err
		}
	}
	return nil
}

func (c *config) resolveMCPServers() error {
	seen := map[string]bool{}
	for _, s := range c.mcpServers {
		if s == nil {
			return &ConfigError{Field: "mcpServers", Err: fmt.Errorf("a registered MCP server is nil")}
		}
		if err := s.validate(); err != nil {
			return &ConfigError{Field: "mcpServers", Err: err}
		}
		if seen[s.Name()] {
			return &ConfigError{Field: "mcpServers", Err: fmt.Errorf("MCP server %q is registered more than once", s.Name())}
		}
		seen[s.Name()] = true
	}
	return nil
}

// resolveWorkspaces normalizes each path and defaults to the working
// directory, matching what a caller running the agent from a project root
// expects.
func (c *config) resolveWorkspaces() error {
	if !c.workspacesSet {
		cwd, err := os.Getwd()
		if err != nil {
			return &ConfigError{Field: "workspaces", Err: fmt.Errorf("determining the working directory: %w", err)}
		}
		c.workspaces = []string{cwd}
		return nil
	}
	out := make([]string, 0, len(c.workspaces))
	for _, p := range c.workspaces {
		resolved, err := wire.NormalizeWorkspace(p)
		if err != nil {
			return &ConfigError{Field: "workspaces", Err: fmt.Errorf("resolving %q: %w", p, err)}
		}
		if resolved != "" {
			out = append(out, resolved)
		}
	}
	c.workspaces = out
	return nil
}

func (c *config) resolveAppDataDir() error {
	if c.appDataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return &ConfigError{Field: "appDataDir", Err: fmt.Errorf("determining the home directory: %w", err)}
		}
		c.appDataDir = filepath.Join(home, ".gemini", "antigravity")
		return nil
	}
	if !filepath.IsAbs(c.appDataDir) {
		return &ConfigError{Field: "appDataDir", Err: fmt.Errorf(
			"must be an absolute path, got %q", c.appDataDir)}
	}
	return nil
}

// resolveModels merges explicitly configured targets with the shorthand model
// and the package defaults.
//
// Explicit targets win; the shorthand adds one more; defaults fill in only the
// modalities nobody covered, so an agent always has both a text and an image
// model even when the caller named just one.
//
// A target that names no model gets the package default for its modality, so
// it can say "whatever the default model is, but on this endpoint" — which is
// what selecting a service tier takes, the tier being a property of the
// endpoint. Naming [DefaultModel] explicitly says something subtly different:
// it pins today's default rather than tracking it.
//
// Without the defaulting the nameless target would still cover its modality,
// suppress the default appended below, and reach the harness with an empty
// name, which the harness rejects mid-turn with "tModel: model is empty". See
// docs/DESIGN.md §6 — this is a deliberate divergence from the Python SDK.
func (c *config) resolveModels() error {
	merged := slices.Clone(c.models)
	if c.model != "" {
		merged = append(merged, ModelTarget{Name: c.model, Types: []ModelType{ModelTypeText}})
	}

	covered := map[ModelType]bool{}
	for i := range merged {
		if merged[i].Endpoint == nil {
			merged[i].Endpoint = c.newEndpoint()
		}
		if merged[i].Name == "" {
			merged[i].Name = merged[i].defaultName()
		}
		for _, t := range merged[i].modelTypes() {
			covered[t] = true
		}
	}
	if !covered[ModelTypeText] {
		merged = append(merged, ModelTarget{
			Name:     DefaultModel,
			Types:    []ModelType{ModelTypeText},
			Endpoint: c.newEndpoint(),
		})
	}
	if !covered[ModelTypeImage] {
		merged = append(merged, ModelTarget{
			Name:     DefaultImageGenerationModel,
			Types:    []ModelType{ModelTypeImage},
			Endpoint: c.newEndpoint(),
		})
	}

	for i := range merged {
		if err := merged[i].validate(); err != nil {
			return &ConfigError{Field: "models", Err: err}
		}
	}
	c.models = merged
	return nil
}

// newEndpoint builds a fresh endpoint from the shorthand credentials. Each
// model gets its own, because validation resolves environment fallbacks into
// the value.
func (c *config) newEndpoint() ModelEndpoint {
	if c.vertex {
		return &VertexEndpoint{Project: c.project, Location: c.location, APIKey: c.apiKey}
	}
	return &GeminiAPIEndpoint{APIKey: c.apiKey}
}

// checkSupervision refuses an agent that can change the world with nothing
// deciding whether it may.
//
// Either a policy set or a pre-tool hook counts as supervision: both see every
// call before it runs. Read-only agents with no MCP servers need neither.
func (c *config) checkSupervision() error {
	dangerous := c.capabilities.hasWriteTools() || len(c.mcpServers) > 0
	supervised := len(c.policies) > 0 || len(c.hooks.preTool) > 0
	if !dangerous || supervised {
		return nil
	}
	return &ConfigError{Field: "policies", Err: fmt.Errorf(
		"write tools or MCP servers are enabled with no policies and no pre-tool hook; " +
			"pass WithPolicies(One(AllowAll())) to approve every call, " +
			"WithPolicies(One(DenyAll()), One(AllowTool(...))) to allow specific ones, " +
			"or register a WithPreToolCallHook")}
}

// sortedKeys returns a map's keys in order, for deterministic error messages.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
