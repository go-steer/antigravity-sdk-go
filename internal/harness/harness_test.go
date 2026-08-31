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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/google/go-cmp/cmp"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// frame length-prefixes a message the way the harness stdio protocol does.
func frame(t *testing.T, m proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out
}

func TestHandshakeRoundTrip(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdout := strings.NewReader(string(frame(t, pb.OutputConfig_builder{
		Port:   proto.Int32(41234),
		ApiKey: proto.String("secret"),
	}.Build())))

	// Read the InputConfig the SDK writes, so the pipe does not block.
	type got struct {
		cfg *pb.InputConfig
		err error
	}
	read := make(chan got, 1)
	go func() {
		var lenBuf [4]byte
		if _, err := io.ReadFull(stdinR, lenBuf[:]); err != nil {
			read <- got{err: err}
			return
		}
		body := make([]byte, binary.LittleEndian.Uint32(lenBuf[:]))
		if _, err := io.ReadFull(stdinR, body); err != nil {
			read <- got{err: err}
			return
		}
		var cfg pb.InputConfig
		read <- got{cfg: &cfg, err: proto.Unmarshal(body, &cfg)}
	}()

	out, err := handshake(t.Context(), stdinW, stdout, Options{
		StorageDirectory: "/tmp/session",
		Env:              map[string]string{"FOO": "bar"},
	})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if out.GetPort() != 41234 {
		t.Errorf("Port = %d, want 41234", out.GetPort())
	}
	if out.GetApiKey() != "secret" {
		t.Errorf("ApiKey = %q, want secret", out.GetApiKey())
	}

	r := <-read
	if r.err != nil {
		t.Fatalf("reading the InputConfig the SDK wrote: %v", r.err)
	}
	if r.cfg.GetStorageDirectory() != "/tmp/session" {
		t.Errorf("StorageDirectory = %q, want /tmp/session", r.cfg.GetStorageDirectory())
	}
	if r.cfg.GetEnv()["FOO"] != "bar" {
		t.Errorf("Env = %v, want FOO=bar", r.cfg.GetEnv())
	}
	if lang := r.cfg.GetClientInfo().GetLanguage(); lang != "go" {
		t.Errorf("ClientInfo.Language = %q, want go", lang)
	}
	// The edition default must survive a round trip; the harness relies on it
	// to decide where to listen.
	if addr := r.cfg.GetBindAddress(); addr != "localhost" {
		t.Errorf("BindAddress = %q, want the edition default localhost", addr)
	}
}

func TestHandshakeRejectsTruncatedStdout(t *testing.T) {
	// A harness that dies before writing anything is the common failure.
	_, err := handshake(t.Context(), io.Discard, strings.NewReader(""), Options{})
	if err == nil {
		t.Fatal("expected an error for empty stdout")
	}
	if !strings.Contains(err.Error(), "OutputConfig length") {
		t.Errorf("error = %v, want it to name the length read", err)
	}
}

func TestHandshakeRejectsDesyncLength(t *testing.T) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], 1<<30)

	_, err := handshake(t.Context(), io.Discard, strings.NewReader(string(buf[:])), Options{})
	if err == nil {
		t.Fatal("expected an error for an implausible length")
	}
	if !strings.Contains(err.Error(), "desynchronized") {
		t.Errorf("error = %v, want it to report desynchronization", err)
	}
}

func TestHandshakeRejectsMissingPort(t *testing.T) {
	stdout := strings.NewReader(string(frame(t, pb.OutputConfig_builder{
		ApiKey: proto.String("secret"),
	}.Build())))

	_, err := handshake(t.Context(), io.Discard, stdout, Options{})
	if err == nil || !strings.Contains(err.Error(), "no port") {
		t.Fatalf("error = %v, want a complaint about the missing port", err)
	}
}

// serverPort extracts the port an httptest server is listening on.
func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing %s: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parsing the port from %s: %v", srv.URL, err)
	}
	return port
}

// dialTest connects to an httptest server through the production dial path,
// so the retry and header logic is exercised rather than bypassed.
func dialTest(t *testing.T, srv *httptest.Server) *Conn {
	t.Helper()
	conn, err := dial(t.Context(), int32(serverPort(t, srv)), "test-key")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// echoServer accepts one WebSocket and hands it to fn.
func echoServer(t *testing.T, fn func(context.Context, *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket.Accept: %v", err)
			return
		}
		ws.SetReadLimit(-1)
		fn(r.Context(), ws)
		// Keep reading after fn returns so the server answers the client's
		// close handshake, as a real harness would. Without this every test
		// would pay the client's five-second close timeout.
		for {
			if _, _, err := ws.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestConnSendRecv(t *testing.T) {
	want := pb.OutputEvent_builder{
		SeqNum: proto.Int64(7),
		ToolCall: pb.ToolCall_builder{
			Id:            proto.String("call-1"),
			Name:          proto.String("view_file"),
			ArgumentsJson: proto.String(`{"path":"main.go"}`),
		}.Build(),
	}.Build()

	var serverSaw *pb.InputEvent
	done := make(chan struct{})
	srv := echoServer(t, func(ctx context.Context, ws *websocket.Conn) {
		defer close(done)
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		// Text frames, not binary: the WebSocket half of the protocol is
		// protojson.
		if typ != websocket.MessageText {
			t.Errorf("frame type = %v, want text", typ)
		}
		var ev pb.InputEvent
		if err := protojson.Unmarshal(data, &ev); err != nil {
			t.Errorf("server parsing InputEvent: %v", err)
			return
		}
		serverSaw = &ev

		reply, err := protojson.Marshal(want)
		if err != nil {
			t.Errorf("marshaling reply: %v", err)
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, reply); err != nil {
			t.Errorf("server write: %v", err)
		}
	})

	conn := dialTest(t, srv)

	sent := pb.InputEvent_builder{
		UserInput: pb.UserInput_builder{
			Parts: []*pb.UserInput_Part{
				pb.UserInput_Part_builder{Text: proto.String("hello")}.Build(),
			},
		}.Build(),
	}.Build()
	if err := conn.Send(t.Context(), sent); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, err := conn.Recv(t.Context())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if diff := cmp.Diff(want, got, protocmp.Transform()); diff != "" {
		t.Errorf("received event mismatch (-want +got):\n%s", diff)
	}

	<-done
	if diff := cmp.Diff(sent, serverSaw, protocmp.Transform()); diff != "" {
		t.Errorf("sent event mismatch (-want +got):\n%s", diff)
	}
}

func TestConnRecvReportsHangupAsEOF(t *testing.T) {
	srv := echoServer(t, func(_ context.Context, ws *websocket.Conn) {
		_ = ws.Close(websocket.StatusNormalClosure, "done")
	})

	conn := dialTest(t, srv)
	if _, err := conn.Recv(t.Context()); !errors.Is(err, io.EOF) {
		t.Errorf("Recv error = %v, want io.EOF", err)
	}
}

func TestConnRecvToleratesUnknownFields(t *testing.T) {
	// A harness ahead of the vendored protos must not break the session.
	srv := echoServer(t, func(ctx context.Context, ws *websocket.Conn) {
		_ = ws.Write(ctx, websocket.MessageText,
			[]byte(`{"seqNum":"3","somethingNewFromTheFuture":{"x":1}}`))
	})

	conn := dialTest(t, srv)
	ev, err := conn.Recv(t.Context())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetSeqNum() != 3 {
		t.Errorf("SeqNum = %d, want 3", ev.GetSeqNum())
	}
}

func TestConnCloseIsIdempotent(t *testing.T) {
	srv := echoServer(t, func(ctx context.Context, ws *websocket.Conn) {
		_, _, _ = ws.Read(ctx)
	})

	conn := dialTest(t, srv)
	if err := conn.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestDialSendsAPIKeyHeader(t *testing.T) {
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-goog-api-key")
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_ = ws.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	conn, err := dial(t.Context(), int32(serverPort(t, srv)), "the-key")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if key != "the-key" {
		t.Errorf("x-goog-api-key = %q, want the-key", key)
	}
}

func TestDialGivesUpAndReportsPort(t *testing.T) {
	// Nothing is listening, so every attempt fails. A short deadline keeps the
	// backoff from dominating the test.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, err := dial(ctx, 1, "key")
	if err == nil {
		t.Fatal("expected a dial failure")
	}
	if !strings.Contains(err.Error(), "port 1") {
		t.Errorf("error = %v, want it to name the port", err)
	}
}

func TestMergedEnvOverridesParent(t *testing.T) {
	t.Setenv("ANTIGRAVITY_TEST_VAR", "parent")

	env := mergedEnv(map[string]string{
		"ANTIGRAVITY_TEST_VAR": "child",
		"ANTIGRAVITY_NEW_VAR":  "added",
	})

	if !slices.Contains(env, "ANTIGRAVITY_TEST_VAR=child") {
		t.Error("the override was not applied")
	}
	if slices.Contains(env, "ANTIGRAVITY_TEST_VAR=parent") {
		t.Error("the parent value survived alongside the override")
	}
	if !slices.Contains(env, "ANTIGRAVITY_NEW_VAR=added") {
		t.Error("the added variable is missing")
	}
}

func TestFindBinaryRejectsMissingPath(t *testing.T) {
	_, err := FindBinary(map[string]string{PathEnvVar: "/nonexistent/localharness"})
	if err == nil {
		t.Fatal("expected an error for a path that does not exist")
	}
	if !strings.Contains(err.Error(), PathEnvVar) {
		t.Errorf("error = %v, want it to name %s", err, PathEnvVar)
	}
}

func TestFindBinaryRejectsDirectory(t *testing.T) {
	if _, err := FindBinary(map[string]string{PathEnvVar: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a directory")
	}
}

func TestStartRequiresBinaryPath(t *testing.T) {
	if _, err := Start(t.Context(), Options{}); err == nil {
		t.Fatal("expected an error when BinaryPath is empty")
	}
}

func TestFakeTransportRoundTrip(t *testing.T) {
	f := NewFakeTransport()
	defer f.Close()

	f.Push(pb.OutputEvent_builder{SeqNum: proto.Int64(1)}.Build())
	ev, err := f.Recv(t.Context())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.GetSeqNum() != 1 {
		t.Errorf("SeqNum = %d, want 1", ev.GetSeqNum())
	}

	in := pb.InputEvent_builder{
		UserInput: pb.UserInput_builder{
			Parts: []*pb.UserInput_Part{
				pb.UserInput_Part_builder{Text: proto.String("hi")}.Build(),
			},
		}.Build(),
	}.Build()
	if err := f.Send(t.Context(), in); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := f.Sent(); len(got) != 1 || got[0].GetUserInput().GetParts()[0].GetText() != "hi" {
		t.Errorf("Sent() = %v, want one message saying hi", got)
	}
}

func TestFakeTransportCloseEndsRecv(t *testing.T) {
	f := NewFakeTransport()
	f.Close()
	if _, err := f.Recv(t.Context()); !errors.Is(err, io.EOF) {
		t.Errorf("Recv error = %v, want io.EOF", err)
	}
}

func TestFakeTransportDrainsBeforeEOF(t *testing.T) {
	// Events pushed before the close must still be delivered, so a test can
	// script a whole turn and close in one breath.
	f := NewFakeTransport()
	f.Push(pb.OutputEvent_builder{SeqNum: proto.Int64(1)}.Build())
	f.Close()

	if _, err := f.Recv(t.Context()); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if _, err := f.Recv(t.Context()); !errors.Is(err, io.EOF) {
		t.Errorf("second Recv = %v, want io.EOF", err)
	}
}

// ---------------------------------------------------------------------------
// The subprocess
// ---------------------------------------------------------------------------

// The localharness binary is not available here, so these tests re-execute the
// test binary itself as a stand-in that speaks the same protocol: a framed
// stdio handshake followed by a WebSocket carrying protojson.

const (
	// helperEnv selects the stand-in harness's behavior. It is unset in the
	// parent, which is what keeps the helper test itself inert.
	helperEnv = "ANTIGRAVITY_TEST_HARNESS_MODE"
	// helperServe completes the handshake and answers on the WebSocket.
	helperServe = "serve"
	// helperCrash dies before the handshake, as a misconfigured harness does.
	helperCrash = "crash"
	// helperHangUp completes the handshake and then drops the WebSocket.
	helperHangUp = "hangup"
)

// TestMain turns this binary into the stand-in harness when the mode variable
// is set, which is how a re-executed copy of it plays the other side of the
// protocol. The parent never sets the variable, so it runs the tests normally.
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		standInHarness(mode)
	}
	os.Exit(m.Run())
}

// standInHarness plays the harness side of the protocol and exits when the
// session ends. It never returns.
func standInHarness(mode string) {
	fmt.Fprintln(os.Stderr, "stand-in harness starting")

	if mode == helperCrash {
		fmt.Fprintln(os.Stderr, "cannot bind a port")
		os.Exit(1)
	}

	if err := readInputConfig(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "reading the InputConfig: %v\n", err)
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listening: %v\n", err)
		os.Exit(1)
	}

	served := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ws.SetReadLimit(-1)
		if mode == helperHangUp {
			_ = ws.Close(websocket.StatusGoingAway, "going away")
			return
		}
		serveStandInSession(r.Context(), ws)
	})}
	go func() { _ = srv.Serve(ln) }()

	port := int32(ln.Addr().(*net.TCPAddr).Port)
	if _, err := os.Stdout.Write(frameMessage(pb.OutputConfig_builder{
		Port:   proto.Int32(port),
		ApiKey: proto.String("stand-in-key"),
	}.Build())); err != nil {
		os.Exit(1)
	}

	// Exiting with the session is what lets the parent's Close return without
	// waiting out its shutdown grace.
	<-served
	os.Exit(0)
}

// serveStandInSession answers the initialize handshake and then echoes.
func serveStandInSession(ctx context.Context, ws *websocket.Conn) {
	for i := 0; ; i++ {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}

		var reply *pb.OutputEvent
		if i == 0 {
			reply = pb.OutputEvent_builder{
				InitializeConversationResponse: pb.InitializeConversationResponse_builder{
					CascadeId: proto.String("conv-stand-in"),
				}.Build(),
			}.Build()
		} else {
			var ev pb.InputEvent
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &ev); err != nil {
				return
			}
			reply = pb.OutputEvent_builder{
				SeqNum: proto.Int64(int64(i)),
				StepUpdate: pb.StepUpdate_builder{
					TrajectoryId: proto.String("main"),
					Text:         proto.String(ev.GetUserInput().GetParts()[0].GetText()),
				}.Build(),
			}.Build()
		}

		out, err := protojson.Marshal(reply)
		if err != nil {
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}

// readInputConfig consumes the framed InputConfig the SDK writes at startup.
func readInputConfig(r io.Reader) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return err
	}
	body := make([]byte, binary.LittleEndian.Uint32(lenBuf[:]))
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	var cfg pb.InputConfig
	return proto.Unmarshal(body, &cfg)
}

// frameMessage length-prefixes a message for the stdio protocol.
func frameMessage(m proto.Message) []byte {
	body, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out
}

// startStandIn launches this test binary again, in the stand-in harness mode.
func startStandIn(t *testing.T, mode string) (*Process, error) {
	t.Helper()

	return Start(t.Context(), Options{
		BinaryPath:       os.Args[0],
		StorageDirectory: t.TempDir(),
		Env:              map[string]string{helperEnv: mode},
	})
}

func TestStartTalksToAHarness(t *testing.T) {
	p, err := startStandIn(t, helperServe)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	resp, err := p.Initialize(t.Context(), pb.HarnessConfig_builder{
		CascadeId: proto.String("conv-1"),
	}.Build())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if resp.GetCascadeId() != "conv-stand-in" {
		t.Errorf("CascadeId = %q, want the harness's own", resp.GetCascadeId())
	}

	send := pb.InputEvent_builder{
		UserInput: pb.UserInput_builder{
			Parts: []*pb.UserInput_Part{pb.UserInput_Part_builder{Text: proto.String("hello")}.Build()},
		}.Build(),
	}.Build()
	if err := p.Send(t.Context(), send); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := p.Recv(t.Context())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got.GetStepUpdate().GetText() != "hello" {
		t.Errorf("Recv = %v, want the echoed prompt", got)
	}

	// Whatever the harness printed is kept for diagnostics, even on success.
	if !strings.Contains(p.Stderr(), "stand-in harness starting") {
		t.Errorf("Stderr = %q, want the harness output captured", p.Stderr())
	}

	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Closing twice must not wait on an already-reaped subprocess.
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestStartReportsACrashWithStderr(t *testing.T) {
	_, err := startStandIn(t, helperCrash)
	if err == nil {
		t.Fatal("Start succeeded against a harness that died")
	}

	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("error = %T (%v), want *StartError", err, err)
	}
	// Stderr is the only explanation a dead harness leaves, so it has to reach
	// the caller.
	if !strings.Contains(startErr.Error(), "cannot bind a port") {
		t.Errorf("error = %v, want the harness stderr attached", startErr)
	}
	if startErr.Unwrap() == nil {
		t.Error("the underlying failure is not reachable")
	}
}

func TestProcessInitializeReportsAHangup(t *testing.T) {
	p, err := startStandIn(t, helperHangUp)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()

	// The socket is gone before the config is answered, which is fatal: there
	// is no session to fall back on.
	_, err = p.Initialize(t.Context(), pb.HarnessConfig_builder{}.Build())
	if err == nil {
		t.Fatal("Initialize succeeded against a harness that hung up")
	}
	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Errorf("error = %T (%v), want *StartError", err, err)
	}
}

func TestStartErrorWithoutStderr(t *testing.T) {
	err := &StartError{Err: errors.New("dial failed")}
	if got := err.Error(); got != "dial failed" {
		t.Errorf("Error = %q, want just the cause", got)
	}
}

func TestFindBinaryResolutionOrder(t *testing.T) {
	// A binary on PATH, and a different one named explicitly, so the order the
	// two are consulted in is observable.
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, binaryName())
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	explicit := filepath.Join(t.TempDir(), binaryName())
	if err := os.WriteFile(explicit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	t.Setenv("PATH", pathDir)
	t.Setenv(PathEnvVar, "")

	got, err := FindBinary(nil)
	if err != nil {
		t.Fatalf("FindBinary: %v", err)
	}
	if got != onPath {
		t.Errorf("FindBinary = %q, want the binary on PATH", got)
	}

	// The environment outranks PATH.
	t.Setenv(PathEnvVar, explicit)
	if got, err := FindBinary(nil); err != nil || got != explicit {
		t.Errorf("FindBinary = %q, %v, want the path from %s", got, err, PathEnvVar)
	}

	// And the caller's own configuration outranks the environment.
	if got, err := FindBinary(map[string]string{PathEnvVar: onPath}); err != nil || got != onPath {
		t.Errorf("FindBinary = %q, %v, want the configured path", got, err)
	}
}

func TestFindBinaryReportsAnEmptyPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(PathEnvVar, "")

	_, err := FindBinary(nil)
	if err == nil {
		t.Fatal("FindBinary succeeded with nothing to find")
	}
	// The message has to say what to do about it, since this is the first thing
	// a new user hits.
	if !strings.Contains(err.Error(), PathEnvVar) || !strings.Contains(err.Error(), binaryName()) {
		t.Errorf("error = %v, want it to name the binary and the variable", err)
	}
}

func TestConnStderrIsEmpty(t *testing.T) {
	// Only a subprocess has stderr to report; a bare WebSocket has none, and
	// the Transport interface still has to be satisfiable.
	srv := echoServer(t, func(ctx context.Context, ws *websocket.Conn) { _, _, _ = ws.Read(ctx) })
	if got := dialTest(t, srv).Stderr(); got != "" {
		t.Errorf("Stderr = %q, want nothing", got)
	}
}
