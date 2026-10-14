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
    schema/               protocol and diagnostic JSON Schemas
    testdata/             specs, cases, golden outputs
    examples/python/      stdlib-only example client

## Schemas and docs

`schema/protocol.schema.json` and `schema/diagnostic.schema.json` are normative; tests validate
every reply against them. `docs/protocol.md` and `docs/diagnostics.md` describe the behavior.

## Known gaps

See `docs/protocol.md` "Known gaps". Notably: only JSON bodies are schema-validated (others are
reported `incomplete`), and cross-file schema locations and large/gzip bodies are untested.
