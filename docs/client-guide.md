# Writing a Humbug client

What a program that drives Humbug (or any implementation of the protocol) must get right. The
worked example is `examples/python/client.py`; the rules and their reasons are in `protocol.md`.

## Process

- Start `humbug` once and keep it running: loading a specification is the expensive step.
- One JSON object per line on stdin; one reply per line on stdout, in order, with the same `id`.
  stdout carries replies only; library logging goes to stderr.
- Send `initialize` first, then any number of `validate`, then `shutdown` (or close stdin). A failed
  `initialize` may be retried.
- A dead process is a failed transaction. Restart it; do not assume the command ran.
- Keep reading stdout. If the client stops, the next write fails and the process exits.

## Send the message as captured

Humbug judges what was sent, so do not tidy it first.

**Headers: an ordered list of entries, never a dict.** `"headers": [["Content-Type","application/json"],["Set-Cookie","a=1"],["Set-Cookie","b=2"]]`.

- Keep the captured order, and keep repeated headers as separate entries. A dict silently drops
  repeats, and Humbug reports a repeated single-valued header (`header.repeated`).
- Do not join lines with commas, lower-case names, or trim values.
- A value that is not valid UTF-8 (or that JSON cannot carry) goes as `[name, base64_of_bytes, true]`.
  Names are never encoded. You may base64 any value.
- A header Humbug cannot treat as HTTP (bad name, control characters in the value) is an `error`
  reply, `validator.unsupported_input`, not a diagnostic.
- `Host` is ignored; the URL decides the route.

**URL.** Send an absolute URL (`http://host/path?query`). Scheme, host and port are not checked.
Send the path and query as they appeared on the wire.

**Body: `body_base64` is the already-decoded body.**

- Decode `Content-Encoding` (gzip, br, ...) and transfer encoding yourself first. Humbug ignores
  `Content-Encoding`, so compressed bytes are reported as a malformed body.
- Leave the bytes alone otherwise: no re-serialising JSON, no re-encoding form data. Spans point into
  the bytes you send, so a client that pretty-prints JSON gets spans for text nobody sent.
- Omit `body_base64` for no body. An empty body (`""`) is a body, and is not the same thing.
- Send `Content-Type` as captured. XML and form bodies are validated by type; see
  `diagnostics.md`, "Non-JSON bodies".

**Response.** Send `status` and, when there is one, the same `headers` and `body_base64`. The
response is validated only when supplied, or when `validate.response` says so.

## Limits

`initialize` reports the limits in effect (`limits`: line, body, nesting depth, header counts and
sizes). The field is optional, so treat a missing one as unknown. Check against them before sending
when you can:

- A body over `max_body_bytes` is not an error: that side comes back `incomplete` (reason
  `body.too_large`). Decide whether to send it at all.
- A line over `max_line_bytes` gets `protocol.line_too_long` with a null `id`; the line is skipped.
- Header limits give `validator.unsupported_input`.

## Reading the result

- **Two kinds of reply.** `type: "error"` means the command could not run (bad input, protocol misuse);
  there is no verdict. `type: "result"` always has one.
- **`valid` is `complete && !has_violations`.** Check `complete` as well. An `incomplete` side (body too
  large, an unsupported media type, an XML root without `xml.name`, multipart) is not valid, and not a
  violation either; it says what Humbug could not check, and why (`validation.incomplete`, `reason`).
- **Warnings do not make a result invalid.** `header.repeated` and `body.undeclared` are warnings;
  look at `severity`.
- **Hide `suppressed_by` diagnostics** in a display. They are consequences of another finding and stay
  in the result so nothing is lost.
- **Do not parse messages.** Use `code`, `instance`, `schema`, `detail`. Messages are for humans and may
  change. Diagnostics never contain payload or header values, so they are safe to log.
- **Spans** (`instance.span`) are line, column and byte offsets into the body you sent, and `part`
  says whether they cover the member name (`key`), a value, or a position (`position`). XML and form
  findings have a `pointer` but no span.
- **Unknown codes happen.** Handle a code you do not know by its `severity`, not by failing. Codes are listed in `diagnostics.md`.

## Checklist

1. Headers as an ordered list of entries, repeats and order kept, bytes base64.
2. Absolute URL; body already decoded; no re-serialising.
3. Read `limits` from `initialize`; handle `incomplete`, `error` and a dead process.
4. Decide on `valid`, not on `has_violations` alone.
