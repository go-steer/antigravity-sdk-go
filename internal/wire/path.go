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

// Package wire translates between the harness's on-the-wire representations
// and the plain Go values the SDK exposes.
package wire

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizePath converts a wire-format URI to a platform-native absolute
// path.
//
// Paths reach the SDK as file:// or cns:// URIs, but callers expect the
// filesystem paths their own code uses. Anything that is not one of those two
// schemes is returned unchanged, including plain paths.
func NormalizePath(path string) string {
	if path == "" {
		return ""
	}
	// Only strings that actually look like URIs are parsed. A bare Windows
	// path such as C:\src would otherwise parse as scheme "c".
	if !strings.Contains(path, "://") && !strings.HasPrefix(path, "file:") {
		return path
	}

	u, err := url.Parse(path)
	if err != nil {
		return path
	}

	switch u.Scheme {
	case "file":
		return filePathFromURL(u)
	case "cns":
		// cns://el-d/home/user/x is the canonical /cns/el-d/home/user/x.
		return "/cns/" + u.Host + u.Path
	default:
		return path
	}
}

// filePathFromURL converts the path component of a file:// URL to a native
// path. url.Parse has already percent-decoded it.
func filePathFromURL(u *url.URL) string {
	p := u.Path
	if runtime.GOOS != "windows" {
		return p
	}
	// A Windows URL path is /C:/dir/file; drop the leading slash and switch
	// to backslashes.
	p = strings.TrimPrefix(p, "/")
	if u.Host != "" {
		// file://server/share is a UNC path.
		p = `//` + u.Host + "/" + p
	}
	return filepath.FromSlash(p)
}

// NormalizeWorkspace normalizes a wire URI, expands a leading ~, and resolves
// the result to an absolute path.
//
// CNS paths are returned as-is: they are already absolute and are not
// filesystem paths, so resolving them against the local tree would corrupt
// them.
func NormalizeWorkspace(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	normalized := NormalizePath(path)
	if strings.HasPrefix(normalized, "/cns/") {
		return normalized, nil
	}
	// A remaining non-file scheme is a remote location we must not touch.
	if u, err := url.Parse(normalized); err == nil && u.Scheme != "" && u.Scheme != "file" && len(u.Scheme) > 1 {
		return normalized, nil
	}

	expanded, err := expandHome(normalized)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

// expandHome replaces a leading ~ with the user's home directory.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
