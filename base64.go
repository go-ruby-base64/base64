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

// decStdLUT maps every byte to its standard-alphabet 6-bit value, or 0xFF for any
// non-alphabet byte. It is the table form of stdVal (the single source of truth),
// letting Decode64 classify a byte with one indexed load instead of a branch
// cascade — the same 256-entry table MRI's C unpack("m") uses.
var decStdLUT = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = 0xFF
	}
	for c := 0; c < 256; c++ {
		if v := stdVal(byte(c)); v >= 0 {
			t[c] = byte(v)
		}
	}
	return t
}()

// Decode64 decodes a base64 string leniently, matching MRI's Base64.decode64
// (String#unpack1("m")). It reproduces the C unpack("m") semantics exactly: bytes
// outside the standard alphabet are dropped; the surviving characters are gathered
// into 4-char quads; a '=' that lands on a 2- or 3-char partial quad finalises that
// quad and stops decoding (trailing padding terminates the stream), while a '=' on
// a quad boundary is ignored; and a lone trailing sextet at end-of-input is
// discarded.
//
// Rather than fold sextets one byte at a time (the shape of MRI's C loop, but slow
// in Go), it de-spaces the input with github.com/go-simd/base64's vectorised
// Compact kernel — a branch-free SWAR pass that copies the alphabet bytes, drops
// whitespace/newlines/stray bytes, and applies the very same '='-on-a-partial-quad
// stop rule MRI's C loop uses — then hands the packed alphabet run to that package's
// batched SIMD decoder. Both passes run over a single mutable copy of the input:
// Compact de-spaces in place and Decode decodes in place (its documented in-place
// contract — the write cursor always trails the read cursor), so the whole decode
// costs one buffer for the working copy plus the result string, matching the
// allocation profile of MRI's C unpack("m") instead of the extra scratch+output
// buffers a naive Go port needs. Only the 2/3-char tail is finished by hand. The
// result is byte-identical to the previous decoder, taking the lenient path from
// behind MRI to ahead of it (including YJIT).
func Decode64(s string) string {
	if len(s) == 0 {
		return ""
	}
	// One mutable working buffer holds the input, the compacted alphabet run, and
	// the decoded bytes in turn — no scratch, no separate output allocation.
	buf := []byte(s)
	// Pass 1: SIMD de-space in place. Compact drops every non-alphabet byte and
	// honours the '='-partial-quad stop, returning the kept count. dst==src is safe:
	// its write cursor never runs ahead of its read cursor, so a byte is read before
	// it is overwritten (whole-window moves load all 8 bytes before storing).
	m := simd.Compact(buf, buf)
	// Pass 2: batch-decode the clean bytes in place (buf is a prefix of buf — the
	// documented in-place Decode contract). Whole quads go through the SIMD kernel;
	// a 2- or 3-char remainder yields whole bytes by hand (a lone orphaned sextet
	// cannot form a byte and is discarded). The 3*full/4 decoded bytes never reach
	// buf[full], so the tail sextets stay intact until read below.
	full := m - m%4
	di := 0
	if full > 0 {
		n, _ := simd.Decode(buf, buf[:full])
		di = n
	}
	if rem := m - full; rem >= 2 {
		var acc uint32
		for k := full; k < m; k++ {
			acc = acc<<6 | uint32(decStdLUT[buf[k]])
		}
		di += flushPartial(buf[di:], acc, rem)
	}
	return string(buf[:di])
}

// flushPartial writes the whole bytes carried by a 2- or 3-sextet partial quad
// (acc holds n 6-bit groups, most-significant first) and returns the byte count:
// 2 sextets carry 1 byte, 3 sextets carry 2. The orphaned low bits are dropped,
// exactly as MRI discards an incomplete final group.
func flushPartial(dst []byte, acc uint32, n int) int {
	if n == 2 {
		dst[0] = byte(acc >> 4) // q0<<2 | q1>>4
		return 1
	}
	dst[0] = byte(acc >> 10) // q0<<2 | q1>>4
	dst[1] = byte(acc >> 2)  // q1<<4 | q2>>2
	return 2
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
	// alphabet character or '=' before decoding so strictness matches MRI. The
	// decStdLUT table classifies each byte with one load (no branch cascade), which
	// keeps this pre-scan from dominating the decode.
	for i := 0; i < len(s); i++ {
		if c := s[i]; decStdLUT[c] == 0xFF && c != '=' {
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
