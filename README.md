# Humbug

*Bah! That's not in the spec.*

A language-neutral, out-of-process validator for HTTP requests and responses against OpenAPI
specifications, built on `pb33f/libopenapi` and `libopenapi-validator`. Humbug validates
exchanges you give it and returns structured findings; it does not send traffic or decide
whether a test passes.

Status: MVP in progress. Requirements: `docs/humbug-sidecar-requirements-v0.1.md`.

## Build, test, run

Requires Go 1.27+ (see `go.mod`).

    make build      # bin/humbug
    make test       # vet + unit + golden protocol tests
    make example    # drive bin/humbug from the Python example client

`humbug` speaks NDJSON on stdin/stdout (one command per line, one reply per command); stderr
is for process diagnostics only. See `docs/protocol.md` for the protocol as implemented.

## Layout

    cmd/humbug/           executable
    internal/protocol/    NDJSON envelope, commands, error codes
    internal/validation/  pb33f wrapper and normalizer (pb33f types stay here)
    internal/diagnostics/ wire result/diagnostic types, ordering, ID assignment
    internal/sourcemap/   JSON pointer -> byte span / line / column index
    testdata/             specs, cases, golden outputs
    examples/python/      stdlib-only example client

## Not done yet

`schema/*.json` (normative JSON Schemas), `docs/diagnostics.md`, cookie/array/allOf/binary
fixtures, 3.0 vs 3.1 transaction tests. See `docs/protocol.md` "Known gaps".
