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
4. **Spans.** A span highlights the member **name** (key) when the pointer addresses an object
   member, and the value when there is no name (the root, array elements); `span.part` says
   which (`key`, `value`). For a missing property or an unmapped pointer, the closest existing
   ancestor is highlighted with `contextual: true`. Duplicate member names set `ambiguous: true`
   and report the last occurrence. Malformed JSON gives `body.decode` with a zero-width span
   (`part: "position"`) at the parser's error offset (`detail.parser` has the parser message).
5. **Limits.** Resource limits bound what one command can cost; see "Limits" below (decided).
   Header handling is described under "Headers".
6. **Schema locations.** `schema.pointer` is relative to the root schema of the validated
   body (`relative: true`); `line`/`column` locate that root schema, from the library. The file
   name is not exposed by the library and is omitted.
7. **Codes.** See `internal/diagnostics/diagnostics.go`. `oneOf`/`anyOf` failures are one parent
   diagnostic (`children`) plus branch diagnostics (`parent`, `branch`); all stay in the flat
   `diagnostics` array. Only the first composition level is grouped. Anything unrecognized is
   `validation.unclassified` with `detail.validation_type/validation_sub_type`.

## Limits

The memory a validation takes is a multiple of its input, not a constant. Measured with the pinned
library on one machine: about 11 to 12 times the body size for a flat document (a 100 MB body peaked
near 1.2 GB), and about 70 times for a structured one (a 19 MB array of small objects peaked at
1.4 GB). The command-line limit alone is therefore not a memory bound, so each limit is checked
before the work it guards.

| Limit | Default | Flag | When exceeded |
| --- | --- | --- | --- |
| Command line | 64 MiB | `--max-line-bytes` | `protocol.line_too_long` (null id); the line is skipped and the stream stays usable |
| Decoded body, per message | 16 MiB | `--max-body-bytes` | that side is `incomplete`, reason `body.too_large` |
| JSON nesting depth | 1,000 | `--max-nesting-depth` (at most 10,000) | that side is `incomplete`, reason `body.too_deep` |
| Header entries, per message | 1,000 | `--max-headers` | `validator.unsupported_input` |
| One decoded header value | 64 KiB | `--max-header-value-bytes` | `validator.unsupported_input` |
| All header names and values | 1 MiB | `--max-header-bytes` | `validator.unsupported_input` |

- **An oversized body is not an error.** A body that is too large or too deep is never handed to
  the library, and its side is `incomplete` (never valid) with a `validation.incomplete` diagnostic
  whose `detail` gives the limit (and, for size, the measured size; never content). Findings the
  library would have made about the body are dropped, since it never saw it; findings about
  everything else (headers, parameters, status) are unaffected. The other side is validated
  normally.
- **Depth applies to JSON bodies only.** Brackets inside strings are not counted. The ceiling of
  10,000 is the depth limit of Go's JSON decoder (and of the span index); it cannot be raised.
- **Header limits are input errors,** like malformed headers, because a message with that many or
  that large headers is not something to validate. The size is of the decoded value, so a
  `base64` header is limited after decoding. A response's headers are judged only when the
  response is being validated.
- **Flags, not the wire.** The limits are set on the command line (`humbug --max-body-bytes N ...`),
  never by a client. There is no "unlimited": each must be at least 1. A usage error exits with
  status 2, explains itself on stderr, and writes nothing to stdout.
- **`initialize` reports the limits in effect** as `limits` (`max_line_bytes`, `max_body_bytes`,
  `max_nesting_depth`, `max_headers`, `max_header_value_bytes`, `max_header_bytes`), so a client can
  adapt. The field is optional: another implementation may report a different set.
- **Plan for memory.** At the defaults a message just under the body limit can peak around 1 GB
  for a structured JSON body (an 11 MB array of small objects measured about 810 MiB), and a command
  near the line limit costs several hundred MiB to receive. Lower `--max-body-bytes` to reduce
  the worst case.
- **There is no per-command timeout.** Validation time is linear in the input (the library's
  regular expressions are RE2, which cannot backtrack catastrophically) and the limits bound the
  input, so measured worst cases are seconds. A client should apply its own timeout, and treat a
  process that dies (for example killed for memory) as a failed transaction and restart it.

## Supported OpenAPI versions

**The OpenAPI 3.1 line is supported: 3.1.0, 3.1.1, 3.1.2 and later 3.1 patch releases.** 3.1.0 is
the version the first target application uses and the version the fixtures declare. Patch
releases are accepted on evidence: `TestPatchReleasesBehaveLikeThe310Snapshots` runs every fixture
against copies of the specification declaring 3.1.1 and 3.1.2, and the results must equal the
3.1.0 snapshots. Other versions can be added later if needed.

`initialize` rejects any other declared `openapi` version with `spec.unsupported_version`, naming
the supported versions, and leaves the process uninitialized (the client may retry with another
specification). This includes 3.0.x, 3.2.x, 4.x, and Swagger 2.0, and also strings that are not
release versions (`3.1` with no patch number, `3.1.0-rc1`, `3.1.01`). The pinned library will load
several of those, and an earlier build did, but they differ from 3.1 (for example `nullable` and
boolean `exclusiveMinimum` in 3.0), so validating them under the same assumptions would silently
misreport. A document with no version, or one the library cannot read, is `spec.invalid`.

The version a specification declares is returned by `initialize` as
`specification.openapi_version`. The supported family is `validation.SupportedVersions`; extend it
only together with fixtures for the new version.

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

## Routing

### What the protocol expects (any implementation)

A request is matched to an operation by the path of its URL, as OpenAPI defines it:

- **Paths are literal and case-sensitive.** A path matches a path template only if every segment
  matches; a template parameter matches exactly one non-empty segment.
- **A trailing slash or a doubled slash is part of the path.** `/users/` and `//users` are not
  `/users`. A request for them is `operation.not_found` unless the specification defines those
  paths.
- A path that matches but has no operation for the request method is
  `operation.method_not_allowed`; no match at all is `operation.not_found`.
- The URL is the effective absolute URL (a fragment is never sent over HTTP, so a client should
  not include one).

Not yet decided, so not part of the contract: whether the host, scheme and port, or a server
base path, must match the specification's `servers`.

### What Humbug does (implementation behavior)

Humbug routes exactly as the validator it wraps (it builds the same router, and a test compares
the two across many URL shapes, so the `operation` in a result is present exactly when the request
was routed and validated). That differs from the expectations above in these ways:

- **Known deviation: trailing and doubled slashes are tolerated.** `/users/`, `//users` and
  `/users//` are routed to `/users` and validated against it, and the result reports operation
  `/users`. This is a leniency of the underlying library, not intended behavior: OpenAPI treats
  these as different paths, and another implementation should report `operation.not_found` for
  them. Do not rely on it. Decided: documenting this is enough for now, and Humbug does not
  enforce strict slashes. The tests that pin the current behavior
  (`TestOperationIsReportedForEveryRoutedPath`, and the slash cases in
  `TestRoutingMatchesTheValidator`) would change if that is revisited.
- **Host, scheme and port are not checked.** `servers` is not used to reject a request sent to a
  different host. The library's strict server-matching mode is deliberately not used (decided);
  it may be offered as an opt-in option later.
- **A server base path is optional.** With `servers: [https://api.example.test/v1]`, both
  `/v1/users` and `/users` match `/users`.
- **Case matters** (as expected): `/Users` is `operation.not_found`.
- **A fragment is part of the path**, so `/users#frag` is not routed (as expected).

## Behavior notes

- Messages are generated from structure and never echo payload values.
- JSON nested more than 10,000 levels deep is treated as malformed (`body.decode`; `detail.parser`
  names the depth limit). This is the depth limit of Go's JSON decoder, which the validation
  library uses, so such a body is rejected by the library regardless. The source-map index
  (spans) costs memory proportional to the number of values, not to their nesting depth.
- Unmatched path/method: request side reports `operation.not_found` /
  `operation.method_not_allowed`; both sides are `complete: false`. A response side that could
  not be routed gets an info-level `validation.incomplete` diagnostic (`reason` names the cause).
- Absent request body for a required body is `body.required`; an explicitly empty body is
  `body.decode`.
- Order: request, then response; within a side by location, parameter name, instance pointer,
  schema pointer, code. IDs (`d1`…) are assigned after sorting.

## Headers

### The wire contract (any implementation)

`headers` on a request or response is the message's header lines **exactly as captured**: an
ordered list of entries, each `[name, value]` or `[name, value, base64]`. The protocol guarantees
that this list reaches the implementation verbatim:

- order is preserved, including the relative order of different names;
- duplicates stay separate entries, and lines are never combined or split (a comma is just a
  character);
- name casing and value text, including leading and trailing whitespace and empty values, are not
  altered;
- nothing is validated: the protocol does not care what a name or value contains.

**Carrying any bytes.** The optional third element, `base64`, is a boolean that defaults to
`false`. When `true`, `value` is the standard base64 encoding (RFC 4648 §4, with padding, no line
breaks) of the header value's bytes, and the implementation decodes it to get the value. This lets
bytes that are not valid UTF-8 (obs-text) or that JSON cannot represent travel unchanged. Names are
never encoded. `["X-Bin","//4=",true]` is the value `0xFF 0xFE`; `["X-A","//4="]` and
`["X-A","//4=",false]` are the literal text `//4=`. A client may use it for any value, not only
those that need it.

The only checks are structural. Every entry must be an array of two or three elements; name and
value must be strings (not `null`), and the third element, if present, must be a boolean. When
`base64` is true the value must be valid base64. Anything else (`["X-A"]`,
`["X-A","v","true"]`, `["X-A","v",true,"extra"]`, `["X-A",null]`, an object, a number) is
`protocol.invalid_message`, because silently dropping, truncating or inventing an element would
change what was sent. `headers` may be absent or empty (both mean no headers).

This contract is what lets another implementation replace Humbug behind the same protocol: it
receives the original header lines and is free to interpret them differently. It is pinned by
`wire_test.go`, which hands the protocol layer a stand-in implementation and asserts exact
equality, including order, for unusual input.

### What Humbug does with headers (implementation behavior)

These rules belong to Humbug, not to the protocol, and another implementation may differ:

- **Base64 values are decoded before anything else.** Humbug supports `base64: true`: every rule
  below applies to the decoded bytes, so a value hidden in base64 is judged exactly like the same
  value sent as text. Non-UTF-8 bytes are accepted; they are never echoed.
- **Headers that cannot exist in HTTP are an input error.** A name that is not an RFC 9110 token,
  or a value (after decoding) with a control character other than tab (CR, LF, NUL, DEL, ...), is rejected with
  `validator.unsupported_input` (an execution failure, not a contract finding). The message never
  repeats the content. Response headers are only judged when the response is being validated.
- **Names are case-insensitive.** Diagnostics always carry the name as declared in the
  specification, whatever casing was sent.
- **Lines are never joined.** Each line stays a separate value. In particular `Set-Cookie` lines
  must never be combined (cookie values can contain commas), and multiple `Cookie` lines are
  cookie pairs, which the library reads together.
- **Repeated lines are reported, not hidden.** If a header the specification declares as a single
  value (not an array) is sent on more than one line, or `Content-Type` is, the side gets a
  `header.repeated` diagnostic with `severity: warning` and `detail.lines`. A warning is not a
  violation: `valid` and `has_violations` are unaffected. Array-typed headers, `Set-Cookie`, and
  headers the specification does not declare are exempt.
- **Empty values count as present.** The library treats an empty request header value as absent,
  which reports a false "missing" for schemas that allow `""` and misses violations for schemas
  that do not. For a declared header parameter whose first line is empty, Humbug discards that
  finding and judges the empty string itself: string schemas are validated, and numeric, integer
  and boolean schemas always reject it (`parameter.invalid`, `detail.empty: "true"`). A header that
  was not sent at all is still `parameter.required`.
- **`Host` is ignored.** The URL in the request is authoritative for routing.
- Header values never appear in diagnostics.

Internally Humbug builds a Go `http.Header` (which canonicalizes names and groups lines by name)
only after the wire list has been handed over and checked; the original list is what the header
rules above are evaluated against.

## Bodies and Content-Encoding

`body_base64` is the **already-decoded** HTTP body: the bytes after any transfer and content
decoding, exactly as the application sees them. Humbug never decompresses or otherwise decodes a
body.

`Content-Encoding` headers are preserved like any other header and passed to the validator, but
Humbug takes no action on them. A `gzip` header over a plain JSON body is validated as JSON, and
spans refer to the supplied bytes. If a client sends still-compressed bytes, that is a client
error: the bytes are reported as `body.decode` (malformed JSON) and nothing mentions gzip.

Clients that capture compressed bodies must decode them first. Body size is bounded by the limits
described under "Limits".

## Known library limitations (pinned pb33f v0.16.1)

- **Label and matrix path styles.** A primitive path parameter with `style: label` (`.5`) or
  `style: matrix` (`;id=5`) is validated with the prefix still attached, so a valid request is
  reported `parameter.invalid`. Humbug does not mask this. Fixtures F27 and F29 and
  `TestPathParameterStyles` pin the behavior and fail if an upgrade changes it. Simple-style arrays
  (`/list/1,2,3`) work.
- **Trailing and doubled slashes are routed** to the path without them (see "Routing", where this
  is listed as a deviation from what the protocol expects).
- **Schema file names.** Cross-file `$ref` schema locations give the line and column in the
  referenced file, but the library does not expose which file, so `schema.uri` is omitted.
- **Remote references** are disabled; a spec that needs one fails with `spec.invalid`, and a test
  confirms no request is made.

## Known gaps

- Missing-property names are only filled when unambiguous (not inside `oneOf`/`anyOf` branches).
- Non-JSON bodies are checked only when their schema is a string (see `diagnostics.md`, "Non-JSON
  bodies"). Form, XML and multipart bodies under object schemas currently stay `incomplete`.
  Planned (decided, not yet implemented): support XML and URL-encoded form bodies through the
  library's opt-in decoders. **Multipart is deferred (decided)**: the library's multipart decoder
  only reads form field values and ignores per-part content types, encodings and files, so it
  is not worth enabling yet. Format validity (for example VCL syntax) is never checked.
- **Security requirements are deferred (decided).** `security.*` codes are in the taxonomy but are
  not produced, and security schemes are not checked.
- Untested: path styles other than simple, label and matrix.
- Repeated lines of an ordinary single-valued request header: the library validates only the first
  line (response headers: every line). Humbug reports the repetition (`header.repeated`, a
  warning) but does not validate the later request lines individually.
- Array and object header parameters sent empty are left to the library, which treats empty as absent.
- `Set-Cookie` response headers are validated per line by the library; Humbug does not parse cookie
  attributes.
- Only the OpenAPI 3.1 line is supported (see "Supported OpenAPI versions"); fixtures are written
  against 3.1.0.
