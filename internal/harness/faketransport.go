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
	"io"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// FakeTransport simulates the harness side of the WebSocket, so the SDK can be
// tested without a localharness binary.
//
// It mirrors the Python SDK's TestLocalHarness: Push injects an event as if
// the harness had emitted it, and Sent observes what the SDK wrote back.
//
// This lives in the non-test build so packages outside internal/harness can
// use it in their own tests.
type FakeTransport struct {
	out chan *pb.OutputEvent
	in  chan *pb.InputEvent

	mu   sync.Mutex
	sent []*pb.InputEvent

	closeOnce sync.Once
	closed    chan struct{}

	// StderrText is returned by Stderr, letting tests exercise diagnostic
	// paths.
	StderrText string
}

var _ Transport = (*FakeTransport)(nil)

// NewFakeTransport returns a fake with buffered channels deep enough that
// tests can queue events without a reader running.
func NewFakeTransport() *FakeTransport {
	return &FakeTransport{
		out:    make(chan *pb.OutputEvent, 64),
		in:     make(chan *pb.InputEvent, 64),
		closed: make(chan struct{}),
	}
}

// Push queues an event for the SDK to receive.
func (f *FakeTransport) Push(ev *pb.OutputEvent) {
	select {
	case f.out <- ev:
	case <-f.closed:
	}
}

// Send records an event written by the SDK.
func (f *FakeTransport) Send(ctx context.Context, ev *pb.InputEvent) error {
	// Cloned so a test's assertions cannot be perturbed by later mutation of
	// the caller's message.
	clone := proto.Clone(ev).(*pb.InputEvent)

	f.mu.Lock()
	f.sent = append(f.sent, clone)
	f.mu.Unlock()

	select {
	case f.in <- clone:
		return nil
	case <-f.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Recv delivers the next pushed event, or [io.EOF] once the fake is closed.
func (f *FakeTransport) Recv(ctx context.Context) (*pb.OutputEvent, error) {
	select {
	case ev := <-f.out:
		return ev, nil
	case <-f.closed:
		// Drain anything queued before the close, so a test that pushes and
		// immediately closes still sees its events.
		select {
		case ev := <-f.out:
			return ev, nil
		default:
			return nil, io.EOF
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// WaitSent blocks until the SDK sends an event, or the context ends.
func (f *FakeTransport) WaitSent(ctx context.Context) (*pb.InputEvent, error) {
	select {
	case ev := <-f.in:
		return ev, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Sent returns every event the SDK has written so far.
func (f *FakeTransport) Sent() []*pb.InputEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.InputEvent(nil), f.sent...)
}

// Close simulates the harness hanging up. It is idempotent.
func (f *FakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

// Stderr returns the canned diagnostic text, if a test set any.
func (f *FakeTransport) Stderr() string { return f.StderrText }
