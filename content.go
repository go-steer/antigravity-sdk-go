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
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Content is one piece of a prompt. A prompt is a slice of Content values,
// letting text and media be interleaved in a single turn.
//
// The implementations are [Text], [Image], [Document], [Audio], [Video], and
// [SlashCommand].
type Content interface {
	isContent()
}

// Text is a plain text prompt fragment.
type Text string

func (Text) isContent() {}

// MediaKind classifies an attachment.
type MediaKind string

const (
	KindImage    MediaKind = "image"
	KindDocument MediaKind = "document"
	KindAudio    MediaKind = "audio"
	KindVideo    MediaKind = "video"
)

// Supported MIME types, grouped by the media kind that accepts them.
var (
	supportedImageMIMEs = []string{
		"image/bmp",
		"image/jpeg",
		"image/png",
		"image/webp",
	}
	supportedDocumentMIMEs = []string{
		"application/json",
		"application/pdf",
		"text/css",
		"text/csv",
		"text/html",
		"text/javascript",
		"text/plain",
		"text/rtf",
		"text/xml",
	}
	supportedAudioMIMEs = []string{
		"audio/aac",
		"audio/flac",
		"audio/l16",
		"audio/m4a",
		"audio/mp3",
		"audio/mp4",
		"audio/mpeg",
		"audio/ogg",
		"audio/opus",
		"audio/vnd.wave",
		"audio/wav",
		"audio/wave",
		"audio/webm",
		"audio/x-wav",
	}
	supportedVideoMIMEs = []string{
		"video/3gpp",
		"video/avi",
		"video/mp4",
		"video/mpeg",
		"video/mpg",
		"video/quicktime",
		"video/webm",
		"video/wmv",
		"video/x-flv",
	}
)

// mimeToKind maps every supported MIME type to its media kind. Built once at
// init so that FromBytes can classify without scanning.
var mimeToKind = func() map[string]MediaKind {
	m := make(map[string]MediaKind)
	for _, t := range supportedImageMIMEs {
		m[t] = KindImage
	}
	for _, t := range supportedDocumentMIMEs {
		m[t] = KindDocument
	}
	for _, t := range supportedAudioMIMEs {
		m[t] = KindAudio
	}
	for _, t := range supportedVideoMIMEs {
		m[t] = KindVideo
	}
	return m
}()

// Media is the interface satisfied by every attachment type: [Image],
// [Document], [Audio], and [Video].
//
// A tool may return media, or a structure containing media, and the SDK
// forwards it to the model as a real attachment rather than base64 buried in
// the tool's text result.
type Media interface {
	Content
	// Kind reports which attachment type this is.
	Kind() MediaKind
	// Bytes returns the raw payload.
	Bytes() []byte
	// MIME returns the payload's MIME type.
	MIME() string
	// Describe returns optional explanatory text, which may be empty.
	Describe() string
}

// media carries the payload shared by every attachment type.
type media struct {
	// Data is the raw file content.
	Data []byte
	// MIMEType identifies the format. It must be one the SDK supports for the
	// attachment's kind.
	MIMEType string
	// Description is optional text explaining what the attachment is. Models
	// use it to decide how to treat the attachment.
	Description string
}

// Image is an image attachment.
type Image struct{ media }

// Document is a document attachment.
type Document struct{ media }

// Audio is an audio attachment.
type Audio struct{ media }

// Video is a video attachment.
type Video struct{ media }

func (Image) isContent()    {}
func (Document) isContent() {}
func (Audio) isContent()    {}
func (Video) isContent()    {}

// Kind reports the media kind, which is useful when handling a [Content] value
// generically.
func (Image) Kind() MediaKind    { return KindImage }
func (Document) Kind() MediaKind { return KindDocument }
func (Audio) Kind() MediaKind    { return KindAudio }
func (Video) Kind() MediaKind    { return KindVideo }

// Bytes returns the raw attachment payload.
func (m media) Bytes() []byte { return m.Data }

// MIME returns the attachment's MIME type.
func (m media) MIME() string { return m.MIMEType }

// Describe returns the attachment's description, which may be empty.
func (m media) Describe() string { return m.Description }

// NewImage returns an image attachment, verifying that mimeType is supported.
func NewImage(data []byte, mimeType, description string) (*Image, error) {
	if err := checkMIME(mimeType, KindImage, supportedImageMIMEs); err != nil {
		return nil, err
	}
	return &Image{media{Data: data, MIMEType: mimeType, Description: description}}, nil
}

// NewDocument returns a document attachment, verifying that mimeType is
// supported.
func NewDocument(data []byte, mimeType, description string) (*Document, error) {
	if err := checkMIME(mimeType, KindDocument, supportedDocumentMIMEs); err != nil {
		return nil, err
	}
	return &Document{media{Data: data, MIMEType: mimeType, Description: description}}, nil
}

// NewAudio returns an audio attachment, verifying that mimeType is supported.
func NewAudio(data []byte, mimeType, description string) (*Audio, error) {
	if err := checkMIME(mimeType, KindAudio, supportedAudioMIMEs); err != nil {
		return nil, err
	}
	return &Audio{media{Data: data, MIMEType: mimeType, Description: description}}, nil
}

// NewVideo returns a video attachment, verifying that mimeType is supported.
func NewVideo(data []byte, mimeType, description string) (*Video, error) {
	if err := checkMIME(mimeType, KindVideo, supportedVideoMIMEs); err != nil {
		return nil, err
	}
	return &Video{media{Data: data, MIMEType: mimeType, Description: description}}, nil
}

func checkMIME(mimeType string, kind MediaKind, allowed []string) error {
	if slices.Contains(allowed, mimeType) {
		return nil
	}
	return fmt.Errorf("unsupported %s MIME type %q; supported types are %s",
		kind, mimeType, strings.Join(allowed, ", "))
}

// FromBytes classifies raw bytes by MIME type and returns the matching
// attachment. Use it when the payload is already in memory; use [FromFile] to
// read from disk and infer the type.
func FromBytes(data []byte, mimeType, description string) (Content, error) {
	kind, ok := mimeToKind[mimeType]
	if !ok {
		return nil, fmt.Errorf("unsupported MIME type %q; supported types are %s",
			mimeType, strings.Join(supportedMIMEs(), ", "))
	}
	m := media{Data: data, MIMEType: mimeType, Description: description}
	switch kind {
	case KindImage:
		return &Image{m}, nil
	case KindDocument:
		return &Document{m}, nil
	case KindAudio:
		return &Audio{m}, nil
	default:
		return &Video{m}, nil
	}
}

// FromFile reads a local file and returns the attachment matching its inferred
// MIME type.
//
// The type is guessed from the file extension, so a file with a missing or
// unrecognized extension is an error rather than a silent fallback.
func FromFile(path, description string) (Content, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	ext := filepath.Ext(path)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		return nil, fmt.Errorf("could not infer a MIME type for extension %q of file %s", ext, path)
	}
	// TypeByExtension may append parameters, as in "text/plain; charset=utf-8".
	if base, _, err := mime.ParseMediaType(mimeType); err == nil {
		mimeType = base
	}
	return FromBytes(data, mimeType, description)
}

// supportedMIMEs returns every MIME type the SDK accepts, sorted, for error
// messages.
func supportedMIMEs() []string {
	out := make([]string, 0, len(mimeToKind))
	for t := range mimeToKind {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// BuiltinSlashCommand names a system slash command.
type BuiltinSlashCommand string

const (
	// SlashPlan asks the agent to plan carefully before acting. It produces an
	// implementation plan artifact and waits for user approval, so it requires
	// [BehaviorInteractive].
	SlashPlan BuiltinSlashCommand = "plan"
)

// SlashCommand is a slash command included in a prompt.
type SlashCommand struct {
	// Name is the command to run.
	Name BuiltinSlashCommand
}

func (SlashCommand) isContent() {}

// Prompt is a convenience constructor turning strings into a [Content] slice.
//
//	agent.Chat(ctx, antigravity.Prompt("Summarize this repository.")...)
func Prompt(parts ...string) []Content {
	out := make([]Content, len(parts))
	for i, p := range parts {
		out[i] = Text(p)
	}
	return out
}

// validatePrompt rejects prompts that carry no usable content.
//
// An empty slice, or one holding only blank text, would produce a turn with
// nothing for the model to act on, so it is caught at the SDK boundary rather
// than sent to the harness.
func validatePrompt(prompt []Content) error {
	if len(prompt) == 0 {
		return fmt.Errorf("%w: prompt is empty", ErrInvalidPrompt)
	}
	onlyBlankText := true
	for _, c := range prompt {
		if c == nil {
			return fmt.Errorf("%w: prompt contains a nil Content element", ErrInvalidPrompt)
		}
		t, isText := c.(Text)
		if !isText || strings.TrimSpace(string(t)) != "" {
			onlyBlankText = false
		}
	}
	if onlyBlankText {
		return fmt.Errorf("%w: prompt contains only empty or whitespace-only text", ErrInvalidPrompt)
	}
	return nil
}
