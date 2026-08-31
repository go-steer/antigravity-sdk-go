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
	"runtime"
	"runtime/debug"
	"sync"

	"google.golang.org/protobuf/proto"

	pb "github.com/go-steer/antigravity-sdk-go/internal/genproto/google/antigravity/proto"
)

// modulePath is this SDK's module path, used to find its own version in the
// build info of whatever binary embeds it.
const modulePath = "github.com/go-steer/antigravity-sdk-go"

var clientInfoOnce = sync.OnceValue(buildClientInfo)

// ClientInfo describes this SDK to the harness. The result is computed once
// and shared; treat it as read-only.
func ClientInfo() *pb.ClientInfo { return clientInfoOnce() }

func buildClientInfo() *pb.ClientInfo {
	return pb.ClientInfo_builder{
		Language:        proto.String("go"),
		Version:         proto.String(sdkVersion()),
		LanguageVersion: proto.String(runtime.Version()),
		Os:              proto.String(runtime.GOOS),
		// os_version is left unset: the Python SDK fills it from
		// platform.release(), and Go has no portable equivalent.
	}.Build()
}

// sdkVersion reports this module's version as recorded by the Go toolchain.
//
// It is only available when the SDK is consumed as a dependency; a binary
// built from this repo directly reports no version for its own module.
func sdkVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if info.Main.Path == modulePath && info.Main.Version != "" {
		return info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath && dep.Version != "" {
			return dep.Version
		}
	}
	return "unknown"
}
