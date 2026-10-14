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
| `parameter.invalid` | Parameter present but fails its schema (including array items) | `location`, `name` |
| `body.required` | Operation requires a body and none was supplied (absent, not empty) | `body` |
| `body.decode` | Body present but not decodable (malformed JSON, empty, or corrupt gzip) | `body`, zero-width `span` at the parse error |
| `body.media_type` | Content-Type not defined for the operation | `header` `Content-Type` |
| `response.status` | Status code not defined for the operation | `status` |
| `response.header.required` | Required response header missing | `header`, `name` |
| `response.header.invalid` | Response header fails its schema | `header`, `name` |
| `schema.<keyword>` | Body fails a JSON Schema keyword: `type`, `required`, `enum`, `format`, `pattern`, `minimum`, `minLength`, … (`additionalProperties` is `schema.additional_properties`) | `body`, `pointer`, `span` |
| `schema.oneOf.no_match` | No `oneOf` branch matched; branch failures are `children` | `body`, `pointer` |
| `schema.oneOf.ambiguous` | More than one `oneOf` branch matched | `body`, `pointer` |
| `schema.anyOf` | No `anyOf` branch matched; branch failures are `children` | `body`, `pointer` |
| `validation.incomplete` | A requested check could not run; `reason` says why (info) | varies |
| `validation.unclassified` | The library reported something Humbug does not classify; original `validation_type`/`validation_sub_type` in `detail` | none |

`validation.incomplete` reasons: `body.not_inspected` (a body is present but its media type is not
JSON, so no schema check ran; `detail.media_type` has the type), `body.content_encoding_unsupported`
and `body.too_large` (see `protocol.md`, `detail.content_encoding`), `operation.not_found` and
`operation.method_not_allowed` (response side could not be routed).

Only JSON bodies are schema-validated. Any other body — text, binary, form, XML — makes the side
`incomplete`, never `valid`, even if the library accepted it.

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

Rule 2 is defensive. JSON Schema ignores object and array keywords on values of the wrong type, so
pb33f v0.16.1 does not emit such cascades (probed: string, array and null where an object is
expected, including inside `allOf` and `oneOf`). The rule is covered by unit tests, not by a
fixture, and exists so a library change or a different validator cannot reintroduce noisy output.
Rule 1 is exercised end to end by fixture F34.

`schema.type` diagnostics carry `expected` (an array of JSON type names) and `actual` (the type
name found, never the value). They are omitted when several type constraints apply at one location
(for example across composition branches), because choosing one would be a guess.

## Locations

- Body diagnostics carry an RFC 6901 `pointer` and, when the body parses, a `span` (zero-based byte
  offsets, end exclusive; one-based line and column in UTF-8 bytes). `contextual: true` marks the
  nearest existing ancestor (used for missing properties); `ambiguous: true` marks duplicate keys.
- Header, query, cookie and path diagnostics identify the parameter by `location` and `name` only;
  no wire positions are invented.
- `schema.pointer` is relative to the root schema of the validated body. `schema.line`/`column`
  locate that root schema in the specification when the library resolves it; the source file is not
  exposed by the library and is omitted.
- Messages never contain body or parameter values.
