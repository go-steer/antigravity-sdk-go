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

// Package harness manages the localharness subprocess: locating the binary,
// performing the stdio handshake, and exchanging events over the WebSocket it
// exposes.
//
// The protocol has two distinct framing schemes, and mixing them up is the
// most common way to break this layer:
//
//   - stdin/stdout carries binary protobuf, length-prefixed with a 4-byte
//     little-endian uint32. It is used exactly once, for the startup
//     handshake that reports the port and API key.
//   - The WebSocket carries protojson text frames for every subsequent event.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// PathEnvVar names the environment variable holding an explicit path to the
// localharness binary.
const PathEnvVar = "ANTIGRAVITY_HARNESS_PATH"

// binaryName is the executable's name as found on PATH.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "localharness.exe"
	}
	return "localharness"
}

// FindBinary locates the localharness executable.
//
// Resolution order matches the Python SDK: an explicit path in the supplied
// env map, then the same variable in the process environment, then PATH.
//
// Go has no equivalent of the Python wheel that ships the binary inside the
// installed package, so there is no packaged-location step. Callers who
// obtained the binary from a Python install should point [PathEnvVar] at it.
func FindBinary(env map[string]string) (string, error) {
	if p := env[PathEnvVar]; p != "" {
		return verifyExecutable(p)
	}
	if p := os.Getenv(PathEnvVar); p != "" {
		return verifyExecutable(p)
	}
	if p, err := exec.LookPath(binaryName()); err == nil {
		return p, nil
	}
	return "", fmt.Errorf(
		"could not find the %s binary: set %s to its absolute path, put it on PATH, "+
			"or pass an explicit path in the configuration",
		binaryName(), PathEnvVar)
}

// verifyExecutable checks that an explicitly configured path actually exists,
// so a stale environment variable produces a clear error rather than an
// obscure exec failure later.
func verifyExecutable(path string) (string, error) {
	// #nosec G703 -- the path is the harness location the caller chose, and
	// checking it is the whole point of this function. Refusing to stat a
	// caller-supplied path would make the check impossible.
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s points at %s, which cannot be used: %w", PathEnvVar, path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s points at %s, which is a directory, not an executable", PathEnvVar, path)
	}
	return path, nil
}
