# OpenAPI Sidecar Protocol — Draft v0.1

Status: Draft; not yet an implemented or frozen protocol.

## Goals

A language-neutral contract for validating captured HTTP requests and responses against an OpenAPI document. The sidecar is not a test runner and does not decide whether a deliberately invalid request is an expected test outcome.

## Transport and framing

- Persistent subprocess; UTF-8 NDJSON, one JSON object per line on stdin and stdout.
- stdout is exclusively protocol traffic; stderr is exclusively diagnostics/logging.
- Process requests sequentially in v0.1; every reply echoes the request ID (string or integer).
- Clients must read stdout while writing requests to avoid deadlock. EOF terminates the process cleanly.
- No arbitrary application output on stdout.
- Protocol version is negotiated on initialization; version 1 is proposed.

## Envelope

Commands: `{ "id": 1, "type": "initialize|validate|shutdown", "params": {...} }` (params optional for shutdown).

Replies: `{ "id": 1, "type": "result", "result": {...} }` or `{ "id": 1, "type": "error", "error": { "code": "...", "message": "..." } }`.

The format is not JSON-RPC.

## Initialization

```json
{"id":1,"type":"initialize","params":{"protocol_version":1,"spec":{"path":"/project/openapi.yaml"}}}
```

```json
{"id":1,"type":"result","result":{"protocol_version":1,"specification":{"openapi_version":"3.1.0","title":"Example API"}}}
```

Paths are resolved relative to the sidecar process working directory. Initializing again is not supported in the MVP; launch another process to use a different specification.

## Validation

```json
{
  "id": 2,
  "type": "validate",
  "params": {
    "request": {
      "method": "POST",
      "url": "https://example.com/api/users",
      "headers": [["Content-Type", "application/json"]],
      "body_base64": "eyJuYW1lIjoiQWxpY2UifQ=="
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

Header arrays preserve duplicates and order. `url` is the effective absolute URL. Body data is original bytes encoded as base64, not reconstructed parsed JSON. A missing body is distinct from an empty body; this distinction needs implementation tests. For response-only validation, request context (at least method and URL) is still required to identify the operation. `validate` flags default to true for supplied messages and false for omitted messages (provisional).

## Validation reply

```json
{
  "id": 2,
  "type": "result",
  "result": {
    "valid": false,
    "operation": {"method": "POST", "path": "/api/users", "operation_id": "createUser"},
    "request": {
      "status": "invalid",
      "diagnostics": [
        {
          "code": "schema.type",
          "severity": "error",
          "message": "expected integer, got string",
          "instance": {
            "location": "body",
            "pointer": "/age",
            "span": {"start": 25, "end": 39, "line": 3, "column": 10}
          },
          "schema": {"pointer": "/components/schemas/User/properties/age"},
          "expected": "integer",
          "actual": "string"
        }
      ]
    },
    "response": {"status": "valid", "diagnostics": []}
  }
}
```

The span and values above are illustrative, not derived from the sample request body. Status values are `valid`, `invalid`, and `skipped`. The aggregate `valid` is true only if all requested checks completed without error-severity contract violations. A failure to complete validation is a protocol/execution error, not a contract violation. A requested side must never silently become `skipped` and produce an aggregate success.

## Source mapping

- Instance location may identify a body, header, query, cookie, path, or status.
- Body source spans are byte offsets into decoded original body bytes; start inclusive, end exclusive, zero-based.
- Line and column are one-based, with columns measured in UTF-8 bytes.
- JSON pointers follow RFC 6901. For a missing property, identify its parent object if available.
- Schema pointers identify the relevant constraint; source URI, line, and column can be added when available.
- Source spans and pointers are optional when an exact location cannot be established.
- Do not echo full sensitive payloads in diagnostic messages by default.

## Error semantics

A contract violation is a successful `result` with `valid:false`. Invalid protocol, unsupported version, invalid specification, unsupported input, or internal failure return an `error` reply and are not valid/invalid contract outcomes.

```json
{"id":2,"type":"error","error":{"code":"validator.internal","message":"validation could not be completed"}}
```

Suggested error codes: `protocol.invalid_message`, `protocol.unsupported_version`, `spec.invalid`, `validator.unsupported_input`, `validator.internal`.

## Shutdown

`{"id":3,"type":"shutdown"}` receives a result acknowledgement before process exit. EOF also ends the process cleanly.

## Deferred / open questions

- Inline OpenAPI specs and multiple loaded specs.
- UNIX socket transport, parallel validation, streaming payloads.
- Exact representation of absent versus empty bodies and transport-level HTTP metadata.
- Diagnostic code taxonomy and grouping of cascading errors.
- Limits, timeouts, and handling of oversized input lines.
- Schema resolution and stable URI handling for external references.
- Security boundaries for local file references in the OpenAPI document.
- Exact policy for malformed JSON payloads versus unsupported parser input.

## Integration boundary

The Go sidecar owns OpenAPI matching, validation, diagnostic normalization, and source mapping. Language-specific adapters own capture, subprocess lifecycle, formatting, expected-failure logic, and test assertions. One Perl test assertion per transaction remains a responsibility of the Perl test framework.
