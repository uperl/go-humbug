// Package limits holds the resource limits that bound what one command can cost.
//
// They exist because the memory a validation takes is a multiple of the input, not a constant:
// measured with the pinned library, about 11 to 12 times the body size for a flat document and
// about 70 times for a structured one (a 19 MB array of small objects peaked at 1.4 GB). The line
// limit alone is therefore not a memory bound. Each limit is checked before the work it guards.
package limits

import (
	"flag"
	"fmt"
)

// NestingCeiling is the deepest JSON nesting the toolchain can handle at all: the limit of Go's
// JSON decoder (used by the validation library) and of the source-map parser. MaxNestingDepth may
// not exceed it.
const NestingCeiling = 10000

// Limits is the effective resource policy. The JSON tags are the names reported by `initialize`.
type Limits struct {
	// MaxLineBytes bounds one command line, including base64 expansion of both bodies. An
	// over-long line is answered with protocol.line_too_long and skipped.
	MaxLineBytes int `json:"max_line_bytes"`
	// MaxBodyBytes bounds one decoded message body. A larger body is not inspected: its side is
	// `incomplete` (reason body.too_large), never valid.
	MaxBodyBytes int `json:"max_body_bytes"`
	// MaxNestingDepth bounds the nesting of a JSON body. A deeper body is not inspected: its side
	// is `incomplete` (reason body.too_deep).
	MaxNestingDepth int `json:"max_nesting_depth"`
	// MaxHeaders, MaxHeaderValueBytes and MaxHeaderBytes bound the header list of one message
	// (entries; bytes per decoded value; bytes of all names and values together). Exceeding one is
	// an input error (validator.unsupported_input).
	MaxHeaders          int `json:"max_headers"`
	MaxHeaderValueBytes int `json:"max_header_value_bytes"`
	MaxHeaderBytes      int `json:"max_header_bytes"`
}

// Default returns the default limits. A decoded body is 16 MiB at most, so a command carrying a
// request and a response body of that size (about 43 MiB of base64) fits in the 64 MiB line limit.
func Default() Limits {
	return Limits{
		MaxLineBytes:        64 << 20,
		MaxBodyBytes:        16 << 20,
		MaxNestingDepth:     1000,
		MaxHeaders:          1000,
		MaxHeaderValueBytes: 64 << 10,
		MaxHeaderBytes:      1 << 20,
	}
}

// Validate reports an unusable configuration. There is no "unlimited": every limit must be a
// positive number, and the nesting limit cannot exceed what the toolchain supports.
func (l Limits) Validate() error {
	for _, f := range []struct {
		name string
		v    int
	}{
		{"max-line-bytes", l.MaxLineBytes}, {"max-body-bytes", l.MaxBodyBytes},
		{"max-nesting-depth", l.MaxNestingDepth}, {"max-headers", l.MaxHeaders},
		{"max-header-value-bytes", l.MaxHeaderValueBytes}, {"max-header-bytes", l.MaxHeaderBytes},
	} {
		if f.v < 1 {
			return fmt.Errorf("--%s must be at least 1 (there is no unlimited setting), got %d", f.name, f.v)
		}
	}
	if l.MaxNestingDepth > NestingCeiling {
		return fmt.Errorf("--max-nesting-depth cannot exceed %d, got %d", NestingCeiling, l.MaxNestingDepth)
	}
	return nil
}

// Bind registers one command-line flag per limit on fs, with the current values as defaults.
func Bind(fs *flag.FlagSet, l *Limits) {
	fs.IntVar(&l.MaxLineBytes, "max-line-bytes", l.MaxLineBytes, "largest command line accepted, in bytes")
	fs.IntVar(&l.MaxBodyBytes, "max-body-bytes", l.MaxBodyBytes, "largest decoded message body inspected, in bytes")
	fs.IntVar(&l.MaxNestingDepth, "max-nesting-depth", l.MaxNestingDepth, fmt.Sprintf("deepest JSON body inspected (at most %d)", NestingCeiling))
	fs.IntVar(&l.MaxHeaders, "max-headers", l.MaxHeaders, "most header entries in one message")
	fs.IntVar(&l.MaxHeaderValueBytes, "max-header-value-bytes", l.MaxHeaderValueBytes, "largest decoded header value, in bytes")
	fs.IntVar(&l.MaxHeaderBytes, "max-header-bytes", l.MaxHeaderBytes, "largest total of all header names and values in one message, in bytes")
}
