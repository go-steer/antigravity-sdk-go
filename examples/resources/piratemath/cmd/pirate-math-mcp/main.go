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

// Command pirate-math-mcp serves the pirate math MCP tools over stdio or
// Streamable HTTP.
//
//	go run ./examples/resources/piratemath/cmd/pirate-math-mcp --transport=stdio
//	go run ./examples/resources/piratemath/cmd/pirate-math-mcp --transport=http
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/go-steer/antigravity-sdk-go/examples/resources/piratemath"
)

func main() {
	transport := flag.String("transport", "stdio", "transport to serve: stdio or http")
	flag.Parse()

	// The work is in run so the deferred signal and server teardown actually
	// happen: log.Fatal exits the process without unwinding.
	if err := run(*transport); err != nil {
		log.Fatal(err)
	}
}

func run(transport string) error {
	switch transport {
	case "stdio":
		// Anything written to stdout that is not a JSON-RPC message corrupts
		// the stream, so diagnostics go to stderr.
		log.SetOutput(os.Stderr)
		return piratemath.ServeStdio(os.Stdin, os.Stdout)

	case "http":
		ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stopSignals()

		url, stop, err := piratemath.Start(ctx)
		if err != nil {
			return err
		}
		defer stop()

		fmt.Fprintln(os.Stderr, "pirate math listening on", url)
		<-ctx.Done()
		return nil

	default:
		return fmt.Errorf("unknown transport %q: use stdio or http", transport)
	}
}
