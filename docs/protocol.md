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
5. **Limits.** Command lines over 256 MiB get `protocol.line_too_long`. Header handling is
   described under "Headers" below; there are no header count or size caps beyond the line limit.
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

## Routing

A request is matched to an operation by its URL path alone, exactly as the validator matches it
(Humbug builds the same router; a test compares the two across many URL shapes, so the `operation`
in a result is present exactly when the request was routed and validated).

- **Host, scheme and port are not checked.** `servers` in the specification is not used to reject a
  request sent to a different host. (The library has a strict server-matching option; it is off.)
- **A server base path is optional.** With `servers: [https://api.example.test/v1]`, both
  `/v1/users` and `/users` match `/users`.
- **Trailing and doubled slashes are tolerated.** `/users/`, `//users` and `/users//` route to
  `/users`. This is the library's leniency; OpenAPI itself treats paths literally.
- **Case matters.** `/Users` does not match `/users` (`operation.not_found`).
- **A fragment is part of the path.** `/users#frag` is not routed. Fragments are never sent over
  HTTP, so clients should not include one in the effective URL.
- A matched path with no operation for the method is `operation.method_not_allowed`.

## Behavior notes

- Messages are generated from structure and never echo payload values.
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

Clients that capture compressed bodies must decode them first. There is no size limit on bodies
beyond the command line limit (256 MiB, provisional); an 8 MiB JSON body is validated and mapped in
well under a second.

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
- Non-JSON bodies are checked only when their schema is a string (see `diagnostics.md`, "Non-JSON
  bodies"). Form, XML and multipart bodies under object schemas stay `incomplete`; parsing them is
  deferred. Format validity (for example VCL syntax) is never checked.
- **Security requirements are deferred (decided).** `security.*` codes are in the taxonomy but are
  not produced, and security schemes are not checked.
- Untested: path styles other than simple, label and matrix.
- Repeated lines of an ordinary single-valued request header: the library validates only the first
  line (response headers: every line). Humbug reports the repetition (`header.repeated`, a
  warning) but does not validate the later request lines individually.
- Array and object header parameters sent empty are left to the library, which treats empty as absent.
- `Set-Cookie` response headers are validated per line by the library; Humbug does not parse cookie
  attributes.
- 3.0 is covered by one transaction test (`nullable`, boolean `exclusiveMinimum`) only.
