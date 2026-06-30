// Copyright (c) the go-ruby-base64/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

// Package base64 is a pure-Go (no cgo), MRI-4.0.5-faithful reimplementation of
// Ruby's standard-library Base64 module — the deterministic, interpreter-
// independent core of `require "base64"`.
//
// It reproduces, byte-for-byte, what MRI's Base64 methods compute:
//
//   - Encode64        — RFC 2045 (pack("m")):  standard +/ alphabet, a newline
//     every 60 output characters and a trailing newline.
//   - Decode64        — lenient (unpack1("m")): every non-alphabet byte is
//     dropped, padding is optional, an orphaned final sextet is discarded.
//   - StrictEncode64  — pack("m0"): standard alphabet, no newlines.
//   - StrictDecode64  — unpack1("m0"): strict, returns ErrInvalid on any byte
//     that is not part of a well-formed padded base64 string.
//   - UrlsafeEncode64 — the -_ alphabet; padding is on by default and stripped
//     when padding=false.
//   - UrlsafeDecode64 — the -_ alphabet, RFC-4648; accepts correctly-padded or
//     unpadded input, rejects everything else.
//
// The hot standard-alphabet encode/decode paths run on the SIMD kernels of
// github.com/go-simd/base64 (go-asmgen: amd64 SSE/AVX2, arm64 NEON, ppc64le VSX,
// s390x vector; stdlib fallback elsewhere), so the output stays bit-identical to
// encoding/base64.StdEncoding while going faster on the supported arches. Only
// the MRI-specific framing (60-column wrapping, lenient filtering, the urlsafe
// padding rules) is hand-written here.
//
// It is the Base64 backend for github.com/go-embedded-ruby/ruby, but is a
// standalone, reusable module with no dependency on the Ruby runtime — a sibling
// of go-ruby-yaml/yaml, go-ruby-regexp/regexp and go-ruby-erb/erb.
package base64

import (
	"errors"
	"strings"

	simd "github.com/go-simd/base64"
)

// ErrInvalid is returned by StrictDecode64 and UrlsafeDecode64 for input that is
// not a well-formed base64 string. It mirrors the ArgumentError("invalid
// base64") MRI raises from the strict decoders.
var ErrInvalid = errors.New("invalid base64")

// lineLen is MRI's pack("m") line width.
const lineLen = 60

// Encode64 returns the RFC-2045 base64 encoding of s, matching MRI's
// Base64.encode64 (Array#pack("m")): the standard +/ alphabet with a newline
// inserted every 60 output characters and a trailing newline. The empty string
// encodes to the empty string (no trailing newline), exactly as MRI.
func Encode64(s string) string {
	if len(s) == 0 {
		return ""
	}
	return wrap60(simd.EncodeToString([]byte(s)))
}

// StrictEncode64 returns the base64 encoding of s with no newlines, matching
// MRI's Base64.strict_encode64 (Array#pack("m0")).
func StrictEncode64(s string) string {
	return simd.EncodeToString([]byte(s))
}

// Decode64 decodes a base64 string leniently, matching MRI's Base64.decode64
// (String#unpack1("m")). It mirrors the C unpack("m") loop exactly: bytes outside
// the standard alphabet are skipped; characters are gathered into 4-char quads;
// a '=' that lands on a 2- or 3-char partial quad finalises that quad and stops
// decoding (trailing padding terminates the stream), while a '=' on a quad
// boundary is ignored; and a lone trailing sextet at end-of-input is discarded.
func Decode64(s string) string {
	out := make([]byte, 0, len(s)*3/4)
	var quad [4]byte
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '=' {
			// Padding finalises a partial quad of 2 or 3 chars and stops; on a
			// quad boundary (n==0) it is extraneous and ignored.
			if n >= 2 {
				out = appendQuad(out, quad, n)
				return string(out)
			}
			continue
		}
		v := stdVal(c)
		if v < 0 {
			continue // non-alphabet byte: skip
		}
		quad[n] = byte(v)
		n++
		if n == 4 {
			out = appendQuad(out, quad, 4)
			n = 0
		}
	}
	// End of input: a 2- or 3-char remainder yields whole bytes; a single
	// orphaned sextet cannot form a byte and is discarded.
	if n >= 2 {
		out = appendQuad(out, quad, n)
	}
	return string(out)
}

// appendQuad decodes the first n (2..4) 6-bit values of a quad into 1..3 bytes.
func appendQuad(dst []byte, q [4]byte, n int) []byte {
	dst = append(dst, q[0]<<2|q[1]>>4)
	if n >= 3 {
		dst = append(dst, q[1]<<4|q[2]>>2)
	}
	if n == 4 {
		dst = append(dst, q[2]<<6|q[3])
	}
	return dst
}

// stdVal maps a standard base64 alphabet byte to its 6-bit value, or -1.
func stdVal(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 26
	case c >= '0' && c <= '9':
		return int(c-'0') + 52
	case c == '+':
		return 62
	case c == '/':
		return 63
	}
	return -1
}

// StrictDecode64 decodes a strictly-padded standard base64 string, matching
// MRI's Base64.strict_decode64 (String#unpack1("m0")). It returns ErrInvalid for
// any input that is not a well-formed padded base64 string (stray characters,
// whitespace, wrong padding).
func StrictDecode64(s string) (string, error) {
	// encoding/base64.StdEncoding (and thus the SIMD fast path) tolerates embedded
	// newlines, but MRI's unpack("m0") does not. Reject any byte that is not an
	// alphabet character or '=' before decoding so strictness matches MRI.
	for i := 0; i < len(s); i++ {
		if c := s[i]; stdVal(c) < 0 && c != '=' {
			return "", ErrInvalid
		}
	}
	out, err := simd.DecodeString(s)
	if err != nil {
		return "", ErrInvalid
	}
	return string(out), nil
}

// UrlsafeEncode64 returns the url-safe (-_) base64 encoding of s, matching MRI's
// Base64.urlsafe_encode64. Padding is included by default; when padding is false
// the trailing = characters are stripped. The variadic padding argument lets the
// Go caller mirror Ruby's `padding:` keyword (default true).
func UrlsafeEncode64(s string, padding ...bool) string {
	pad := true
	if len(padding) > 0 {
		pad = padding[0]
	}
	str := simd.EncodeToString([]byte(s))
	if !pad {
		// MRI: str.chomp!("==") or str.chomp!("=")
		if strings.HasSuffix(str, "==") {
			str = str[:len(str)-2]
		} else if strings.HasSuffix(str, "=") {
			str = str[:len(str)-1]
		}
	}
	return strings.Map(stdToURL, str)
}

// UrlsafeDecode64 decodes a url-safe (-_) RFC-4648 base64 string, matching MRI's
// Base64.urlsafe_decode64: correctly-padded or unpadded input is accepted, and
// anything else (stray characters, wrong padding) returns ErrInvalid.
func UrlsafeDecode64(s string) (string, error) {
	// MRI re-pads unpadded input to a multiple of 4 before strict-decoding so the
	// standard padded decoder accepts it; already-padded input is decoded as-is.
	var std string
	if !strings.HasSuffix(s, "=") && len(s)%4 != 0 {
		s += strings.Repeat("=", (4-len(s)%4)%4)
		std = strings.Map(urlToStd, s)
	} else {
		std = strings.Map(urlToStd, s)
	}
	// MRI's urlsafe_decode64 delegates to strict_decode64, so the same
	// no-stray-bytes strictness (including newline rejection) applies.
	return StrictDecode64(std)
}

// wrap60 inserts a newline every 60 columns and appends a trailing newline,
// matching MRI's pack("m") line wrapping.
func wrap60(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/lineLen + 1)
	for len(s) > lineLen {
		b.WriteString(s[:lineLen])
		b.WriteByte('\n')
		s = s[lineLen:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}

// stdToURL maps the standard +/ alphabet onto the url-safe -_ alphabet.
func stdToURL(r rune) rune {
	switch r {
	case '+':
		return '-'
	case '/':
		return '_'
	}
	return r
}

// urlToStd maps the url-safe -_ alphabet back onto the standard +/ alphabet.
func urlToStd(r rune) rune {
	switch r {
	case '-':
		return '+'
	case '_':
		return '/'
	}
	return r
}
