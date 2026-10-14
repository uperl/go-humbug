# OpenAPI Sidecar Diagnostic Taxonomy — Draft v0.1

## Purpose

Define language-neutral, structured validation diagnostics for the OpenAPI sidecar. The sidecar detects and normalizes contract violations; clients decide assertion semantics and presentation. One transaction yields one validation result with zero or more diagnostics.

## Diagnostic object

```json
{
  "id": "d1",
  "code": "schema.type",
  "severity": "error",
  "message": "expected integer, got string",
  "instance": {
    "side": "request",
    "location": "body",
    "pointer": "/age",
    "span": {"start": 25, "end": 39, "line": 3, "column": 10}
  },
  "schema": {"uri": "openapi.yaml", "pointer": "/components/schemas/User/properties/age"},
  "expected": "integer",
  "actual": "string",
  "related": [],
  "caused_by": [],
  "suppressed_by": null
}
```

`id` is unique within a transaction. `code` is machine-readable and stable within the protocol's compatibility policy; `message` is human-readable and not stable. Optional fields must be omitted if unavailable, rather than invented. `span` is a half-open byte range into the original decoded message body; offsets are zero-based, line/column are one-based and columns count UTF-8 bytes. Pointers use RFC 6901. For missing properties, point to the enclosing object when possible. Never infer source positions from schema positions.

## Initial code taxonomy

| Namespace | Codes / examples |
| --- | --- |
| operation | `operation.not_found`, `operation.ambiguous` |
| parameter | `parameter.required`, `parameter.invalid` |
| body | `body.required`, `body.media_type`, `body.decode` |
| schema | `schema.type`, `schema.required`, `schema.enum`, `schema.format`, `schema.additional_properties`, `schema.oneOf`, `schema.anyOf`, `schema.allOf`, `schema.not`, `schema.pattern`, `schema.minLength` |
| response | `response.status`, `response.header.required` |
| security | `security.requirement`, `security.scheme` (only when checks supported) |

Maintain a documented mapping from underlying validator errors to normalized codes. Unknown errors may use a documented fallback code with the original library detail stored separately; never guess a more specific code.

## Relationships and suppression

- `related`: associated diagnostics or source locations, without asserting causality.
- `caused_by`: transaction-local diagnostic IDs with a demonstrable causal relationship.
- `suppressed_by`: optional transaction-local ID of the primary diagnostic that should replace this item in normal presentation.
- Preserve every underlying diagnostic in the machine-readable result, including suppressed ones.
- Suppress only with justified rules, not merely shared paths or similar messages.
- If a value is a string where an object is required, any downstream required-property diagnostics genuinely contingent on object traversal may be suppressed; independently invalid sibling properties must remain visible.

## Composition keywords

For `oneOf` and `anyOf`, represent the composition failure as the primary diagnostic with branch errors grouped as related details. Distinguish zero matches from multiple matches for `oneOf`; do not manufacture a best-matching branch unless a documented scoring algorithm is implemented. `allOf` failures should preserve actionable independent failures. Keep branch metadata structured rather than concatenated into English text.

## Ordering and deduplication

Deterministically order by message side (request, response), component (operation, path, query, header, cookie, body), source offset if known, code, and pointer. Deduplicate only truly equivalent findings; never discard different constraints merely because they share a location. An optional fingerprint can group repeated problems across transactions based on code, side, operation identity, instance pointer, and schema reference, excluding actual values and message text. Fingerprints are not guaranteed stable across spec changes.

## Severity and result status

Diagnostic severity is `error`, `warning`, or `info`. Severity does not by itself indicate whether requested checks completed. Individual message status is `valid`, `invalid`, `incomplete`, or `skipped`. A transaction result should independently expose whether confirmed violations exist and whether all requested checks completed. If both invalidity and incompleteness occur, preserve both facts. Protocol/execution failures remain separate error responses and must never be interpreted as expected contract violations.

## Presentation

Wire responses contain structured diagnostics, not ANSI escapes or TAP output. Client formatters may offer compact, normal, and verbose modes. Normal mode shows primary actionable violations grouped by request/response and component; verbose mode includes suppressed or related errors and source excerpts. A reference Go formatter can be provided for testing and CLI usage. Avoid copying secrets or full payloads into messages by default.

## Open questions

1. Whether the sidecar should return an optional normalized composition tree in addition to flat diagnostic relationships.
2. Exact semantics and fields for a transaction with both confirmed violations and incomplete checks.
3. Whether source-location spans for non-body components should refer to a separately supplied raw HTTP head.
4. Versioning policy for diagnostic codes and normalization behavior.
