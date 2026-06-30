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
