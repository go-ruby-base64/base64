// Copyright (c) the go-ruby-base64/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

package base64

import (
	stdbase64 "encoding/base64"
	"testing"
)

// scalarStrictEncode64 is the encoding/base64 (scalar) reference the production
// StrictEncode64 replaces with the go-simd kernel; benchmarking the two side by
// side reports the SIMD speedup on the running arch.
func scalarStrictEncode64(s string) string {
	return stdbase64.StdEncoding.EncodeToString([]byte(s))
}

func benchInput(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*131 + 7)
	}
	return string(b)
}

func BenchmarkStrictEncode64SIMD(b *testing.B) {
	in := benchInput(4096)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = StrictEncode64(in)
	}
}

func BenchmarkStrictEncode64Scalar(b *testing.B) {
	in := benchInput(4096)
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = scalarStrictEncode64(in)
	}
}

func BenchmarkStrictDecode64SIMD(b *testing.B) {
	in := StrictEncode64(benchInput(4096))
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_, _ = StrictDecode64(in)
	}
}

func BenchmarkStrictDecode64Scalar(b *testing.B) {
	in := StrictEncode64(benchInput(4096))
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_, _ = stdbase64.StdEncoding.DecodeString(in)
	}
}

// scalarLenientDecode64 is the byte-at-a-time reference the production Decode64
// replaced (gather each sextet into a quad, emit bytes with append). Benchmarking
// it against Decode64 reports the compaction+SIMD speedup on the running arch.
func scalarLenientDecode64(s string) string {
	out := make([]byte, 0, len(s)*3/4)
	var quad [4]byte
	n := 0
	appendQuad := func(dst []byte, q [4]byte, n int) []byte {
		dst = append(dst, q[0]<<2|q[1]>>4)
		if n >= 3 {
			dst = append(dst, q[1]<<4|q[2]>>2)
		}
		if n == 4 {
			dst = append(dst, q[2]<<6|q[3])
		}
		return dst
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '=' {
			if n >= 2 {
				return string(appendQuad(out, quad, n))
			}
			continue
		}
		v := stdVal(c)
		if v < 0 {
			continue
		}
		quad[n] = byte(v)
		n++
		if n == 4 {
			out = appendQuad(out, quad, 4)
			n = 0
		}
	}
	if n >= 2 {
		out = appendQuad(out, quad, n)
	}
	return string(out)
}

// decode64BenchInput reproduces the go-embedded-ruby campaign's decode-3KiB input:
// Base64.encode64 of a 3072-byte payload, i.e. base64 wrapped at 60 columns with a
// newline every line — the lenient decoder's real hot input.
func decode64BenchInput() string {
	p := make([]byte, 256*12) // 3072 bytes, MRI harness payload
	for i := range p {
		p[i] = byte(i % 256)
	}
	return Encode64(string(p))
}

// BenchmarkDecode64SIMD is the production lenient decoder on newline-wrapped input
// (the campaign's decode-3KiB); BenchmarkDecode64Scalar is the byte-at-a-time
// reference it replaced. The ratio is the compaction+SIMD speedup.
func BenchmarkDecode64SIMD(b *testing.B) {
	in := decode64BenchInput()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = Decode64(in)
	}
}

func BenchmarkDecode64Scalar(b *testing.B) {
	in := decode64BenchInput()
	b.SetBytes(int64(len(in)))
	for i := 0; i < b.N; i++ {
		_ = scalarLenientDecode64(in)
	}
}
