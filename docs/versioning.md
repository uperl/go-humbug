# Versioning and compatibility

What a client can rely on, and what may change under it.

## Status

**Unstable until the first tag.** Until then any part of the protocol or the diagnostics may change
incompatibly, with a note in the release notes and no change to `protocol_version`. From the first
tag the rules below apply.

## What is the contract

The contract is the **wire shape** and the **code taxonomy**:

- the commands, the fields of every message, their types, and the enumerations
  (`schema/protocol.schema.json`, `schema/diagnostic.schema.json`);
- the diagnostic and error **codes** and what each one means (`docs/diagnostics.md`);
- the meaning of `valid`, `complete` and `has_violations`.

**Which findings Humbug reports for a given input is not part of the contract.** That depends on the
pinned validation library and on Humbug's workarounds for it (`protocol.md`, "Known library
limitations"). It can change in any release: a library upgrade may find more, or fewer, or
different violations, and a workaround may be added or removed. Changes of this kind are listed in
the release notes. Clients must not treat a fixed answer for fixed input as guaranteed, and tests
that hand Humbug a message and compare the whole reply will break.

Messages (`message` fields) are for people and may change at any time. Match on `code`.

## Breaking and non-breaking changes

Not breaking, and made in any release:

- a new optional field in a message, or a new key in `detail`;
- a new diagnostic or error code;
- a new `limits` entry, or a new value for the `validation.incomplete` reasons;
- a new `severity: warning` or `info` diagnostic (a warning never changes `valid`);
- reporting more, or fewer, findings for the same input (see above).

Breaking, and made only with a new `protocol_version`:

- removing or renaming a command, field, code or enumeration value;
- changing a field's type, or a code's meaning;
- making an optional field required, or a required field optional in a reply (`span.part` was made
  required before the first tag; after it, that would be a break);
- changing what `valid` or `complete` means, or the rule that diagnostic ids are unique within a transaction.

A client must therefore **ignore fields it does not know**, and **handle a code it does not know by
its `severity`**, not by failing. A client that does both survives every non-breaking change.

## How a client finds out

- `initialize` takes `protocol_version` (an integer, now `1`). A server that does not speak that
  version replies `protocol.unsupported_version`. The version changes only for a breaking change.
- `initialize` replies with `implementation`, which says which build answered:

      "implementation": {"name": "humbug", "version": "v0.1.0",
                         "libraries": {"libopenapi": "v0.41.6", "libopenapi_validator": "v0.16.1"}}

  `version` is the release, or `(devel)` or a pseudo-version for a build from a working tree. The
  block is for support and bug reports. It is optional, and a client must not branch on it.
  Because the reported findings depend on the library versions, include the block when reporting a
  difference in findings.

## Tests that guard this

- `TestCodesArePinnedAndDocumented` fails when a code is added, removed or renamed, and when a code
  is not described in `diagnostics.md`. Adding one means adding it to the pinned list and the docs;
  removing or renaming one means a new `protocol_version`.
- The schema tests validate every reply against the schemas, so an undeclared or mistyped field
  fails.
- The library behaviour tables (`TestParameterLibraryBehavior`, `TestXMLFormLibraryBehavior`) fail
  when a library upgrade changes what is reported. That is the signal to review the change and write
  it up in the release notes, not a promise to clients.

## Not decided

- Whether a server may serve two protocol versions at once, after the first break.
- A release-notes format. Until the first tag, the commit history is the record.
