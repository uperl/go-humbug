// Command humbug is a persistent OpenAPI validation sidecar speaking Humbug
// Protocol v1 (NDJSON) on stdin/stdout. Stderr is for process diagnostics only.
//
// Usage: humbug [flags]. The flags set the resource limits (see internal/limits and
// docs/protocol.md); they are not part of the wire protocol. A usage error exits with status 2,
// writes to stderr, and never touches stdout.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"humbug/internal/limits"
	"humbug/internal/protocol"
)

func main() {
	lim := limits.Default()
	fs := flag.NewFlagSet("humbug", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	limits.Bind(fs, &lim)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2) // the flag package has already described the problem on stderr
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "humbug: unexpected argument %q (the protocol is spoken on stdin and stdout)\n", fs.Arg(0))
		os.Exit(2)
	}
	if err := lim.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "humbug:", err)
		os.Exit(2)
	}

	// stdout belongs to the protocol. Keep a private handle to it and point os.Stdout at
	// stderr, so a stray write from any library (fmt, default loggers) cannot corrupt the stream.
	out := os.Stdout
	os.Stdout = os.Stderr
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	s := &protocol.Server{Load: protocol.NewEngineLoader(lim), Limits: lim}
	if err := s.Serve(os.Stdin, out); err != nil {
		fmt.Fprintln(os.Stderr, "humbug:", err)
		os.Exit(1)
	}
}
