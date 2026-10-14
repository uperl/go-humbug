# Humbug Protocol v1 — as implemented

Supersedes the draft in `openapi-sidecar-protocol-v0.1.md` where they differ. NDJSON over
stdin/stdout; commands `initialize`, `validate`, `shutdown`. Envelope, base64 bodies, ordered
header pairs, and the error/result split are as in the draft.

## Decisions on the requirements' open questions (§14), provisional until the JSON Schema is frozen

1. **Status fields.** Each side has `status` (`valid|invalid|incomplete|skipped`), `complete`,
   `has_violations`, and `valid` (= `complete && !has_violations`). The transaction repeats
   `complete`, `has_violations`, `valid` over the sides that were requested. If a side has
   violations *and* could not finish, `status` is `invalid` and `complete` is `false`. A side that
   was not requested is `skipped` with `complete: true`.
2. **Re-initialize.** A failed `initialize` leaves the process uninitialized and may be retried.
   After a successful one, a second `initialize` gets `protocol.already_initialized`.
3. **Flags.** `validate.request` defaults to true; `validate.response` defaults to true iff a
   response was supplied. `validate.response: true` without a response, or both flags false,
   is `protocol.invalid_message`. Request method and absolute URL are always required.
4. **Spans.** Value spans for the instance pointer; for a missing property or an unmapped
   pointer, the closest existing ancestor with `contextual: true`. Duplicate member names set
   `ambiguous: true` and report the last occurrence. Malformed JSON gives `body.decode` with a
   zero-width span at the parser's error offset (`detail.parser` has the parser message).
5. **Limits.** Command lines over 256 MiB get `protocol.line_too_long`. Header names are
   passed through; repeated headers are passed as repeated values and interpreted by pb33f.
6. **Schema locations.** `schema.pointer` is relative to the root schema of the validated
   body (`relative: true`); `line`/`column` locate that root schema, from the library. The file
   name is not exposed by the library and is omitted.
7. **Codes.** See `internal/diagnostics/diagnostics.go`. `oneOf`/`anyOf` failures are one parent
   diagnostic (`children`) plus branch diagnostics (`parent`, `branch`); all stay in the flat
   `diagnostics` array. Only the first composition level is grouped. Anything unrecognized is
   `validation.unclassified` with `detail.validation_type/validation_sub_type`.

## Behavior notes

- Messages are generated from structure and never echo payload values.
- Unmatched path/method: request side reports `operation.not_found` /
  `operation.method_not_allowed`; both sides are `complete: false`. A response side that could
  not be routed gets an info-level `validation.incomplete` diagnostic (`reason` names the cause).
- Absent request body for a required body is `body.required`; an explicitly empty body is
  `body.decode`.
- Order: request, then response; within a side by location, parameter name, instance pointer,
  schema pointer, code. IDs (`d1`…) are assigned after sorting.

## Known gaps

- Missing-property names are only filled when unambiguous (not inside `oneOf`/`anyOf` branches).
- Only JSON bodies are schema-validated; other media types make the side `incomplete`
  (`validation.incomplete`, reason `body.not_inspected`). Form, XML and multipart support is deferred.
- Untested: `$ref` across files for schema locations, gzip and very large bodies, path parameters
  with unusual styles, and security requirements.
- Repeated request headers are validated as pb33f sees them (fixture: the second `X-Request-Id`
  value changes the outcome); this is not normalized by Humbug.
- 3.0 is covered by one transaction test (`nullable`, boolean `exclusiveMinimum`) only.
