# pb33f OpenAPI Validator Spike — Requirements

**Status:** Draft 0.1  
**Project:** Language-neutral OpenAPI validation sidecar  
**Deliverable:** Standalone Go proof of concept and findings report

## 1. Purpose

Determine whether `github.com/pb33f/libopenapi` and `libopenapi-validator` can support a language-neutral HTTP transaction validator with comprehensive, structured, source-aware diagnostics. The spike is intended to reduce implementation risk, not build the production sidecar.

The intended production use is validating HTTP requests and responses during unit and integration tests. A Perl integration using `Mojo::Message::Request`, `Mojo::Message::Response`, and `Mojo::Transaction::HTTP` will be the first client, but **the Go validator must remain independent of Perl and testing frameworks**.

## 2. Scope

### In scope

- Build a small, standalone Go program using pb33f libraries directly.
- Load an OpenAPI specification from a local file.
- Construct or ingest representative HTTP requests and responses and validate them against the specification.
- Exercise request-only, response-only (with operation-identifying request context), and combined transaction validation.
- Inspect and document raw Go error types, fields, nested errors, and source-location metadata.
- Explore whether multiple independent violations are returned in a single validation pass.
- Identify where diagnostics must be normalized or supplemented by a future sidecar.
- Create reproducible fixtures, automated tests, and a concise findings report.

### Out of scope

- Perl bindings, Mojolicious adapters, Test2/TAP integration, or HTTP interception.
- Production NDJSON protocol implementation or persistent process management.
- Unix sockets, HTTP proxying, and network requests to target services.
- A production-grade source mapper, diagnostic formatter, or suppression engine.
- Packaging, installation, distribution, or compatibility guarantees.

## 3. Functional requirements

### R1. Specification loading

The program **MUST** load an OpenAPI specification from a local file and report parse, reference-resolution, and specification-validation errors separately from HTTP transaction violations. Record which OpenAPI versions the selected library versions actually support; do not assume support without testing.

### R2. Request validation

The program **MUST** validate Go `http.Request` objects against the matched OpenAPI operation, including applicable method/path matching, path and query parameters, headers, media type, and JSON request body constraints.

### R3. Response validation

The program **MUST** validate Go `http.Response` objects using sufficient originating request context to identify the operation, including status codes, response headers, media types, and JSON response bodies.

### R4. Independent validation

The program **MUST** demonstrate request-only, response-only, and combined request/response validation. It **MUST** record whether a bad request prevents response validation and whether each side's failures can be reported independently.

### R5. Multiple violations

The program **MUST** investigate whether all independently actionable violations can be collected per message and transaction. Document early termination, aggregation behavior, and any configuration affecting it.

### R6. Raw diagnostic inspection

For each failing fixture, the program **MUST** capture the concrete error types and inspect accessible fields, including:

- Machine-readable error category or keyword, if available.
- Human-readable message.
- Instance JSON Pointer or equivalent path.
- Schema JSON Pointer or equivalent reference.
- Source file, line, column, or byte offsets, if available.
- Nested errors, branch errors, and causal relationships.
- Request versus response provenance.

Missing fields **MUST** be recorded explicitly as unavailable rather than inferred from formatted strings.

### R7. Original payload preservation

Fixtures **MUST** preserve original HTTP body bytes. Investigate whether the validator consumes or modifies body readers, and demonstrate how request and response bodies can be safely replayed or restored. Source locations, if exposed, must be checked against the **original body**, not reserialized JSON.

### R8. Determinism

Repeated runs against identical fixtures **SHOULD** produce equivalent violation sets. Document ordering instability and unstable message wording separately from semantic results.

## 4. Required fixture matrix

| ID | Scenario | What to investigate |
|---|---|---|
| F01 | Valid request and response | Baseline success and operation matching |
| F02 | Wrong JSON property type | Instance path, schema path, value type, source position |
| F03 | Multiple independent invalid fields | Error aggregation and ordering |
| F04 | Missing required nested property | Parent location and absent-value reporting |
| F05 | Invalid query parameter | Parameter name, original URL, decoding |
| F06 | Missing or invalid required header | Header identification and repeated-value behavior |
| F07 | Unsupported request media type | Media type classification |
| F08 | Malformed JSON request body | Parser error and byte/line/column availability |
| F09 | Undocumented response status | Status diagnostic and operation context |
| F10 | Invalid JSON response body | Response-side schema errors |
| F11 | `oneOf` with zero matching branches | Nested branch errors and useful grouping |
| F12 | `oneOf` with multiple matching branches | Distinguish ambiguity from no-match |
| F13 | `anyOf` with no matching branches | Branch diagnostic representation |
| F14 | External `$ref` file | Schema origin, file path, line number |
| F15 | Unmatched method/path | Operation-not-found behavior |
| F16 | Invalid request plus invalid response | Independent results from one transaction |

Additional fixtures **MAY** cover cookies, arrays, `allOf`, `additionalProperties`, binary bodies, and OpenAPI 3.0 versus 3.1 differences.

## 5. Diagnostic mapping study

Create a table mapping each observed pb33f error to a proposed stable sidecar diagnostic code. Initial candidate namespaces include `operation.*`, `parameter.*`, `body.*`, `schema.*`, and `response.*`. These names are provisional and **MUST NOT** be treated as finalized by the spike.

For every fixture, document:

1. Expected validation outcome.
2. Actual library outcome.
3. Number and types of returned violations.
4. Available instance and schema locations.
5. Whether the error is independently actionable or plausibly cascading.
6. Whether normalization requires parsing human-readable text (undesirable).
7. What additional information a future source mapper would need.

The spike **SHOULD** specifically test whether type mismatches cause redundant nested required-property errors and how composition errors represent child failures. It **MUST NOT** suppress errors merely to make output look cleaner.

## 6. Implementation constraints

- Use Go and the pb33f libraries directly; avoid Perl-specific dependencies.
- Pin and record exact dependency versions in `go.mod` / `go.sum`.
- Prefer the public library API; document any requirement for unstable or internal APIs.
- Keep fixtures checked into the repository and runnable without external services.
- Avoid fetching remote references at test runtime; use local references where possible.
- Do not rely on ANSI terminal output as the source of diagnostic truth.
- Keep experimental code intentionally small and easy to discard or promote.

## 7. Suggested repository layout

```text
openapi-sidecar/
├── spike/
│   └── pb33f/
│       ├── main.go
│       ├── README.md
│       └── findings.md
├── testdata/
│   ├── specs/
│   │   ├── users.yaml
│   │   └── components.yaml
│   ├── cases/
│   └── expected/
├── go.mod
└── go.sum
```

This layout is illustrative; the spike need not establish the production repository structure.

## 8. Deliverables

1. **Runnable Go spike:** A documented command to execute individual fixtures or the full matrix.
2. **Automated fixture tests:** Each required case has an explicit expected outcome, with known limitations identified.
3. **Raw diagnostic samples:** Machine-readable captures or clearly documented error structures, not just prettified messages.
4. **Error mapping matrix:** Observed pb33f behavior versus the proposed sidecar taxonomy.
5. **Findings report:** Supported capabilities, gaps, workarounds, version information, and recommended architecture changes.
6. **Go/no-go recommendation:** Whether to use pb33f directly, wrap it with supplementary source mapping, or reconsider the validation engine.

## 9. Acceptance criteria

The spike is complete when:

- All F01–F16 fixtures have been exercised and documented, including unexpected behavior.
- Request-only, response-only, and combined validation have been demonstrated or their limitations explicitly identified.
- Actual diagnostic fields and nesting have been inspected rather than assumed.
- The team understands whether original payload line/column highlighting can come from pb33f or requires a separate source-mapping component.
- Multiple-error and composition behavior are characterized sufficiently to design conservative grouping rules.
- The findings report identifies concrete changes, if any, to the protocol and diagnostic taxonomy drafts.
- Another developer can reproduce the findings using the pinned dependencies and documented commands.

**Success does not require** pb33f to provide perfect source locations or complete error grouping. Discovering and documenting those gaps is a successful outcome of the spike.

## 10. Open questions for the findings report

- Can a response be validated independently when its corresponding request is invalid?
- Does pb33f preserve source metadata from the original OpenAPI file, including external references?
- Can JSON instance paths be obtained without parsing English error messages?
- Does the library expose enough structure to distinguish `oneOf` no-match from multiple-match failures?
- What are the consequences of reading or rewinding HTTP request/response bodies?
- Are validation errors stable and deterministic across repeated runs?
- Which diagnostic relationships are proven by library structure, and which would be speculative?
- Which parts of the proposed v0.1 protocol require revision based on actual library behavior?

## 11. Next step after the spike

Review the findings, revise the language-neutral protocol and diagnostic schema, and then implement the persistent stdin/stdout NDJSON sidecar. The first Perl consumer will adapt Mojolicious messages and transactions; it remains a separate project.
