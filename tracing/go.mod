module github.com/go-steer/antigravity-sdk-go/tracing

// This module's floor is a step above the SDK's own go 1.24: the
// OpenTelemetry API requires 1.25. Tracing is optional, so that cost falls
// only on the projects that opt into it.
go 1.25.0

toolchain go1.26.7

// The SDK is not published yet, so the parent module is resolved from the
// repository rather than the module proxy.
replace github.com/go-steer/antigravity-sdk-go => ../

require (
	github.com/go-steer/antigravity-sdk-go v0.0.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
