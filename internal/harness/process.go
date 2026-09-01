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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

const (
	// handshakeTimeout bounds the stdio exchange, so a binary that starts but
	// never speaks does not hang the caller forever.
	handshakeTimeout = 30 * time.Second
	// shutdownGrace is how long a closed harness gets to exit on its own
	// before it is killed.
	shutdownGrace = 5 * time.Second
	// stderrDrainGrace is how long a failure path waits for the stderr reader
	// to reach EOF before giving up on it. A harness that rejects something
	// explains itself on stderr and then exits, so reading the buffer the
	// instant the socket dies races the explanation; without this wait the
	// diagnostic is there on most runs and missing on the rest.
	stderrDrainGrace = 250 * time.Millisecond
	// maxStderr caps retained harness stderr, which is unbounded in principle.
	maxStderr = 64 << 10
	// maxConfigSize rejects an implausible handshake length rather than
	// allocating on it, which is the visible symptom of a desynchronized
	// stream.
	maxConfigSize = 1 << 20
)

// Options configures a harness subprocess.
type Options struct {
	// BinaryPath is the executable to run. Required.
	BinaryPath string
	// StorageDirectory is where the harness persists session state. An empty
	// value leaves the choice to the harness.
	StorageDirectory string
	// Env supplies environment variables to the harness. When non-nil it is
	// both merged over the parent environment for the subprocess and passed
	// through in the handshake, matching the Python SDK.
	Env map[string]string
}

// Process is a running harness subprocess together with its WebSocket
// session.
type Process struct {
	cmd    *exec.Cmd
	conn   *Conn
	stderr *stderrBuffer

	closeOnce sync.Once
	closeErr  error
}

var _ Transport = (*Process)(nil)

// Start launches the harness, completes the stdio handshake, and dials the
// WebSocket it reports.
//
// On any failure the subprocess is killed and its stderr is attached to the
// returned [*StartError], since that output is usually the only explanation
// available.
func Start(ctx context.Context, opts Options) (_ *Process, err error) {
	if opts.BinaryPath == "" {
		return nil, errors.New("harness: BinaryPath is required")
	}

	// #nosec G204 -- BinaryPath is the harness the caller configured or that
	// discovery found on PATH. Launching it is what this package is for, and
	// no argument comes from the model.
	cmd := exec.Command(opts.BinaryPath)
	if opts.Env != nil {
		cmd.Env = mergedEnv(opts.Env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("harness: opening stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("harness: opening stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("harness: opening stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("harness: starting %s: %w", opts.BinaryPath, err)
	}

	// Drain stderr continuously. Leaving it unread would eventually block the
	// harness on a full pipe buffer, and the captured text is what makes a
	// failed handshake diagnosable at all.
	stderr := newStderrBuffer()
	go stderr.consume(stderrPipe)

	p := &Process{cmd: cmd, stderr: stderr}

	// Nothing past this point may leak the subprocess.
	defer func() {
		if err != nil {
			p.kill()
			stderr.waitDrained(stderrDrainGrace)
			err = &StartError{Err: err, Stderr: stderr.String()}
		}
	}()

	outCfg, err := handshake(ctx, stdin, stdout, opts)
	if err != nil {
		return nil, err
	}

	conn, err := dial(ctx, outCfg.GetPort(), outCfg.GetApiKey())
	if err != nil {
		return nil, err
	}
	p.conn = conn

	return p, nil
}

// handshake writes the InputConfig and reads back the OutputConfig.
//
// Both messages are binary protobuf framed by a 4-byte little-endian length.
// This is the only place in the protocol that framing appears; everything
// afterwards is protojson over the WebSocket.
func handshake(ctx context.Context, stdin io.Writer, stdout io.Reader, opts Options) (*pb.OutputConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	in := pb.InputConfig_builder{
		StorageDirectory: proto.String(opts.StorageDirectory),
		ClientInfo:       ClientInfo(),
		Env:              opts.Env,
	}.Build()

	payload, err := proto.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshaling InputConfig: %w", err)
	}

	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)

	if _, err := stdin.Write(frame); err != nil {
		return nil, fmt.Errorf("writing InputConfig: %w", err)
	}

	// The read runs on its own goroutine so the timeout can fire: a pipe read
	// is not context-aware and would otherwise block indefinitely.
	type result struct {
		cfg *pb.OutputConfig
		err error
	}
	done := make(chan result, 1)
	go func() {
		cfg, err := readOutputConfig(stdout)
		done <- result{cfg, err}
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for the harness OutputConfig: %w", ctx.Err())
	case r := <-done:
		return r.cfg, r.err
	}
}

func readOutputConfig(stdout io.Reader) (*pb.OutputConfig, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(stdout, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("reading the OutputConfig length: %w", err)
	}
	n := binary.LittleEndian.Uint32(lenBuf[:])
	if n > maxConfigSize {
		return nil, fmt.Errorf(
			"OutputConfig length %d exceeds %d, so the stream is desynchronized", n, maxConfigSize)
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(stdout, buf); err != nil {
		return nil, fmt.Errorf("reading the OutputConfig body: %w", err)
	}

	var cfg pb.OutputConfig
	if err := proto.Unmarshal(buf, &cfg); err != nil {
		return nil, fmt.Errorf("parsing the OutputConfig: %w", err)
	}
	if cfg.GetPort() == 0 {
		return nil, errors.New("the harness reported no port in its OutputConfig")
	}
	return &cfg, nil
}

// Initialize performs the conversation handshake over the WebSocket.
//
// A failure here kills the subprocess and reports its stderr, because a
// harness that rejects the config has usually explained itself there and
// nowhere else. That stderr is also read for the one cause the SDK can name on
// the user's behalf: a harness too old to understand the config it was sent.
func (p *Process) Initialize(ctx context.Context, cfg *pb.HarnessConfig) (*pb.InitializeConversationResponse, error) {
	resp, err := p.conn.Initialize(ctx, cfg)
	if err != nil {
		p.kill()
		p.stderr.waitDrained(stderrDrainGrace)
		stderr := p.stderr.String()
		return nil, &StartError{Err: diagnoseSkew(err, stderr), Stderr: stderr}
	}
	return resp, nil
}

// Send writes an event to the harness.
func (p *Process) Send(ctx context.Context, ev *pb.InputEvent) error {
	return p.conn.Send(ctx, ev)
}

// Recv reads the next event, reporting a hangup as [io.EOF].
func (p *Process) Recv(ctx context.Context) (*pb.OutputEvent, error) {
	return p.conn.Recv(ctx)
}

// Stderr returns whatever the harness has written to standard error.
func (p *Process) Stderr() string { return p.stderr.String() }

// Close shuts down the WebSocket and the subprocess. It is idempotent.
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		if p.conn != nil {
			// A clean close lets the harness flush and exit on its own; the
			// wait below is what actually decides the outcome.
			_ = p.conn.Close()
		}
		p.closeErr = p.shutdown()
	})
	return p.closeErr
}

// shutdown waits briefly for a clean exit before killing the subprocess.
func (p *Process) shutdown() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()

	select {
	case err := <-done:
		// A nonzero exit provoked by our own close is not a failure worth
		// reporting to the caller.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return err
	case <-time.After(shutdownGrace):
		p.kill()
		<-done
		return nil
	}
}

// kill terminates the subprocess immediately, ignoring errors from one that
// has already exited.
func (p *Process) kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// StartError reports a failed harness startup, carrying the subprocess's
// stderr because it is usually the only diagnostic available.
type StartError struct {
	Err    error
	Stderr string
}

func (e *StartError) Error() string {
	if e.Stderr == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + "\nharness stderr:\n" + e.Stderr
}

func (e *StartError) Unwrap() error { return e.Err }

// mergedEnv overlays extra onto the parent environment.
func mergedEnv(extra map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))

	for _, kv := range base {
		if k, _, ok := strings.Cut(kv, "="); ok {
			if _, overridden := extra[k]; overridden {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// stderrBuffer accumulates harness stderr up to a cap, discarding the excess.
type stderrBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer

	// drained closes when consume returns, which is when the harness's stderr
	// pipe has reached EOF and the buffer will not grow again.
	drained chan struct{}
}

func newStderrBuffer() *stderrBuffer { return &stderrBuffer{drained: make(chan struct{})} }

func (b *stderrBuffer) consume(r io.Reader) {
	defer close(b.drained)

	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			b.mu.Lock()
			if remaining := maxStderr - b.buf.Len(); remaining > 0 {
				b.buf.Write(chunk[:min(n, remaining)])
			}
			b.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (b *stderrBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitDrained blocks until the harness's stderr pipe reaches EOF, or d elapses.
//
// It is bounded because the pipe stays open for as long as any descendant of
// the harness holds its write end, which is not something a failure path may
// wait on indefinitely.
func (b *stderrBuffer) waitDrained(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-b.drained:
	case <-timer.C:
	}
}
