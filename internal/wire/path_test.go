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

package wire

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain path", "/home/user/x", "/home/user/x"},
		{"relative path", "src/main.go", "src/main.go"},
		{"file uri", "file:///home/user/x", "/home/user/x"},
		{"percent encoded", "file:///home/user/a%20b.txt", "/home/user/a b.txt"},
		{"cns uri", "cns://el-d/home/user/x", "/cns/el-d/home/user/x"},
		{"other scheme", "https://example.com/x", "https://example.com/x"},
		// A bare Windows path would parse as scheme "c" if it were treated
		// as a URI, so it has to be left alone.
		{"windows drive", `C:\src\main.go`, `C:\src\main.go`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tt.name == "file uri" {
				t.Skip("native separators differ on Windows")
			}
			if got := NormalizePath(tt.in); got != tt.want {
				t.Errorf("NormalizePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeWorkspace(t *testing.T) {
	if got, err := NormalizeWorkspace(""); err != nil || got != "" {
		t.Errorf("NormalizeWorkspace(\"\") = %q, %v; want \"\", nil", got, err)
	}

	// A CNS path is already absolute and is not on this filesystem: resolving
	// it locally would corrupt it.
	const cns = "cns://el-d/home/user/x"
	if got, err := NormalizeWorkspace(cns); err != nil || got != "/cns/el-d/home/user/x" {
		t.Errorf("NormalizeWorkspace(%q) = %q, %v", cns, got, err)
	}

	const remote = "https://example.com/repo"
	if got, err := NormalizeWorkspace(remote); err != nil || got != remote {
		t.Errorf("NormalizeWorkspace(%q) = %q, %v; want it untouched", remote, got, err)
	}
}

func TestNormalizeWorkspaceResolvesLocalPaths(t *testing.T) {
	got, err := NormalizeWorkspace("some/relative/dir")
	if err != nil {
		t.Fatalf("NormalizeWorkspace: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("NormalizeWorkspace returned %q, want an absolute path", got)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}
	got, err = NormalizeWorkspace("~/projects")
	if err != nil {
		t.Fatalf("NormalizeWorkspace: %v", err)
	}
	if want := filepath.Join(home, "projects"); got != want {
		t.Errorf("NormalizeWorkspace(\"~/projects\") = %q, want %q", got, want)
	}

	got, err = NormalizeWorkspace("~")
	if err != nil {
		t.Fatalf("NormalizeWorkspace: %v", err)
	}
	if got != home {
		t.Errorf("NormalizeWorkspace(\"~\") = %q, want %q", got, home)
	}
}

func TestNormalizeWorkspaceExpandsAFileURI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native separators differ on Windows")
	}
	got, err := NormalizeWorkspace("file:///tmp/work")
	if err != nil {
		t.Fatalf("NormalizeWorkspace: %v", err)
	}
	if got != "/tmp/work" {
		t.Errorf("NormalizeWorkspace = %q, want /tmp/work", got)
	}
}
