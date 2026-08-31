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
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// runTrigger drives one trigger to completion with a context the test can
// cancel, returning whatever error it stopped with.
func runTrigger(t *testing.T, ctx context.Context, tr Trigger) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- tr(ctx, &TriggerContext{name: "test"}) }()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the trigger did not return within 5s")
		return nil
	}
}

func TestEveryFiresRepeatedly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var calls atomic.Int32
	fired := make(chan struct{}, 8)
	tr := Every(time.Millisecond, func(context.Context, *TriggerContext) error {
		calls.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	})

	go func() {
		// Stop once we have seen it fire more than once, which is what
		// distinguishes a repeating trigger from a one-shot.
		<-fired
		<-fired
		cancel()
	}()

	if err := runTrigger(t, ctx, tr); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got < 2 {
		t.Errorf("calls = %d, want at least 2", got)
	}
}

func TestEveryDoesNotFireImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	var calls atomic.Int32
	tr := Every(time.Hour, func(context.Context, *TriggerContext) error {
		calls.Add(1)
		return nil
	})

	// Cancelling before the first interval elapses must produce no call at all.
	cancel()
	if err := runTrigger(t, ctx, tr); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("calls = %d, want 0: the first call must wait one interval", got)
	}
}

func TestEveryStopsOnCallbackError(t *testing.T) {
	want := errors.New("boom")
	tr := Every(time.Millisecond, func(context.Context, *TriggerContext) error { return want })

	if err := runTrigger(t, t.Context(), tr); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestEveryRejectsNonPositiveInterval(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		tr := Every(d, func(context.Context, *TriggerContext) error { return nil })
		err := runTrigger(t, t.Context(), tr)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("interval %s: err = %v, want ErrInvalidConfig", d, err)
		}
	}
}

func TestAfterFiresOnce(t *testing.T) {
	var calls atomic.Int32
	tr := After(time.Millisecond, func(context.Context, *TriggerContext) error {
		calls.Add(1)
		return nil
	})

	if err := runTrigger(t, t.Context(), tr); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestAfterCancelledBeforeFiring(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var calls atomic.Int32
	tr := After(time.Hour, func(context.Context, *TriggerContext) error {
		calls.Add(1)
		return nil
	})

	if err := runTrigger(t, ctx, tr); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("calls = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// File watching
// ---------------------------------------------------------------------------

func TestDiffSnapshots(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t1 := time.Unix(2000, 0)

	before := map[string]fileStamp{
		"/a": {modTime: t0, size: 1},
		"/b": {modTime: t0, size: 1},
		"/c": {modTime: t0, size: 1},
	}
	after := map[string]fileStamp{
		"/a": {modTime: t0, size: 1}, // unchanged
		"/b": {modTime: t1, size: 1}, // touched
		"/d": {modTime: t0, size: 9}, // added; /c deleted
	}

	got := diffSnapshots(before, after)
	want := []FileChange{
		{Kind: FileModified, Path: "/b"},
		{Kind: FileDeleted, Path: "/c"},
		{Kind: FileAdded, Path: "/d"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("change %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestDiffSnapshotsDetectsSizeOnlyChange(t *testing.T) {
	// A write fast enough to land in the same filesystem timestamp is still a
	// change when the length differs.
	at := time.Unix(1000, 0)
	got := diffSnapshots(
		map[string]fileStamp{"/a": {modTime: at, size: 1}},
		map[string]fileStamp{"/a": {modTime: at, size: 2}},
	)
	if len(got) != 1 || got[0].Kind != FileModified {
		t.Errorf("got %v, want one modification", got)
	}
}

func TestSnapshotWalksRecursivelyAndSkipsDirs(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(nested, "f.txt")
	if err := os.WriteFile(leaf, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := snapshot(root)
	if len(got) != 1 {
		t.Fatalf("snapshot has %d entries, want 1 (directories must not be recorded): %v", len(got), got)
	}
	if stamp, ok := got[leaf]; !ok {
		t.Errorf("snapshot is missing %s", leaf)
	} else if stamp.size != 5 {
		t.Errorf("size = %d, want 5", stamp.size)
	}
}

func TestSnapshotOfMissingRootIsEmpty(t *testing.T) {
	if got := snapshot(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Errorf("got %v, want an empty snapshot", got)
	}
}

func TestOnFileChangeReportsAWrite(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	changes := make(chan []FileChange, 1)
	tr := OnFileChange(root, time.Millisecond, func(_ context.Context, _ *TriggerContext, c []FileChange) error {
		select {
		case changes <- c:
		default:
		}
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- tr(ctx, &TriggerContext{name: "watch"}) }()

	// The baseline sweep happens on the watcher's goroutine, so a single write
	// could land before it and be absorbed into the baseline. Growing the file
	// until the watcher notices removes that race: whichever sweep is first,
	// the next write differs from it.
	written := filepath.Join(root, "new.txt")
	go func() {
		for i := 0; ctx.Err() == nil; i++ {
			os.WriteFile(written, make([]byte, i+1), 0o644)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	select {
	case got := <-changes:
		if len(got) != 1 || got[0].Path != written {
			t.Fatalf("got %v, want a single change to %s", got, written)
		}
		if k := got[0].Kind; k != FileAdded && k != FileModified {
			t.Errorf("Kind = %q, want added or modified", k)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not report the write within 5s")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestOnFileChangeRejectsNonPositiveInterval(t *testing.T) {
	tr := OnFileChange(t.TempDir(), 0, func(context.Context, *TriggerContext, []FileChange) error { return nil })
	if err := runTrigger(t, t.Context(), tr); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("err = %v, want ErrInvalidConfig", err)
	}
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

func TestStartTriggersReturnsNilWhenEmpty(t *testing.T) {
	if r := startTriggers(t.Context(), nil, nil, discardLogger()); r != nil {
		t.Error("startTriggers returned a runner for an empty set, want nil")
	}
	// A nil runner must still be safe to stop, twice.
	var r *triggerRunner
	r.stop()
	r.stop()
}

func TestRunnerStopCancelsEveryTrigger(t *testing.T) {
	var running atomic.Int32
	started := make(chan struct{}, 3)

	block := func(ctx context.Context, _ *TriggerContext) error {
		running.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		running.Add(-1)
		return ctx.Err()
	}

	triggers := []namedTrigger{
		{name: "a", fn: block},
		{name: "b", fn: block},
		{name: "c", fn: block},
	}
	r := startTriggers(t.Context(), triggers, nil, discardLogger())
	for range triggers {
		<-started
	}

	r.stop()
	if got := running.Load(); got != 0 {
		t.Errorf("%d triggers still running after stop, want 0", got)
	}
	// stop must be idempotent: a second call cannot re-close or re-wait.
	r.stop()
}

func TestRunnerContainsAPanic(t *testing.T) {
	done := make(chan struct{})
	triggers := []namedTrigger{
		{name: "boom", fn: func(context.Context, *TriggerContext) error { panic("kaboom") }},
		{name: "fine", fn: func(context.Context, *TriggerContext) error { close(done); return nil }},
	}

	r := startTriggers(t.Context(), triggers, nil, discardLogger())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the surviving trigger never ran")
	}
	// If the panic escaped, the wait below would never be reached at all.
	r.stop()
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

func TestWithTriggersNamesPositionally(t *testing.T) {
	noop := func(context.Context, *TriggerContext) error { return nil }

	c := newConfig()
	WithTriggers(noop, nil, noop)(c)
	WithNamedTrigger("nightly", noop)(c)
	WithNamedTrigger("ignored", nil)(c)

	want := []string{"trigger-0", "trigger-1", "nightly"}
	if len(c.triggers) != len(want) {
		t.Fatalf("got %d triggers, want %d", len(c.triggers), len(want))
	}
	for i, name := range want {
		if c.triggers[i].name != name {
			t.Errorf("trigger %d is named %q, want %q", i, c.triggers[i].name, name)
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestTriggerContext(t *testing.T) {
	s := newSession(t)
	tc := &TriggerContext{name: "hourly", conv: s.Conversation}

	if tc.Name() != "hourly" {
		t.Errorf("Name = %q, want the trigger's label", tc.Name())
	}
	if tc.Conversation() != s.Conversation {
		t.Error("Conversation is not the session the trigger fires into")
	}

	if err := tc.Send(t.Context(), "an hour has passed"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// A trigger's message is an external notification, not a user prompt, so
	// it must not arrive as one.
	ev := waitSent(t, s.fake)
	if ev.GetUserInput() != nil {
		t.Errorf("the trigger sent %v, want an external notification", ev)
	}
}
