// Copyright (c) the go-ruby-base64/base64 authors
//
// SPDX-License-Identifier: BSD-3-Clause

package base64

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// These differential tests pin this package's output to a live MRI `ruby` binary.
// They skip themselves where ruby is absent (the Windows lane and the qemu
// cross-arch lanes) and where MRI is older than 4.0, so the deterministic suite in
// base64_test.go alone drives the 100% gate there.

// rubyBin locates a usable `ruby` (>= 4.0) once, or skips.
func rubyBin(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby not on PATH; skipping MRI oracle")
	}
	out, err := exec.Command(path, "-e", "print RUBY_VERSION").Output()
	if err != nil {
		t.Skipf("cannot query ruby version: %v", err)
	}
	if v := string(out); v < "4.0" {
		t.Skipf("ruby %s < 4.0; skipping MRI oracle", v)
	}
	return path
}

// rubyEval runs an MRI script with binmode on both stdin and stdout so Windows
// text-mode (were ruby ever present there) cannot pollute the bytes, and returns
// stdout. stdin carries the input string so arbitrary bytes survive the boundary.
func rubyEval(t *testing.T, bin, script, stdin string) string {
	t.Helper()
	cmd := exec.Command(bin, "-rbase64", "-e",
		"$stdout.binmode\n$stdin.binmode\n"+script)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ruby error: %v\nscript:\n%s\noutput:\n%s", err, script, out)
	}
	return string(out)
}

// corpus is the shared set of input strings the oracle drives, spanning every
// length class (so every padding remainder and a line wrap are exercised) plus
// non-ASCII bytes that map to + / - _ in the two alphabets.
func corpus() []string {
	c := []string{"", "f", "fo", "foo", "foob", "fooba", "foobar",
		"hello world", "\xfb\xff\xfe", "\x00\x01\x02\x03\xff"}
	for _, n := range []int{44, 45, 46, 50, 120} {
		c = append(c, strings.Repeat("a", n))
	}
	return c
}

func TestOracleEncode(t *testing.T) {
	bin := rubyBin(t)
	for _, s := range corpus() {
		s := s
		t.Run(strconv.Itoa(len(s)), func(t *testing.T) {
			// MRI prints the three encodings, each delimited so we can split.
			script := "i = $stdin.read\n" +
				"print Base64.encode64(i), \"\\0\", " +
				"Base64.strict_encode64(i), \"\\0\", " +
				"Base64.urlsafe_encode64(i), \"\\0\", " +
				"Base64.urlsafe_encode64(i, padding: false)"
			parts := strings.Split(rubyEval(t, bin, script, s), "\x00")
			if len(parts) != 4 {
				t.Fatalf("oracle returned %d parts", len(parts))
			}
			check(t, "encode64", Encode64(s), parts[0])
			check(t, "strict_encode64", StrictEncode64(s), parts[1])
			check(t, "urlsafe_encode64", UrlsafeEncode64(s), parts[2])
			check(t, "urlsafe_encode64(pad=false)", UrlsafeEncode64(s, false), parts[3])
		})
	}
}

func TestOracleDecode(t *testing.T) {
	bin := rubyBin(t)
	// Decode inputs: clean encodings plus deliberately messy ones for the lenient
	// decoder, and well-/ill-formed strict/urlsafe inputs.
	inputs := []string{
		"aGVsbG8=", "aGVsbG8gd29ybGQ=\n", "YWJj", "YWJjZA", "YQ==",
		"aGVs bG8=\n junk", "YWJj=ZZZZ", "YW=JjZ", "Q", "====", "",
		// The lenient hot path: a long newline-wrapped encode64 (the decode-3KiB
		// campaign input) drives the SIMD Compact de-space + in-place SIMD decode
		// over many 60-column lines, so the whole-window SWAR move and the in-place
		// Decode contract are proved byte-for-byte against MRI, not just round-trip.
		Encode64(strings.Repeat("The quick brown fox. ", 200)),
		// A long run peppered with stray bytes on every SWAR window boundary forces
		// the straddle fallback and a compacted length whose tail exercises each
		// partial-quantum remainder against MRI's C loop.
		strings.Repeat("YWJjZA==\n\t !", 64) + "aGVsbG8",
	}
	for _, in := range inputs {
		in := in
		t.Run(strconv.Itoa(len(in)), func(t *testing.T) {
			script := "i = $stdin.read\nprint Base64.decode64(i)"
			want := rubyEval(t, bin, script, in)
			check(t, "decode64", Decode64(in), want)
		})
	}
}

func TestOracleStrictAndUrlsafeErrors(t *testing.T) {
	bin := rubyBin(t)
	// For each input ask MRI whether strict/urlsafe decode raises; mirror it here.
	inputs := []string{"aGVsbG8=", "aGVsbG8", "aGVs\nbG8=", "not base64!!!",
		"====", "YWI=", "YWI", "-__-", "MDEyMzQ1Njc", "bad_==", "MDEyMzQ1Njc=="}
	for _, in := range inputs {
		in := in
		t.Run(strconv.Itoa(len(in)), func(t *testing.T) {
			script := "i = $stdin.read\n" +
				"begin; print 'S:', Base64.strict_decode64(i); rescue ArgumentError; print 'S!'; end\n" +
				"print \"\\0\"\n" +
				"begin; print 'U:', Base64.urlsafe_decode64(i); rescue ArgumentError; print 'U!'; end"
			parts := strings.Split(rubyEval(t, bin, script, in), "\x00")

			sgot, serr := StrictDecode64(in)
			want := parts[0]
			if serr != nil {
				if want != "S!" {
					t.Errorf("strict_decode64(%q): we erred, MRI = %q", in, want)
				}
			} else if want != "S:"+sgot {
				t.Errorf("strict_decode64(%q) = %q, MRI = %q", in, sgot, want)
			}

			ugot, uerr := UrlsafeDecode64(in)
			want = parts[1]
			if uerr != nil {
				if want != "U!" {
					t.Errorf("urlsafe_decode64(%q): we erred, MRI = %q", in, want)
				}
			} else if want != "U:"+ugot {
				t.Errorf("urlsafe_decode64(%q) = %q, MRI = %q", in, ugot, want)
			}
		})
	}
}

func check(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, MRI = %q", name, got, want)
	}
}
