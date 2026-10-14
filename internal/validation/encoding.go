package validation

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
)

// MaxDecodedBodyBytes caps the size of a gzip body after decompression, so a
// small compressed payload cannot exhaust memory (provisional default).
const MaxDecodedBodyBytes = 64 << 20

type bodyIssueKind int

const (
	issueUnsupportedEncoding bodyIssueKind = iota + 1 // e.g. br, deflate, or stacked encodings
	issueTooLarge                                     // decoded size exceeds MaxDecodedBodyBytes
	issueCorrupt                                      // declared gzip but not decodable
)

type bodyIssue struct {
	kind     bodyIssueKind
	encoding string
}

// resolveBody applies Content-Encoding to a message body. Spans and JSON checks
// refer to the decoded bytes, matching the protocol's "decoded body" contract.
// identity/absent encodings return the body unchanged; gzip is decompressed;
// anything else returns an issue and the body is left untouched.
func resolveBody(headers []Header, body *[]byte) (*[]byte, *bodyIssue) {
	if body == nil {
		return nil, nil
	}
	var encs []string
	for _, h := range headers {
		if strings.EqualFold(h[0], "content-encoding") {
			for _, e := range strings.Split(h[1], ",") {
				if e = strings.ToLower(strings.TrimSpace(e)); e != "" && e != "identity" {
					encs = append(encs, e)
				}
			}
		}
	}
	switch {
	case len(encs) == 0:
		return body, nil
	case len(encs) == 1 && (encs[0] == "gzip" || encs[0] == "x-gzip"):
		zr, err := gzip.NewReader(bytes.NewReader(*body))
		if err != nil {
			return body, &bodyIssue{issueCorrupt, "gzip"}
		}
		out, err := io.ReadAll(io.LimitReader(zr, MaxDecodedBodyBytes+1))
		if err != nil {
			return body, &bodyIssue{issueCorrupt, "gzip"}
		}
		if len(out) > MaxDecodedBodyBytes {
			return body, &bodyIssue{issueTooLarge, "gzip"}
		}
		return &out, nil
	default:
		return body, &bodyIssue{issueUnsupportedEncoding, strings.Join(encs, ", ")}
	}
}
