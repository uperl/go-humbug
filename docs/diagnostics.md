# Humbug diagnostics — as implemented

Normative shape: `schema/diagnostic.schema.json` (tests validate every reply against it).
Design background: `openapi-sidecar-diagnostics-v0.1.md`. Codes below are stable within protocol
v1; `message` text is not and must never be matched programmatically.

## Result shape

A `validate` result has `request` and `response` sides plus transaction-level `complete`,
`has_violations`, `valid` (see `protocol.md`). Each side has `diagnostics`, a flat array in
deterministic order: location (operation, path, query, header, cookie, body, status), then name,
instance pointer, schema pointer, code. `id`s (`d1`…) are assigned after sorting, request side first.

`severity: error` diagnostics are contract violations and make `has_violations` true. `info`
diagnostics (`validation.incomplete`) explain why a side is `complete: false` and are not violations.

## Code catalog

| Code | Meaning | Instance |
| --- | --- | --- |
| `operation.not_found` | No path in the specification matches | `operation` |
| `operation.method_not_allowed` | Path matches, method has no operation | `operation` |
| `parameter.required` | Required query/header/path/cookie parameter missing | `location`, `name` |
| `parameter.invalid` | Parameter present but fails its schema (including array items). `detail.empty` is `"true"` when a header parameter was sent empty | `location`, `name` |
| `body.required` | Operation requires a body and none was supplied (absent, not empty) | `body` |
| `body.decode` | Body present but not decodable (malformed JSON, or empty) | `body`, zero-width `span` at the parse error |
| `body.undeclared` | A body was sent where the matched operation declares no content for it: a request with no `requestBody`, or a response (such as a `204`) whose definition has no content. **Warning**, not a violation; `detail.media_type` has the sent type when there was one | `body` |
| `body.media_type` | Content-Type not defined for the operation | `header` `Content-Type` |
| `response.status` | Status code not defined for the operation | `status` |
| `response.header.required` | Required response header missing | `header`, `name` |
| `response.header.invalid` | Response header is present but fails its schema | `header`, `name` |
| `header.repeated` | A single-valued header (or `Content-Type`) was sent on more than one line. **Warning**, not a violation; `detail.lines` has the count | `header`, `name` |
| `schema.<keyword>` | Body fails a JSON Schema keyword: `type`, `required`, `enum`, `format`, `pattern`, `minimum`, `minLength`, … (`additionalProperties` is `schema.additional_properties`) | `body`, `pointer`, `span` |
| `schema.oneOf.no_match` | No `oneOf` branch matched; branch failures are `children` | `body`, `pointer` |
| `schema.oneOf.ambiguous` | More than one `oneOf` branch matched | `body`, `pointer` |
| `schema.anyOf` | No `anyOf` branch matched; branch failures are `children` | `body`, `pointer` |
| `validation.incomplete` | A requested check could not run; `reason` says why (info) | varies |
| `validation.unclassified` | The library reported something Humbug does not classify; original `validation_type`/`validation_sub_type` in `detail` | none |

`validation.incomplete` reasons: `body.not_inspected` (a non-JSON body whose schema Humbug cannot
check as a string, or a body with no usable Content-Type; `detail.media_type` has the type),
`body.charset_unsupported` (a text body declared in a charset other than UTF-8 or US-ASCII;
`detail.charset`), `operation.not_found` and `operation.method_not_allowed` (response side could
not be routed).

## Undeclared and absent bodies

- **Undeclared bodies are a warning** (`body.undeclared`). It is only raised when the operation was
  matched and the message's definition exists but declares no content, and the body is non-empty.
  An empty or absent body is not a body. A response status the operation does not define at all
  is reported as `response.status` instead, because whether a body is declared is unknowable.
  The warning does not affect `valid` or `has_violations`.
- **A response with no body and no `Content-Type` is valid.** There is no media type to check,
  so the library's "media type" finding for it is dropped. A body sent without a `Content-Type`,
  or a `Content-Type` the operation does not declare, is still reported.
- **An empty body with no `Content-Type` is not "uninspected"** (there is nothing to inspect). With
  a `Content-Type`, an empty body is validated like any other (an empty string can violate
  `minLength`).
- A missing response body is never a finding: OpenAPI has no "required response body", unlike a
  required request body (`body.required`).

## Non-JSON bodies

JSON bodies (`application/json` and `+json`) are checked by the library, with pointers and spans.
Every other body is handled by the shape of the schema declared for its media type, not by the
media type's name. Matching is exact first, then `type/*`, then `*/*`; media types and charsets are
case-insensitive.

| Schema declared for the media type | Result |
| --- | --- |
| Media type not declared for the operation | `body.media_type` only; nothing else to check |
| Declared with no schema | Any content conforms: valid and complete |
| `type: string` (or untyped and unstructured) | The body is a string and the string keywords (`minLength`, `maxLength`, `pattern`, `enum`, …) apply; violations are `schema.<keyword>` |
| `type: string, format: binary` | Raw bytes: length keywords count bytes, not characters |
| Object, array or composition (form, XML, multipart) | Not parsed: `incomplete`, reason `body.not_inspected` |

Text bodies must be valid UTF-8 (`body.decode`, `detail.utf8_offset` is the first bad byte); a
charset other than UTF-8 or US-ASCII is `incomplete`. These diagnostics have no `pointer` or `span`:
the body is not parsed as JSON even when it happens to look like JSON.

Humbug does not check that content is valid for its format. For `application/vcl` with a string
schema, "valid" means a UTF-8 string within the schema's limits, not syntactically valid VCL.

## Composition

A `oneOf`/`anyOf` failure is one parent (`children` list) and one diagnostic per failing branch
keyword (`parent`, `branch` index). The library repeats the whole cause tree for every leaf; Humbug
emits each distinct finding once. Only the first composition level is grouped. `allOf` failures are
ordinary `schema.*` diagnostics: each failing branch keeps its own finding.

## Relationships and suppression

Nothing is ever removed. A diagnostic that is a demonstrable consequence of another carries
`caused_by` (the primary's ID) and `suppressed_by` (the primary that should replace it in normal
presentation). Suppressed diagnostics keep every field, still appear in `diagnostics`, and the
primary is always an error, so `has_violations` and `valid` are unaffected. A client that wants
everything can ignore `suppressed_by`; a formatter should hide diagnostics where it is set.
`related` is unused in v1.

Only two rules exist. Sharing a path or similar wording never suppresses anything.

1. **Routing.** If the operation cannot be matched, the response side's `validation.incomplete`
   note (reason `operation.not_found` or `operation.method_not_allowed`) is caused by the request
   side's finding of the same code. With request validation disabled there is no primary, so the
   note is not suppressed.
2. **Wrong type.** When a body value has the wrong type (`schema.type` with `expected` and
   `actual`), object keywords at the same pointer (`required`, `additional_properties`,
   `minProperties`, …) are suppressed if an object was expected, and array keywords (`items`,
   `minItems`, …) if an array was expected, along with any finding below that pointer. Sibling
   properties, other keywords at the pointer, and other `oneOf`/`anyOf` branches are never touched.

Rule 2 is defensive, and kept deliberately (decided). JSON Schema ignores object and array
keywords on values of the wrong type, so
pb33f v0.16.1 does not emit such cascades (probed: string, array and null where an object is
expected, including inside `allOf` and `oneOf`). The rule is covered by unit tests, not by a
fixture, and exists so a library change or a different validator cannot reintroduce noisy output.
Rule 1 is exercised end to end by fixture F34.

`schema.type` diagnostics carry `expected` (an array of JSON type names) and `actual` (the type
name found, never the value). They are omitted when several type constraints apply at one location
(for example across composition branches), because choosing one would be a guess.

## Locations

- Body diagnostics carry an RFC 6901 `pointer` and, when the body parses, a `span` (zero-based byte
  offsets, end exclusive; one-based line and column in UTF-8 bytes). **The span highlights the
  member name** when the pointer addresses an object member (`part: "key"`, quotes included), and
  the value where there is no name: the root and array elements (`part: "value"`). A parse error
  is a zero-width `part: "position"`. `contextual: true` marks the nearest existing ancestor, used
  for missing properties (so `required` highlights the parent's name, or the whole document at the
  root); `ambiguous: true` marks duplicate keys, where the last occurrence is reported.
- **`additionalProperties`** gets one finding per rejected property, pointing at that member, so the
  highlight is the offending name. When the rejected names cannot be attributed with certainty
  (inside `oneOf`/`anyOf`, or two closed schemas applying to one object), Humbug does not guess:
  it reports one finding per constraint for the object.
- The value's own span is not reported. A consumer that wants it can parse the body at `pointer`.
- Header, query, cookie and path diagnostics identify the parameter by `location` and `name` only;
  no wire positions are invented.
- `schema.pointer` is relative to the root schema of the validated body. `schema.line`/`column`
  locate that root schema in the specification when the library resolves it; the source file is not
  exposed by the library and is omitted.
- Messages never contain body or parameter values.
