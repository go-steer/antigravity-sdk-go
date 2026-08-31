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
	"fmt"
	"reflect"
)

// splitMedia separates attachments out of a tool's return value.
//
// Media is delivered to the model as real attachments; leaving it in the JSON
// result would embed opaque base64 in the text the model reads. The cleaned
// value is v with the attachments removed, and is nil when v was nothing but
// attachments.
//
// Slices, arrays, maps, structs, and pointers are searched recursively so a
// tool can return media nested inside an ordinary result type.
func splitMedia(v any) (cleaned any, found []Media) {
	if v == nil {
		return nil, nil
	}
	if m, ok := v.(Media); ok {
		return nil, []Media{m}
	}
	out, media := splitMediaValue(reflect.ValueOf(v))
	if !out.IsValid() {
		return nil, media
	}
	return out.Interface(), media
}

var mediaType = reflect.TypeFor[Media]()

// splitMediaValue is the reflective half of [splitMedia]. It returns an
// invalid [reflect.Value] to mean "this collapsed to nothing", mirroring the
// Python implementation's use of None.
func splitMediaValue(v reflect.Value) (reflect.Value, []Media) {
	if !v.IsValid() {
		return v, nil
	}

	if v.Type().Implements(mediaType) {
		if v.Kind() == reflect.Pointer && v.IsNil() {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, []Media{v.Interface().(Media)}
	}
	// A non-pointer attachment still satisfies Media through its address.
	if v.CanAddr() && v.Addr().Type().Implements(mediaType) {
		return reflect.Value{}, []Media{v.Addr().Interface().(Media)}
	}

	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return v, nil
		}
		inner, media := splitMediaValue(v.Elem())
		if !inner.IsValid() {
			return reflect.Value{}, media
		}
		// Rewrapping would fight the type system for no benefit; the cleaned
		// element stands on its own.
		return inner, media

	case reflect.Slice, reflect.Array:
		var (
			kept  []any
			media []Media
		)
		for i := range v.Len() {
			item, itemMedia := splitMediaValue(v.Index(i))
			media = append(media, itemMedia...)
			if item.IsValid() {
				kept = append(kept, item.Interface())
			}
		}
		if len(media) == 0 {
			return v, nil // untouched, so keep the caller's concrete type
		}
		if len(kept) == 0 {
			return reflect.Value{}, media
		}
		return reflect.ValueOf(kept), media

	case reflect.Map:
		var media []Media
		kept := map[string]any{}
		for _, key := range v.MapKeys() {
			item, itemMedia := splitMediaValue(v.MapIndex(key))
			media = append(media, itemMedia...)
			if item.IsValid() {
				kept[fmt.Sprint(key.Interface())] = item.Interface()
			}
		}
		if len(media) == 0 {
			return v, nil
		}
		if len(kept) == 0 {
			return reflect.Value{}, media
		}
		return reflect.ValueOf(kept), media

	case reflect.Struct:
		return splitMediaStruct(v)

	default:
		return v, nil
	}
}

// splitMediaStruct searches a struct's exported fields, rebuilding it as a map
// only if it actually contained media.
func splitMediaStruct(v reflect.Value) (reflect.Value, []Media) {
	t := v.Type()

	var media []Media
	kept := map[string]any{}
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		item, itemMedia := splitMediaValue(v.Field(i))
		media = append(media, itemMedia...)
		if item.IsValid() {
			kept[structFieldName(field)] = item.Interface()
		}
	}

	if len(media) == 0 {
		return v, nil // no media, so the caller's own JSON tags still apply
	}
	if len(kept) == 0 {
		return reflect.Value{}, media
	}
	return reflect.ValueOf(kept), media
}

// structFieldName resolves the JSON name of a struct field.
func structFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	for i, c := range tag {
		if c == ',' {
			tag = tag[:i]
			break
		}
	}
	if tag == "" || tag == "-" {
		return f.Name
	}
	return tag
}

// encodeToolResult renders a tool's return value as the JSON object the
// harness expects.
//
// A non-object result is wrapped under "result", matching the Python SDK, so
// the model always receives an object.
func encodeToolResult(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		// A value that will not serialize is still worth reporting; its text
		// form beats failing the whole call.
		fallback, ferr := json.Marshal(map[string]string{"result": fmt.Sprint(v)})
		if ferr != nil {
			return nil, err
		}
		return fallback, nil
	}

	// Only an object can be merged into the harness's result envelope.
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) == nil {
		return data, nil
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{"result": data})
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}
