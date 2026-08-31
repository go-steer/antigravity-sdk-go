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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMediaConstructors(t *testing.T) {
	tests := []struct {
		name string
		make func(mimeType string) (Media, error)
		ok   string
		bad  string
		kind MediaKind
	}{
		{
			name: "image",
			make: func(m string) (Media, error) { return NewImage([]byte{1}, m, "d") },
			ok:   "image/png",
			bad:  "image/gif",
			kind: KindImage,
		},
		{
			name: "document",
			make: func(m string) (Media, error) { return NewDocument([]byte{1}, m, "d") },
			ok:   "application/pdf",
			bad:  "application/zip",
			kind: KindDocument,
		},
		{
			name: "audio",
			make: func(m string) (Media, error) { return NewAudio([]byte{1}, m, "d") },
			ok:   "audio/wav",
			bad:  "audio/midi",
			kind: KindAudio,
		},
		{
			name: "video",
			make: func(m string) (Media, error) { return NewVideo([]byte{1}, m, "d") },
			ok:   "video/mp4",
			bad:  "video/ogg",
			kind: KindVideo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.make(tt.ok)
			if err != nil {
				t.Fatalf("constructing %s: %v", tt.name, err)
			}
			if got.Kind() != tt.kind {
				t.Errorf("Kind = %q, want %q", got.Kind(), tt.kind)
			}
			if got.MIME() != tt.ok || got.Describe() != "d" || len(got.Bytes()) != 1 {
				t.Errorf("attachment = %+v", got)
			}

			// An unsupported type is rejected at construction, where the caller
			// can still do something about it.
			if _, err := tt.make(tt.bad); err == nil {
				t.Errorf("%q was accepted as %s", tt.bad, tt.name)
			} else if !strings.Contains(err.Error(), tt.ok) {
				t.Errorf("error = %v, want it to list the supported types", err)
			}
		})
	}
}

func TestFromBytes(t *testing.T) {
	tests := []struct {
		mimeType string
		want     MediaKind
	}{
		{"image/webp", KindImage},
		{"text/csv", KindDocument},
		{"audio/opus", KindAudio},
		{"video/webm", KindVideo},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			got, err := FromBytes([]byte("x"), tt.mimeType, "")
			if err != nil {
				t.Fatalf("FromBytes: %v", err)
			}
			m, ok := got.(Media)
			if !ok {
				t.Fatalf("FromBytes returned %T, want a Media", got)
			}
			if m.Kind() != tt.want {
				t.Errorf("Kind = %q, want %q", m.Kind(), tt.want)
			}
		})
	}
}

func TestFromBytesRejectsAnUnsupportedType(t *testing.T) {
	_, err := FromBytes([]byte("x"), "application/x-tar", "")
	if err == nil {
		t.Fatal("an unsupported MIME type was accepted")
	}
	// The message lists what is supported, sorted, so it reads the same twice.
	if !strings.Contains(err.Error(), "application/json") {
		t.Errorf("error = %v, want it to list the supported types", err)
	}
}

func TestFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	got, err := FromFile(path, "some notes")
	if err != nil {
		t.Fatalf("FromFile: %v", err)
	}
	doc, ok := got.(*Document)
	if !ok {
		t.Fatalf("FromFile returned %T, want *Document", got)
	}
	// TypeByExtension reports "text/plain; charset=utf-8"; the parameters must
	// be stripped before the type is looked up.
	if doc.MIME() != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", doc.MIME())
	}
	if string(doc.Bytes()) != "hello" || doc.Describe() != "some notes" {
		t.Errorf("document = %+v", doc)
	}
}

func TestFromFileErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := FromFile(filepath.Join(dir, "absent.txt"), ""); err == nil {
		t.Error("FromFile succeeded on a file that does not exist")
	}

	// An unrecognized extension is an error rather than a silent fallback to
	// some default type.
	path := filepath.Join(dir, "data.unknownext")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	if _, err := FromFile(path, ""); err == nil {
		t.Error("FromFile inferred a type for an unrecognized extension")
	}
}

func TestPrompt(t *testing.T) {
	got := Prompt("first", "second")
	if len(got) != 2 {
		t.Fatalf("got %d parts, want 2", len(got))
	}
	if got[0] != Text("first") || got[1] != Text("second") {
		t.Errorf("Prompt = %v", got)
	}
	if len(Prompt()) != 0 {
		t.Error("Prompt() returned parts for no arguments")
	}
}

func TestValidatePrompt(t *testing.T) {
	image, err := NewImage([]byte{1}, "image/png", "")
	if err != nil {
		t.Fatalf("NewImage: %v", err)
	}

	tests := []struct {
		name   string
		prompt []Content
		wantOK bool
	}{
		{"text", []Content{Text("hello")}, true},
		{"media alone", []Content{image}, true},
		{"a slash command alone", []Content{SlashCommand{Name: SlashPlan}}, true},
		{"blank text beside media", []Content{Text("  "), image}, true},
		{"empty", nil, false},
		{"empty text", []Content{Text("")}, false},
		{"whitespace only", []Content{Text(" \n\t")}, false},
		{"a nil element", []Content{Text("hello"), nil}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePrompt(tt.prompt)
			if tt.wantOK && err != nil {
				t.Fatalf("validatePrompt: %v", err)
			}
			if !tt.wantOK && !errors.Is(err, ErrInvalidPrompt) {
				t.Fatalf("error = %v, want ErrInvalidPrompt", err)
			}
		})
	}
}

func TestSupportedMIMEsIsSorted(t *testing.T) {
	got := supportedMIMEs()
	if len(got) == 0 {
		t.Fatal("no MIME types are supported")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("supportedMIMEs is unsorted at %d: %q before %q", i, got[i-1], got[i])
		}
	}
}
