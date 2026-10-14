// Command humbug is a persistent OpenAPI validation sidecar speaking Humbug
// Protocol v1 (NDJSON) on stdin/stdout. Stderr is for process diagnostics only.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"humbug/internal/protocol"
)

func main() {
	// Libraries log through slog's default logger; keep that off stdout.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	s := &protocol.Server{Load: protocol.EngineLoader}
	if err := s.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "humbug:", err)
		os.Exit(1)
	}
}
