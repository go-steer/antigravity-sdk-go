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
	"strings"
	"testing"
	"time"
)

func TestNewMCPStdioServer(t *testing.T) {
	s := NewMCPStdioServer("weather", "weatherd", "--port", "0")

	if s.Name() != "weather" || s.Command != "weatherd" {
		t.Errorf("server = %+v", s)
	}
	if len(s.Args) != 2 || s.Args[0] != "--port" {
		t.Errorf("Args = %v, want the variadic arguments kept in order", s.Args)
	}
	if err := s.validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestNewMCPHTTPServer(t *testing.T) {
	s := NewMCPHTTPServer("docs", "https://mcp.example/sse")

	if s.Name() != "docs" || s.URL != "https://mcp.example/sse" {
		t.Errorf("server = %+v", s)
	}
	// The constructor applies the defaults, so a caller who never touches the
	// timeouts still gets bounded ones.
	if s.ConnectTimeout != 30*time.Second || s.SSEReadTimeout != 300*time.Second {
		t.Errorf("timeouts = %v / %v, want the defaults", s.ConnectTimeout, s.SSEReadTimeout)
	}
	if err := s.validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestMCPServerValidation(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServer
		want   string
	}{
		{
			// Names travel into tool identifiers of the form "server/tool", so
			// anything that could confuse that grammar is rejected up front.
			name:   "a name with a slash",
			server: NewMCPStdioServer("weather/eu", "weatherd"),
			want:   "must match",
		},
		{
			name:   "an empty name",
			server: NewMCPStdioServer("", "weatherd"),
			want:   "must match",
		},
		{
			name:   "a stdio server with no command",
			server: NewMCPStdioServer("weather", ""),
			want:   "Command must not be empty",
		},
		{
			name:   "an HTTP server with no URL",
			server: NewMCPHTTPServer("docs", ""),
			want:   "URL must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.server.validate()
			if err == nil {
				t.Fatalf("%+v was accepted", tt.server)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestMCPToolFiltersAreExclusive(t *testing.T) {
	// Enabling and disabling at once has no coherent meaning, and silently
	// picking one would hand the agent a tool set the caller did not intend.
	stdio := NewMCPStdioServer("weather", "weatherd")
	stdio.EnabledTools = []string{"forecast"}
	stdio.DisabledTools = []string{"alerts"}
	if err := stdio.validate(); err == nil {
		t.Error("a stdio server with both tool filters was accepted")
	}

	http := NewMCPHTTPServer("docs", "https://mcp.example")
	http.EnabledTools = []string{"search"}
	http.DisabledTools = []string{"delete"}
	if err := http.validate(); err == nil {
		t.Error("an HTTP server with both tool filters was accepted")
	}
}
