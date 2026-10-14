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
	// stdout belongs to the protocol. Keep a private handle to it and point os.Stdout at
	// stderr, so a stray write from any library (fmt, default loggers) cannot corrupt the stream.
	out := os.Stdout
	os.Stdout = os.Stderr
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	s := &protocol.Server{Load: protocol.EngineLoader}
	if err := s.Serve(os.Stdin, out); err != nil {
		fmt.Fprintln(os.Stderr, "humbug:", err)
		os.Exit(1)
	}
}
