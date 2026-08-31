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

package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// Transport is the event channel to a harness.
//
// The upper layers depend only on this interface, so they can be exercised
// against an in-memory fake without a localharness binary.
type Transport interface {
	// Send writes an event to the harness.
	Send(ctx context.Context, ev *pb.InputEvent) error
	// Recv reads the next event. It returns [io.EOF] once the harness hangs
	// up. It is single-consumer: callers must not invoke it concurrently.
	Recv(ctx context.Context) (*pb.OutputEvent, error)
	// Close shuts the transport down. It is idempotent.
	Close() error
	// Stderr returns diagnostic output from the harness, if any.
	Stderr() string
}

const (
	// maxDialAttempts bounds the WebSocket connect retry loop. The harness
	// writes its OutputConfig before it finishes binding the listener, so the
	// first attempt often loses the race.
	maxDialAttempts = 5
	// dialBackoffBase is the first retry delay; it doubles each attempt.
	dialBackoffBase = 100 * time.Millisecond
)

// Conn exchanges events with a harness over a WebSocket, encoding each one as
// a protojson text frame.
//
// This is the only framing used after startup; the length-prefixed binary
// protobuf of the stdio handshake never appears here.
type Conn struct {
	ws *websocket.Conn

	sendMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
}

var _ Transport = (*Conn)(nil)

// NewConn wraps an established WebSocket. It takes ownership: closing the Conn
// closes the socket.
func NewConn(ws *websocket.Conn) *Conn {
	// The harness streams large payloads such as media, so the library's
	// conservative default read limit has to go.
	ws.SetReadLimit(-1)
	return &Conn{ws: ws}
}

// dial connects to the harness WebSocket, retrying with exponential backoff.
//
// Both localhost and 127.0.0.1 are attempted on every pass, because some
// environments do not resolve localhost and others bind only to it.
func dial(ctx context.Context, port int32, apiKey string) (*Conn, error) {
	hdr := http.Header{}
	hdr.Set("x-goog-api-key", apiKey)
	opts := &websocket.DialOptions{HTTPHeader: hdr}

	var lastErr error
	for attempt := range maxDialAttempts {
		for _, host := range []string{"localhost", "127.0.0.1"} {
			url := fmt.Sprintf("ws://%s:%d/", host, port)
			ws, _, err := websocket.Dial(ctx, url, opts)
			if err == nil {
				return NewConn(ws), nil
			}
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dialing the harness WebSocket on port %d: %w", port, ctx.Err())
		case <-time.After(dialBackoffBase * (1 << attempt)):
		}
	}
	return nil, fmt.Errorf("could not connect to the harness WebSocket on port %d after %d attempts: %w",
		port, maxDialAttempts, lastErr)
}

// Initialize performs the conversation handshake over the WebSocket and
// returns the harness's response, which carries any restored history and
// usage.
//
// This is the one frame the client sends as something other than an
// InputEvent: the protocol opens with a bare InitializeConversationEvent, and
// every frame after it is an InputEvent. Keeping the asymmetry here means the
// layers above only ever deal with [Transport].
func (c *Conn) Initialize(ctx context.Context, cfg *pb.HarnessConfig) (*pb.InitializeConversationResponse, error) {
	ev := pb.InitializeConversationEvent_builder{Config: cfg}.Build()
	data, err := protojson.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("marshaling InitializeConversationEvent: %w", err)
	}

	c.sendMu.Lock()
	err = c.ws.Write(ctx, websocket.MessageText, data)
	c.sendMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("sending InitializeConversationEvent: %w", err)
	}

	out, err := c.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("waiting for the initialize response: %w", err)
	}
	resp := out.GetInitializeConversationResponse()
	if resp == nil {
		return nil, fmt.Errorf(
			"the harness answered initialize with oneof field %v, not an InitializeConversationResponse",
			out.WhichEvent())
	}
	return resp, nil
}

// Send writes an InputEvent as a protojson text frame.
func (c *Conn) Send(ctx context.Context, ev *pb.InputEvent) error {
	data, err := protojson.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshaling InputEvent: %w", err)
	}

	// Serialized because a WebSocket frame must not be interleaved with
	// another, and the SDK writes from several goroutines (turns, tool
	// results, cancellations).
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("sending InputEvent: %w", err)
	}
	return nil
}

// Recv reads the next OutputEvent, reporting a normal hangup as [io.EOF].
func (c *Conn) Recv(ctx context.Context) (*pb.OutputEvent, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		if isClosed(err) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("reading OutputEvent: %w", err)
	}

	var ev pb.OutputEvent
	// The harness may run ahead of the vendored protos; tolerate fields we do
	// not know rather than failing the session on them.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("parsing OutputEvent: %w", err)
	}
	return &ev, nil
}

// Close shuts down the WebSocket. It is idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.ws.Close(websocket.StatusNormalClosure, "session ended")
		if isClosed(c.closeErr) {
			c.closeErr = nil
		}
	})
	return c.closeErr
}

// Stderr reports nothing for a bare WebSocket; only [Process] has a
// subprocess to collect it from.
func (c *Conn) Stderr() string { return "" }

// isClosed reports whether err means the peer hung up rather than something
// going wrong.
func isClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return true
	}
	return false
}
