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
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// A Trigger is a long-lived function that runs alongside an agent session and
// pushes messages into it when something happens outside the conversation: a
// timer fires, a file changes, a webhook arrives.
//
// Triggers start when the session does and are cancelled when it closes. A
// trigger should run until its context ends and return ctx.Err() when it does;
// any other error is logged, and the trigger is not restarted.
//
// Register them with [WithTriggers], and use [Every] for the common case:
//
//	antigravity.WithTriggers(
//		antigravity.Every(time.Hour, func(ctx context.Context, tc *TriggerContext) error {
//			return tc.Send(ctx, "An hour has passed; summarize what changed.")
//		}),
//	)
type Trigger func(ctx context.Context, tc *TriggerContext) error

// TriggerContext is a trigger's handle on the session. Each trigger gets its
// own.
type TriggerContext struct {
	name string
	conv *Conversation
}

// Name is the trigger's label, used in log messages.
func (t *TriggerContext) Name() string { return t.name }

// Send pushes a message into the agent, starting a turn if it is idle.
//
// The message reaches the agent as an external notification rather than as a
// user prompt. Nothing here consumes the resulting turn: whoever is reading
// the conversation sees it, and if nobody is, it still runs.
func (t *TriggerContext) Send(ctx context.Context, message string) error {
	return t.conv.Trigger(ctx, message)
}

// Conversation gives a trigger access to the full session, for the rarer cases
// where sending a message is not enough — inspecting history, or waiting for
// the agent to go idle before firing again.
func (t *TriggerContext) Conversation() *Conversation { return t.conv }

// namedTrigger pairs a trigger with the label its context reports.
type namedTrigger struct {
	name string
	fn   Trigger
}

// WithTriggers registers triggers to run for the life of the session.
//
// Each runs on its own goroutine with no ordering guarantees between them.
func WithTriggers(triggers ...Trigger) Option {
	return func(c *config) {
		for _, t := range triggers {
			if t == nil {
				continue
			}
			c.triggers = append(c.triggers, namedTrigger{
				name: fmt.Sprintf("trigger-%d", len(c.triggers)),
				fn:   t,
			})
		}
	}
}

// WithNamedTrigger registers one trigger under a name of your choosing, which
// makes its log output easier to follow than the positional default.
func WithNamedTrigger(name string, t Trigger) Option {
	return func(c *config) {
		if t != nil {
			c.triggers = append(c.triggers, namedTrigger{name: name, fn: t})
		}
	}
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// triggerRunner owns the goroutines running a session's triggers.
type triggerRunner struct {
	logger *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

// startTriggers launches every registered trigger, returning nil when there
// are none so the caller has nothing to stop.
//
// The triggers share a context derived from the session's, so closing the
// agent cancels them all at once.
func startTriggers(ctx context.Context, triggers []namedTrigger, conv *Conversation, logger *slog.Logger) *triggerRunner {
	if len(triggers) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &triggerRunner{logger: logger, cancel: cancel}

	for _, t := range triggers {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.run(ctx, t, conv)
		}()
	}
	return r
}

// run executes one trigger, containing its failures.
//
// A panic in a trigger is recovered: an external event handler misbehaving
// should not take the agent's session down with it.
func (r *triggerRunner) run(ctx context.Context, t namedTrigger, conv *Conversation) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("a trigger panicked", "trigger", t.name, "panic", rec)
		}
	}()

	err := t.fn(ctx, &TriggerContext{name: t.name, conv: conv})
	switch {
	case err == nil:
		r.logger.Debug("a trigger finished", "trigger", t.name)
	case ctx.Err() != nil:
		r.logger.Debug("a trigger stopped with the session", "trigger", t.name)
	default:
		r.logger.Error("a trigger failed", "trigger", t.name, "error", err)
	}
}

// stop cancels every trigger and waits for it to return. It is safe to call on
// a nil runner and more than once.
func (r *triggerRunner) stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.cancel()
		r.wg.Wait()
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// Every returns a trigger that calls fn on a fixed interval.
//
// The first call happens after one interval, not immediately. A call that
// returns an error stops the trigger, so return nil to keep going.
func Every(interval time.Duration, fn func(ctx context.Context, tc *TriggerContext) error) Trigger {
	return func(ctx context.Context, tc *TriggerContext) error {
		if interval <= 0 {
			return fmt.Errorf("%w: the trigger interval must be positive, got %s", ErrInvalidConfig, interval)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				if err := fn(ctx, tc); err != nil {
					return err
				}
			}
		}
	}
}

// After returns a trigger that calls fn once, after the given delay.
func After(delay time.Duration, fn func(ctx context.Context, tc *TriggerContext) error) Trigger {
	return func(ctx context.Context, tc *TriggerContext) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fn(ctx, tc)
		}
	}
}

// FileChangeKind is the sort of filesystem change [OnFileChange] observed.
type FileChangeKind string

const (
	FileAdded    FileChangeKind = "added"
	FileModified FileChangeKind = "modified"
	FileDeleted  FileChangeKind = "deleted"
)

// FileChange is one observed filesystem change.
type FileChange struct {
	// Kind is what happened to the file.
	Kind FileChangeKind
	// Path is the absolute path of the file.
	Path string
}

// OnFileChange returns a trigger that calls fn when files under root change.
//
// It polls rather than using OS notifications, which keeps the SDK free of
// non-standard-library dependencies at the cost of latency bounded by
// interval. Changes are batched: one call to fn reports everything seen in a
// single sweep, and a sweep that finds nothing does not call fn at all.
//
// The first sweep establishes the baseline, so files already present when the
// trigger starts are not reported as additions — only what changes afterwards
// is. A change is anything that alters a file's size or modification time.
//
// Directories are walked recursively. A path that cannot be read is skipped
// silently, since a transient permission or race error should not stop the
// watch.
func OnFileChange(root string, interval time.Duration, fn func(ctx context.Context, tc *TriggerContext, changes []FileChange) error) Trigger {
	return func(ctx context.Context, tc *TriggerContext) error {
		if interval <= 0 {
			return fmt.Errorf("%w: the watch interval must be positive, got %s", ErrInvalidConfig, interval)
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return fmt.Errorf("watching %q: %w", root, err)
		}

		previous := snapshot(abs)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}

			current := snapshot(abs)
			if changes := diffSnapshots(previous, current); len(changes) > 0 {
				previous = current
				if err := fn(ctx, tc, changes); err != nil {
					return err
				}
				continue
			}
			previous = current
		}
	}
}

// snapshot records the modification time and size of every file under root,
// which together detect any edit a poll-based watcher can see.
func snapshot(root string) map[string]fileStamp {
	out := map[string]fileStamp{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable subtrees are skipped, not fatal.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out[path] = fileStamp{modTime: info.ModTime(), size: info.Size()}
		return nil
	})
	return out
}

// fileStamp is the observable state of a file for change detection.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// diffSnapshots reports what changed between two sweeps, in path order so the
// result is deterministic.
func diffSnapshots(before, after map[string]fileStamp) []FileChange {
	var changes []FileChange
	for path, now := range after {
		was, existed := before[path]
		switch {
		case !existed:
			changes = append(changes, FileChange{Kind: FileAdded, Path: path})
		case was != now:
			changes = append(changes, FileChange{Kind: FileModified, Path: path})
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changes = append(changes, FileChange{Kind: FileDeleted, Path: path})
		}
	}
	slices.SortFunc(changes, func(a, b FileChange) int {
		return strings.Compare(a.Path, b.Path)
	})
	return changes
}
