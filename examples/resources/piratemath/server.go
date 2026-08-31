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

// Package piratemath is a small Model Context Protocol server used by the MCP
// examples. It offers two tools that do arithmetic badly and describe it
// enthusiastically.
//
// It implements MCP by hand rather than pulling in an MCP library, so that the
// SDK's own module stays free of dependencies it does not need. That means only
// the parts the examples exercise are here: initialize, tools/list, tools/call,
// and ping, over either stdio or Streamable HTTP.
package piratemath

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// protocolVersion is the MCP revision this server speaks. A client asking for
// a different one is answered with this, which is what the spec prescribes
// when the requested version is unsupported.
const protocolVersion = "2025-06-18"

// ---------------------------------------------------------------------------
// JSON-RPC 2.0
// ---------------------------------------------------------------------------

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether the message expects no reply. JSON-RPC
// notifications are exactly the messages without an id.
func (r *request) isNotification() bool { return len(r.ID) == 0 }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// ---------------------------------------------------------------------------
// Tools
// ---------------------------------------------------------------------------

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// twoNumbers is the input schema shared by both tools.
func twoNumbers() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a": map[string]any{"type": "integer"},
			"b": map[string]any{"type": "integer"},
		},
		"required": []string{"a", "b"},
	}
}

var tools = []toolDef{{
	Name:        "pirate_multiply",
	Description: "Does multiplication like a pirate.",
	InputSchema: twoNumbers(),
}, {
	Name:        "pirate_divide",
	Description: "Does division like a pirate.",
	InputSchema: twoNumbers(),
}}

type callParams struct {
	Name      string `json:"name"`
	Arguments struct {
		A int `json:"a"`
		B int `json:"b"`
	} `json:"arguments"`
}

func pirateMultiply(a, b int) string {
	return fmt.Sprintf(`Pirate Multiplication: %d x %d

**Yo ho ho!** The pirate multiplication be done!

| Factor | Value |
|--------|-------|
| a | %d |
| b | %d |

**Result:** `+"`%d`"+`

*Seven seas math - we add 'em, multiply by 7, subtract 13!*`, a, b, a, b, (a+b)*7-13)
}

func pirateDivide(a, b int) string {
	return fmt.Sprintf(`Pirate Division: %d / %d

**Blimey!** The division be calculated!

| Operand | Value |
|---------|-------|
| a | %d |
| b | %d |

**Result:** `+"`%d`"+`

*Pirates triple the first, double the second, add the meaning of life!*`, a, b, a, b, a*3+b*2+42)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// handle routes one request and returns the reply, or nil for a notification.
func handle(req *request) *response {
	reply := func(result any) *response {
		return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *response {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "Pirate Math", "version": "1.0.0"},
		})

	case "tools/list":
		return reply(map[string]any{"tools": tools})

	case "tools/call":
		var params callParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return fail(codeInvalidParams, "invalid arguments: "+err.Error())
		}
		var text string
		switch params.Name {
		case "pirate_multiply":
			text = pirateMultiply(params.Arguments.A, params.Arguments.B)
		case "pirate_divide":
			text = pirateDivide(params.Arguments.A, params.Arguments.B)
		default:
			return fail(codeInvalidParams, "unknown tool "+params.Name)
		}
		return reply(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": false,
		})

	case "ping":
		return reply(map[string]any{})

	default:
		if req.isNotification() {
			// notifications/initialized and friends need no reply and are not
			// errors.
			return nil
		}
		return fail(codeMethodNotFound, "unknown method "+req.Method)
	}
}

// ---------------------------------------------------------------------------
// stdio transport
// ---------------------------------------------------------------------------

// ServeStdio reads newline-delimited JSON-RPC messages from r and writes
// replies to w, returning when r is exhausted.
func ServeStdio(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// MCP messages are small, but tool results can be larger than the default
	// 64KiB line limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	enc := json.NewEncoder(w)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		resp := handle(&req)
		if resp == nil || req.isNotification() {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// ---------------------------------------------------------------------------
// Streamable HTTP transport
// ---------------------------------------------------------------------------

// Handler returns an http.Handler implementing the Streamable HTTP transport
// at whatever path it is mounted on.
func Handler() http.Handler {
	var mu sync.Mutex
	sessions := 0

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			// This server never initiates messages, so there is nothing to
			// stream on a GET.
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed JSON-RPC message", http.StatusBadRequest)
			return
		}

		resp := handle(&req)
		if resp == nil || req.isNotification() {
			// Accepted, nothing to say.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		if req.Method == "initialize" {
			mu.Lock()
			sessions++
			id := fmt.Sprintf("pirate-math-%d", sessions)
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", id)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// Start runs the Streamable HTTP transport on an ephemeral port of localhost
// and returns its URL along with a function that shuts it down.
//
// It binds all interfaces because the harness health-checks the server from
// outside this process.
func Start(ctx context.Context) (url string, stop func(), err error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return "", nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", Handler())
	server := &http.Server{Handler: mux}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Println("piratemath: serve:", err)
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	stop = func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-done
	}
	return fmt.Sprintf("http://localhost:%d/mcp", port), stop, nil
}
