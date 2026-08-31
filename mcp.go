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
	"regexp"
	"time"
)

// mcpNamePattern constrains MCP server names to what the Gemini API tool
// naming specification permits.
var mcpNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// MCPServer configures a Model Context Protocol server whose tools are exposed
// to the agent. The two implementations are [MCPStdioServer] and
// [MCPHTTPServer].
type MCPServer interface {
	// Name uniquely identifies the server.
	Name() string
	validate() error
}

// mcpCommon holds the fields shared by every MCP server transport.
type mcpCommon struct {
	// ServerName uniquely identifies this server. Only alphanumerics,
	// hyphens, and underscores are permitted.
	ServerName string

	// Timeout bounds connecting to the server and listing its tools. Zero
	// uses the harness default.
	Timeout time.Duration

	// EnabledTools is an explicit allowlist of tool names from this server.
	// Mutually exclusive with DisabledTools. Nil enables every tool.
	EnabledTools []string

	// DisabledTools is an explicit denylist of tool names from this server.
	// Mutually exclusive with EnabledTools. Disabled tools are removed from
	// the model's context entirely, saving tokens.
	DisabledTools []string
}

func (c mcpCommon) Name() string { return c.ServerName }

func (c mcpCommon) validateCommon() error {
	if !mcpNamePattern.MatchString(c.ServerName) {
		return fmt.Errorf(
			"MCP server name %q must match %s (alphanumerics, hyphens, and underscores only)",
			c.ServerName, mcpNamePattern)
	}
	if c.EnabledTools != nil && c.DisabledTools != nil {
		return fmt.Errorf("MCP server %q: EnabledTools and DisabledTools are mutually exclusive", c.ServerName)
	}
	return nil
}

// MCPStdioServer is an MCP server run as a subprocess and spoken to over
// stdio.
type MCPStdioServer struct {
	mcpCommon

	// Command is the executable to run.
	Command string
	// Args are passed to Command.
	Args []string
	// Env is merged into the subprocess environment.
	Env map[string]string
}

// NewMCPStdioServer returns a stdio MCP server configuration.
func NewMCPStdioServer(name, command string, args ...string) *MCPStdioServer {
	return &MCPStdioServer{
		mcpCommon: mcpCommon{ServerName: name},
		Command:   command,
		Args:      args,
	}
}

func (s *MCPStdioServer) validate() error {
	if err := s.validateCommon(); err != nil {
		return err
	}
	if s.Command == "" {
		return fmt.Errorf("MCP server %q: Command must not be empty", s.ServerName)
	}
	return nil
}

// MCPHTTPServer is an MCP server reached over Streamable HTTP.
type MCPHTTPServer struct {
	mcpCommon

	// URL is the HTTP endpoint.
	URL string
	// Headers are sent with the connection request.
	Headers map[string]string
	// ConnectTimeout bounds establishing the connection. Zero means 30s.
	ConnectTimeout time.Duration
	// SSEReadTimeout bounds reads on the server-sent events stream. Zero
	// means 300s.
	SSEReadTimeout time.Duration
	// KeepAliveOnClose leaves the connection open when the session ends.
	// The default behavior terminates it.
	KeepAliveOnClose bool
}

// NewMCPHTTPServer returns a Streamable HTTP MCP server configuration with the
// default timeouts applied.
func NewMCPHTTPServer(name, url string) *MCPHTTPServer {
	return &MCPHTTPServer{
		mcpCommon:      mcpCommon{ServerName: name},
		URL:            url,
		ConnectTimeout: 30 * time.Second,
		SSEReadTimeout: 300 * time.Second,
	}
}

func (s *MCPHTTPServer) validate() error {
	if err := s.validateCommon(); err != nil {
		return err
	}
	if s.URL == "" {
		return fmt.Errorf("MCP server %q: URL must not be empty", s.ServerName)
	}
	return nil
}
