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

## Process behavior

- stdout carries protocol replies only. Library logging (pb33f logs reference errors) goes to
  stderr, and `os.Stdout` is pointed at stderr inside the process so stray writes cannot corrupt
  the stream. A clean run writes nothing to stderr.
- EOF (including a final line with no newline) ends the process with exit status 0. `shutdown` is
  acknowledged, then the process exits 0 and ignores any later input.
- SIGTERM is not handled specially: the process is terminated by the signal at once, mid-command
  if need be. Clients should treat a dead process as a failed transaction and restart it.
- If the client stops reading stdout, the next failed write ends the process (non-zero exit).
- Nothing is printed until a command arrives. Remote `$ref` fetching is disabled in the library
  configuration (not separately tested).

## Behavior notes

- Messages are generated from structure and never echo payload values.
- Unmatched path/method: request side reports `operation.not_found` /
  `operation.method_not_allowed`; both sides are `complete: false`. A response side that could
  not be routed gets an info-level `validation.incomplete` diagnostic (`reason` names the cause).
- Absent request body for a required body is `body.required`; an explicitly empty body is
  `body.decode`.
- Order: request, then response; within a side by location, parameter name, instance pointer,
  schema pointer, code. IDs (`d1`…) are assigned after sorting.

## Content-Encoding and body size

Bodies in the protocol are the *decoded* HTTP body, but `Content-Encoding` is honored defensively:

- `identity` or none: used as is.
- `gzip` / `x-gzip` (alone): decompressed by Humbug, then validated. Spans and pointers refer to the
  decompressed bytes. Decompressed size is capped at 64 MiB (`validation.MaxDecodedBodyBytes`).
- Corrupt gzip: `body.decode` (`detail.content_encoding`), side `invalid`.
- Over the cap: `validation.incomplete`, reason `body.too_large`.
- Any other or stacked encoding (`br`, `deflate`, `gzip, br`): `validation.incomplete`, reason
  `body.content_encoding_unsupported`. The library's findings on the undecoded bytes are discarded.

No other size limits apply; an 8 MiB JSON body is validated and mapped in well under a second.

## Known library limitations (pinned pb33f v0.16.1)

- **Label and matrix path styles.** A primitive path parameter with `style: label` (`.5`) or
  `style: matrix` (`;id=5`) is validated with the prefix still attached, so a valid request is
  reported `parameter.invalid`. Humbug does not mask this. Fixtures F27 and F29 and
  `TestPathParameterStyles` pin the behavior and fail if an upgrade changes it. Simple-style arrays
  (`/list/1,2,3`) work.
- **Schema file names.** Cross-file `$ref` schema locations give the line and column in the
  referenced file, but the library does not expose which file, so `schema.uri` is omitted.
- **Remote references** are disabled; a spec that needs one fails with `spec.invalid`, and a test
  confirms no request is made.

## Known gaps

- Missing-property names are only filled when unambiguous (not inside `oneOf`/`anyOf` branches).
- Only JSON bodies are schema-validated; other media types make the side `incomplete`
  (`validation.incomplete`, reason `body.not_inspected`). Form, XML and multipart support is deferred.
- Untested: security requirements, and path styles other than simple, label and matrix.
- Repeated request headers are validated as pb33f sees them (fixture: the second `X-Request-Id`
  value changes the outcome); this is not normalized by Humbug.
- 3.0 is covered by one transaction test (`nullable`, boolean `exclusiveMinimum`) only.
