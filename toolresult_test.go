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
	"encoding/json"
	"testing"
)

// testImage builds an attachment for the media-splitting tests.
func testImage(t *testing.T, description string) *Image {
	t.Helper()

	img, err := NewImage([]byte{1}, "image/png", description)
	if err != nil {
		t.Fatalf("NewImage: %v", err)
	}
	return img
}

func TestSplitMediaLeavesOrdinaryValuesAlone(t *testing.T) {
	type report struct {
		Title string `json:"title"`
	}

	for _, v := range []any{
		"plain text",
		42,
		report{Title: "quarterly"},
		[]string{"a", "b"},
		map[string]int{"a": 1},
	} {
		cleaned, media := splitMedia(v)
		if len(media) != 0 {
			t.Errorf("splitMedia(%#v) found media where there is none", v)
		}
		// The caller's own type survives, so its JSON tags still apply.
		if cleaned == nil {
			t.Errorf("splitMedia(%#v) discarded the value", v)
		}
	}
}

func TestSplitMediaOnNothing(t *testing.T) {
	cleaned, media := splitMedia(nil)
	if cleaned != nil || media != nil {
		t.Errorf("splitMedia(nil) = %v, %v", cleaned, media)
	}
}

func TestSplitMediaOnAnAttachmentAlone(t *testing.T) {
	img := testImage(t, "a chart")

	cleaned, media := splitMedia(img)
	if cleaned != nil {
		t.Errorf("cleaned = %v, want nothing left over", cleaned)
	}
	if len(media) != 1 || media[0].Describe() != "a chart" {
		t.Fatalf("media = %v, want the attachment", media)
	}
}

func TestSplitMediaFromASlice(t *testing.T) {
	cleaned, media := splitMedia([]any{"before", testImage(t, "chart"), "after"})

	if len(media) != 1 {
		t.Fatalf("got %d attachments, want 1", len(media))
	}
	kept, ok := cleaned.([]any)
	if !ok {
		t.Fatalf("cleaned = %T, want a slice", cleaned)
	}
	if len(kept) != 2 || kept[0] != "before" || kept[1] != "after" {
		t.Errorf("cleaned = %v, want the non-media elements in order", kept)
	}
}

func TestSplitMediaFromASliceOfNothingElse(t *testing.T) {
	cleaned, media := splitMedia([]any{testImage(t, "one"), testImage(t, "two")})

	if len(media) != 2 {
		t.Fatalf("got %d attachments, want 2", len(media))
	}
	if cleaned != nil {
		t.Errorf("cleaned = %v, want nothing left over", cleaned)
	}
}

func TestSplitMediaFromAMap(t *testing.T) {
	cleaned, media := splitMedia(map[string]any{
		"summary": "all good",
		"chart":   testImage(t, "chart"),
	})

	if len(media) != 1 {
		t.Fatalf("got %d attachments, want 1", len(media))
	}
	kept, ok := cleaned.(map[string]any)
	if !ok {
		t.Fatalf("cleaned = %T, want a map", cleaned)
	}
	if len(kept) != 1 || kept["summary"] != "all good" {
		t.Errorf("cleaned = %v, want only the non-media entry", kept)
	}
}

func TestSplitMediaFromAStruct(t *testing.T) {
	type result struct {
		Summary string `json:"summary"`
		Chart   *Image `json:"chart"`
		Skipped string `json:"-"`
		Plain   int
		hidden  string //nolint:unused // present to prove unexported fields are skipped
	}

	cleaned, media := splitMedia(result{
		Summary: "all good",
		Chart:   testImage(t, "chart"),
		Skipped: "still kept",
		Plain:   7,
		hidden:  "invisible",
	})

	if len(media) != 1 {
		t.Fatalf("got %d attachments, want 1", len(media))
	}
	kept, ok := cleaned.(map[string]any)
	if !ok {
		t.Fatalf("cleaned = %T, want a map rebuilt from the struct", cleaned)
	}
	// The rebuilt map has to preserve the field names the model would have
	// seen, which come from the json tags.
	if kept["summary"] != "all good" {
		t.Errorf("summary = %v, want the json tag's name to be used", kept)
	}
	// A "-" tag has no usable name, so the field name stands in.
	if kept["Skipped"] != "still kept" {
		t.Errorf("cleaned = %v, want the untagged field under its Go name", kept)
	}
	if kept["Plain"] != 7 {
		t.Errorf("cleaned = %v, want the untagged field kept", kept)
	}
	if _, ok := kept["hidden"]; ok {
		t.Error("an unexported field leaked into the result")
	}
}

func TestSplitMediaFromANestedStruct(t *testing.T) {
	type inner struct {
		Chart *Image `json:"chart"`
	}
	type outer struct {
		Label string `json:"label"`
		Inner inner  `json:"inner"`
	}

	cleaned, media := splitMedia(outer{Label: "x", Inner: inner{Chart: testImage(t, "deep")}})
	if len(media) != 1 || media[0].Describe() != "deep" {
		t.Fatalf("media = %v, want the nested attachment", media)
	}
	kept, ok := cleaned.(map[string]any)
	if !ok {
		t.Fatalf("cleaned = %T, want a map", cleaned)
	}
	if kept["label"] != "x" {
		t.Errorf("cleaned = %v, want the sibling field kept", kept)
	}
	// The nested struct held nothing but media, so it collapses away.
	if _, ok := kept["inner"]; ok {
		t.Errorf("cleaned = %v, want the emptied nested value dropped", kept)
	}
}

func TestSplitMediaIgnoresANilAttachment(t *testing.T) {
	type result struct {
		Summary string `json:"summary"`
		Chart   *Image `json:"chart"`
	}

	cleaned, media := splitMedia(result{Summary: "no chart this time"})
	if len(media) != 0 {
		t.Errorf("media = %v, want none: the attachment was nil", media)
	}
	// With no media anywhere the struct is handed back untouched, tags and all.
	if _, ok := cleaned.(result); !ok {
		t.Errorf("cleaned = %T, want the original struct", cleaned)
	}
}

func TestSplitMediaThroughAnInterface(t *testing.T) {
	var value any = testImage(t, "boxed")
	cleaned, media := splitMedia([]any{value})

	if len(media) != 1 || media[0].Describe() != "boxed" {
		t.Errorf("media = %v, want the attachment found through the interface", media)
	}
	if cleaned != nil {
		t.Errorf("cleaned = %v, want nothing left over", cleaned)
	}
}

func TestEncodeToolResult(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"nothing", nil, `{}`},
		{"an object passes through", map[string]int{"a": 1}, `{"a":1}`},
		// The harness merges the result into an envelope, so a bare value has
		// to be wrapped in an object first.
		{"a string is wrapped", "hello", `{"result":"hello"}`},
		{"a number is wrapped", 42, `{"result":42}`},
		{"a list is wrapped", []int{1, 2}, `{"result":[1,2]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := encodeToolResult(tt.value)
			if err != nil {
				t.Fatalf("encodeToolResult: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("encodeToolResult = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEncodeToolResultFallsBackToText(t *testing.T) {
	// A value JSON cannot represent still says something useful, rather than
	// failing the whole call.
	got, err := encodeToolResult(map[string]any{"fn": func() {}})
	if err != nil {
		t.Fatalf("encodeToolResult: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("the fallback is not an object: %s", got)
	}
	if decoded["result"] == "" {
		t.Errorf("fallback = %s, want the value's text form", got)
	}
}
