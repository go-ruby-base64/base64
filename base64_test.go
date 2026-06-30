// Copyright (c) the go-ruby-base64/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

package base64

import (
	"errors"
	"strings"
	"testing"
)

// The golden vectors below were captured from MRI 4.0.5's Base64 (see
// oracle_test.go, which re-derives them from a live `ruby` on the CI lanes that
// have one). They are deterministic and interpreter-free, so this suite alone
// keeps coverage at 100% on the Windows and cross-arch lanes.

func TestEncode64(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"f", "Zg==\n"},
		{"fo", "Zm8=\n"},
		{"foo", "Zm9v\n"},
		{"hello world", "aGVsbG8gd29ybGQ=\n"},
		// 45 raw bytes encode to exactly 60 chars -> one full line + trailing \n.
		{strings.Repeat("a", 45), "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFh\n"},
		// 50 raw bytes -> 68 chars -> wrapped at 60 with a trailing \n.
		{strings.Repeat("a", 50), "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFh\nYWFhYWE=\n"},
	}
	for _, c := range cases {
		if got := Encode64(c.in); got != c.want {
			t.Errorf("Encode64(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStrictEncode64(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"hello", "aGVsbG8="},
		{strings.Repeat("a", 50), "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE="},
	}
	for _, c := range cases {
		got := StrictEncode64(c.in)
		if got != c.want {
			t.Errorf("StrictEncode64(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("StrictEncode64(%q) contains a newline", c.in)
		}
	}
}

func TestDecode64Lenient(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"YWJj", "abc"},                       // clean, no padding
		{"YQ==", "a"},                         // padded, stops at padding
		{"YWJjZA", "abcd"},                    // unpadded, length 6
		{"aGVsbG8gd29ybGQ=\n", "hello world"}, // spaces dropped, '=' stops
		{"aGVs bG8=\n garbage!", "hello"},     // spaces dropped, '=' after 'bG8' stops decode
		{"YWJj=ZZZZ", "abce\x96Y"},            // '=' on a quad boundary is ignored, decoding continues
		{"YW=JjZ", "a"},                       // '=' after 2 chars -> emit 1 byte, stop
		{"YWJ=jZ", "ab"},                      // '=' after 3 chars -> emit 2 bytes, stop
		{"Q", ""},                             // single orphan sextet -> discarded
		{"====", ""},                          // only padding -> empty
		{strings.Repeat("a", 1), ""},          // one alphabet char -> orphan, discarded
	}
	for _, c := range cases {
		if got := Decode64(c.in); got != c.want {
			t.Errorf("Decode64(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStrictDecode64(t *testing.T) {
	got, err := StrictDecode64("aGVsbG8=")
	if err != nil || got != "hello" {
		t.Fatalf("StrictDecode64 ok case = %q, %v", got, err)
	}
	for _, bad := range []string{"not base64!!!", "aGVsbG8", "aGVs\nbG8=", "====", "a"} {
		if _, err := StrictDecode64(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("StrictDecode64(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestUrlsafeEncode64(t *testing.T) {
	// "\xfb\xff\xfe" -> "+/+-" in std, "-_+-"... verified against MRI: "-__-".
	cases := []struct {
		in   string
		pad  []bool
		want string
	}{
		{"\xfb\xff\xfe", nil, "-__-"},
		{"\xfb\xff\xfe", []bool{true}, "-__-"},
		{"ab", nil, "YWI="},
		{"ab", []bool{false}, "YWI"},
		{"a", []bool{false}, "YQ"}, // "YQ==" -> strip "==" -> "YQ"
		{"abcd", []bool{false}, "YWJjZA"},
		{"", nil, ""},
		{"", []bool{false}, ""},
	}
	for _, c := range cases {
		got := UrlsafeEncode64(c.in, c.pad...)
		if got != c.want {
			t.Errorf("UrlsafeEncode64(%q, %v) = %q, want %q", c.in, c.pad, got, c.want)
		}
	}
}

func TestUrlsafeDecode64(t *testing.T) {
	ok := []struct{ in, want string }{
		{"YWI=", "ab"},               // padded
		{"YWI", "ab"},                // unpadded -> re-padded
		{"-__-", "\xfb\xff\xfe"},     // url alphabet
		{"MDEyMzQ1Njc", "01234567"},  // unpadded, length 11
		{"MDEyMzQ1Njc=", "01234567"}, // padded
		{"", ""},
	}
	for _, c := range ok {
		got, err := UrlsafeDecode64(c.in)
		if err != nil || got != c.want {
			t.Errorf("UrlsafeDecode64(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"bad_==", "MDEyMzQ1Njc==", "a b", "===="} {
		if _, err := UrlsafeDecode64(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("UrlsafeDecode64(%q) err = %v, want ErrInvalid", bad, err)
		}
	}
}

// TestRoundTrip exercises every length class through encode -> decode for all
// three alphabets, ensuring the SIMD kernels and the framing compose losslessly.
func TestRoundTrip(t *testing.T) {
	for n := 0; n < 200; n++ {
		src := make([]byte, n)
		for i := range src {
			src[i] = byte(i*31 + 7)
		}
		s := string(src)

		if got := Decode64(Encode64(s)); got != s {
			t.Fatalf("encode64 round-trip n=%d", n)
		}
		dec, err := StrictDecode64(StrictEncode64(s))
		if err != nil || dec != s {
			t.Fatalf("strict round-trip n=%d: %v", n, err)
		}
		for _, pad := range []bool{true, false} {
			ud, err := UrlsafeDecode64(UrlsafeEncode64(s, pad))
			if err != nil || ud != s {
				t.Fatalf("urlsafe round-trip n=%d pad=%v: %v", n, pad, err)
			}
		}
	}
}

// TestStdVal exercises every branch of the 6-bit alphabet decoder, including the
// boundary characters and the rejection of a non-alphabet byte.
func TestStdVal(t *testing.T) {
	cases := []struct {
		c    byte
		want int
	}{
		{'A', 0}, {'Z', 25}, {'a', 26}, {'z', 51},
		{'0', 52}, {'9', 61}, {'+', 62}, {'/', 63}, {'!', -1}, {'\n', -1},
	}
	for _, c := range cases {
		if got := stdVal(c.c); got != c.want {
			t.Errorf("stdVal(%q) = %d, want %d", c.c, got, c.want)
		}
	}
}
