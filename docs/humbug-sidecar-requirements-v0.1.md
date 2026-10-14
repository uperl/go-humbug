# Humbug Sidecar — MVP Requirements

**Status:** Draft v0.1  
**Date:** 2026-10-13  
**Project:** Humbug  
**Tagline:** *Bah! That's not in the spec.*

## 1. Purpose

Humbug is a language-neutral, out-of-process validator for HTTP requests and responses against OpenAPI specifications. Its first consumer will be the Perl distribution `OpenAPI::Humbug`, but neither the Go executable nor its protocol may depend on Perl, Mojolicious, Test2, or any test framework.

The MVP focuses on use in unit and integration tests. Humbug **does not send HTTP traffic**, intercept clients, act as an HTTP proxy, or decide whether a test passes. It validates supplied exchanges and returns structured findings.

## 2. Goals

1. Validate a complete HTTP exchange or either message independently, using `pb33f/libopenapi` and `pb33f/libopenapi-validator`.
2. Return all reliably detectable violations, separately for request and response, in one result per transaction.
3. Produce stable, machine-readable diagnostic codes, instance paths, schema references, and best-effort source locations.
4. Support source highlighting by mapping JSON instance pointers to byte spans in the **original HTTP body**.
5. Run as a persistent executable using NDJSON on stdin/stdout.
6. Be usable from any programming language that can launch a process and exchange JSON lines.
7. Keep contract violations distinct from failures to execute validation.

## 3. Non-goals for MVP

- Perl adapters, Test2 assertions, `Mojo::UserAgent`/`Test::Mojo` hooks, and CPAN packaging (separate repository).
- Transparent traffic interception, network fetching, HTTP proxying, Unix sockets, or HTTP server transport.
- OpenAPI document editing, generation, or linting beyond errors necessary to initialize the validator.
- Full source highlighting for multipart, XML, binary, or form-encoded bodies.
- Cross-transaction aggregation, historical storage, test expectation handling, or TAP output.
- Arbitrary suppression of errors based on heuristics.

## 4. Executable and process lifecycle

**Executable:** `humbug`.

- Read exactly one UTF-8 JSON command per input line and write exactly one JSON reply per command line, except where process termination prevents a reply.
- Write no logs, progress text, banners, or ANSI escapes to stdout; stderr is reserved for diagnostics about the process itself.
- Process requests sequentially in v0.1. Correlation IDs must be echoed so a future version can support concurrency.
- Initialize a specification once, then validate multiple exchanges without reparsing it each time.
- Exit cleanly on EOF; optionally accept `shutdown` and acknowledge it before exit.
- Malformed individual commands should receive protocol errors when the input can be framed and parsed sufficiently to reply; an error must not corrupt the stream.
- Handle SIGTERM and child-process exit in an ordinary, documented manner. No implicit network activity.

## 5. Wire protocol

**Protocol:** Humbug Protocol, major version `1`, carried over newline-delimited JSON (NDJSON). The canonical normative schema will live in the Go repository.

### 5.1 Envelope

Commands use `id`, `type`, and optional `params`. Replies echo `id`, have `type: "result"` with `result`, or `type: "error"` with `error`. IDs are strings or integers; no coercion is allowed. Unrecognized commands and incompatible versions yield explicit errors.

### 5.2 Initialize

```json
{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"/project/openapi.yaml"}}}
```

Return the agreed protocol version and basic document metadata. Reject missing, unreadable, malformed, or unresolved specifications with an initialization error; do not continue in a partially initialized state. The MVP accepts local filesystem paths; inline specification input is deferred.

### 5.3 Validate

```json
{
  "id": 2,
  "type": "validate",
  "params": {
    "request": {
      "method": "POST",
      "url": "https://example.test/api/users",
      "headers": [["Content-Type", "application/json"]],
      "body_base64": "eyJhZ2UiOiJ0d2VudHktZml2ZSJ9"
    },
    "response": {
      "status": 201,
      "headers": [["Content-Type", "application/json"]],
      "body_base64": "eyJpZCI6MTIzfQ=="
    },
    "validate": {"request": true, "response": true}
  }
}
```

- Request-only validation may omit `response`.
- Response validation requires enough request context (at least method and URL) to identify the operation, even when request schema validation is disabled.
- Preserve headers as ordered name/value pairs, including duplicates and original case.
- Body bytes are base64 encoded; absent body is distinct from an explicitly present empty body. The sidecar must not reconstruct payloads from parsed JSON.
- The effective absolute URL is supplied for routing; original request-target or raw wire formatting is not guaranteed.
- Defaults for omitted validation flags must be specified in the protocol schema and documented; recommended default is to validate each supplied message.
- The sidecar must not assume a failed request means the response is invalid or skip response validation solely because the request violated its schema.

### 5.4 Shutdown

`{"id":3,"type":"shutdown"}` acknowledges termination. EOF is sufficient without an explicit shutdown.

## 6. Result semantics

Each `validate` reply contains one transaction result with separate `request` and `response` sections and, where available, a matched operation (`method`, path template, operation ID).

Each side has a `status`: `valid`, `invalid`, `incomplete`, or `skipped`, plus `diagnostics`. Report confirmed violations even if some other checks could not complete. **Never represent incomplete validation as valid.**

To avoid losing information when an invalid transaction is also incomplete, include independent booleans such as `complete` and `has_violations` at the side and transaction levels; a convenience `valid` boolean is only meaningful when `complete` is true. Final exact field names and invariants must be settled in the protocol JSON Schema before implementation.

**Contract violation:** successful protocol reply containing validation findings.  
**Execution failure:** `type: "error"` with stable error code, e.g. `protocol.invalid_message`, `protocol.unsupported_version`, `spec.invalid`, `validator.internal`.  
**Unmatched operation:** a validation finding, not a crash. Where operation matching prevents response validation, mark that check incomplete or skipped with an explicit reason, never silently valid.

## 7. Diagnostic taxonomy

Every diagnostic has:

- Transaction-local `id`.
- Stable dotted `code` (not a pb33f error string).
- `severity`: `error`, `warning`, or `info`.
- Human-readable `message`, not intended for programmatic matching.
- The affected side (`request` or `response`) and logical location (`body`, `header`, `query`, `path`, `cookie`, etc.).
- Best-effort instance JSON Pointer or equivalent logical parameter identity.
- Best-effort schema reference, including pointer and source file/line/column where supported.
- Optional source span and optional `expected` / `actual` metadata.
- Optional structured child/related diagnostics for composition failures.

Initial codes should cover operation matching, parameters, bodies, JSON Schema keywords (`type`, `required`, `enum`, `format`, `additionalProperties`, `oneOf`, `anyOf`, etc.), response statuses, and response headers. Codes must be documented and tested; unknown pb33f findings must still be represented by a safe generic code, never dropped.

### 7.1 Completeness and relationships

- Preserve all independent findings, including multiple errors in a single request or response.
- Represent `oneOf`/`anyOf` nested errors as parent/child groups when possible; do not flatten repeated subtrees into duplicate top-level complaints.
- Suppression of cascading findings must be conservative and reversible: preserve the original normalized findings and mark relationships, rather than deleting them.
- Stable deterministic ordering: request before response; then location, instance path/source span, code, and stable tie-breakers.
- Do not include arbitrary sensitive body contents in diagnostics by default. A formatter may show excerpts from the original body under explicit caller control.

## 8. Source locations and highlighting

### 8.1 HTTP body mapping (MVP requirement)

For JSON bodies, build a source-aware index from **original decoded HTTP body bytes** mapping RFC 6901 JSON Pointers to source spans. Reuse the index for all diagnostics on that message.

- Byte offsets: zero-based, start inclusive, end exclusive.
- Line and column: one-based; columns measured in UTF-8 bytes, documented consistently.
- Preserve whitespace, indentation, original escaping, and key order.
- Cover nested objects, arrays, escaped property names, Unicode, and multiline documents.
- For missing required properties, locate the closest existing parent object, and mark the span as contextual rather than an exact offending token.
- Duplicate JSON member names make pointer-to-span mapping ambiguous; explicitly mark ambiguity or follow a documented parser-compatible rule, never silently claim false precision.
- If JSON parsing fails, return a useful parse diagnostic with parser-provided location if possible.
- If a pointer cannot be mapped, retain the validation error without a span.

### 8.2 OpenAPI schema locations

Use supported `libopenapi` source-node APIs where possible to resolve a schema reference to its source file, line, and column, including external `$ref` documents. This is best effort; **do not recreate fragile internal hacks or drop a diagnostic when the source location cannot be resolved**.

### 8.3 Header and query locations

Identify the relevant header/query parameter logically. Do not invent line/column numbers for an HTTP wire representation the sidecar never received.

## 9. Validation behavior

- Support the OpenAPI versions supported by the pinned pb33f dependencies, documented in the release notes; do not claim unsupported versions.
- Validate request methods/paths, path/query/header/cookie parameters, media types, bodies, response status, response headers, and response bodies to the extent supported by the library.
- Support external local `$ref` resolution under a documented file-resolution policy; initialization must fail clearly when required references cannot be resolved.
- Avoid accidental remote reference fetching; if the underlying library supports remote references, disable them by default and document any opt-in behavior outside the MVP.
- Preserve distinct error categories for malformed payloads, unsupported content types, missing bodies, schema violations, and inability to inspect a requested body.
- Define and test behavior for missing `Content-Type`, empty bodies, binary bodies, repeated headers, and non-JSON payloads. Do not silently assert that uninspected content conforms.

## 10. Repository and implementation

The Go repository is `humbug`, containing the executable and its reusable internal packages. Suggested layout:

```text
humbug/
├── cmd/humbug/
├── internal/protocol/
├── internal/validation/
├── internal/diagnostics/
├── internal/sourcemap/
├── schema/
│   ├── protocol.schema.json
│   └── diagnostic.schema.json
├── testdata/
│   ├── specs/
│   ├── cases/
│   └── expected/
├── docs/
│   ├── protocol.md
│   └── diagnostics.md
├── examples/
│   └── python/
├── go.mod
├── Makefile
└── README.md
```

- Use public pb33f APIs, pin dependency versions, and document the minimum Go toolchain.
- Do not depend on Perl or on any client-side adapter implementation.
- Keep protocol types separate from pb33f types; never expose pb33f's Go-specific internal structures as the wire contract.
- Provide clear build, run, and test commands.
- Include a tiny non-Go example client (Python or shell) proving language neutrality.

## 11. Tests and fixtures

Reuse the pb33f spike fixtures where applicable and add golden protocol-level tests. Minimum coverage:

1. Valid request and response.
2. Request-only and response-only validation.
3. Multiple independent errors in one transaction; request and response both invalid.
4. Expected invalid request paired with independently valid error response (the sidecar reports facts, not test expectations).
5. Incorrect JSON types, missing properties, arrays, escaped keys, Unicode, and malformed JSON.
6. `oneOf` and `anyOf` nesting and duplicate-tree normalization.
7. Missing/invalid query, path, header, and cookie parameters.
8. Unsupported response status, media type, and body schema violations.
9. Repeated headers, empty versus absent bodies, binary/non-JSON content, and unresolved `$ref`.
10. JSON Pointer → byte span and line/column mapping, including missing properties and ambiguous duplicate keys.
11. Stable diagnostic codes and deterministic output across repeated runs.
12. Protocol initialization errors, malformed commands, unknown command/version, EOF, and clean shutdown.
13. Multiple commands in one process with preserved correlation IDs and no stdout contamination.
14. Behavior when requested validation cannot complete; never report false success.

Tests should compare structured fields rather than fragile English message text. Maintain a small set of snapshot outputs for human inspection.

## 12. Acceptance criteria

The MVP is complete when:

- A clean build produces a standalone `humbug` executable that accepts initialization followed by multiple validations over stdin/stdout.
- A non-Go client can consume the documented protocol without reverse-engineering the Go source.
- The sidecar reports one result per transaction, with independently queryable request and response findings.
- Multiple independent violations are preserved and grouped without noisy duplication of composition subtrees.
- JSON instance paths are mapped to original HTTP body spans and line/column locations for supported cases.
- OpenAPI schema source locations are attached when supported, without unsupported hacks.
- Errors, incomplete validation, and genuine contract violations are distinguishable.
- Golden fixtures pass and output ordering is deterministic.
- The protocol and diagnostic schemas, limitations, minimum Go version, and usage examples are documented.

## 13. Deferred work

- Unix-domain sockets, TCP transport, or concurrent command processing.
- Inline OpenAPI specifications, reloads, multiple specifications per process.
- Streaming or very large bodies, incremental parsing, resource limits beyond essential safeguards.
- XML/multipart/form-specific source highlighting.
- Production HTTP interception, HTTP proxy, transparent capture.
- Perl integration: `Mojo::Message::Request`, `Mojo::Message::Response`, `Mojo::Transaction::HTTP`, `Mojo::UserAgent`, `Test::Mojo`, `HTTP::Request`, `HTTP::Response`, and eventually `HTTP::Tiny`.
- TAP/Test2 formatting and expected-failure assertion policy.

## 14. Open decisions before freezing protocol v1

1. Exact semantics of top-level `status`, `complete`, `has_violations`, and optional `valid`.
2. Whether initialization failure permits a subsequent initialize attempt in the same process.
3. Default validation flags and the minimal request context required for response-only checks.
4. Precise JSON source span selection for keys versus values and parser errors.
5. Header normalization, duplicate handling, and body size/resource limit defaults.
6. How schema source locations behave for references across multiple files.
7. The initial stable diagnostic-code catalog and representation of nested composition errors.

These decisions should be resolved with explicit protocol tests rather than accidental implementation behavior.

## 15. Reference artifacts

- Humbug protocol draft v0.1 (previously named OpenAPI Sidecar Protocol).
- Diagnostic Taxonomy draft v0.1.
- pb33f Spike Requirements and spike findings/implementation.

This document is a requirements baseline, not a claim that the MVP has been implemented.
