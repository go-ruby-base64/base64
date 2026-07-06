<p align="center"><img src="https://raw.githubusercontent.com/go-ruby-base64/brand/main/social/go-ruby-base64-base64.png" alt="go-ruby-base64/base64" width="720"></p>

# base64 — go-ruby-base64

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-DC2626)](https://go-ruby-base64.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--coverage)

**A pure-Go (no cgo) reimplementation of Ruby's standard-library
[`Base64`](https://docs.ruby-lang.org/en/master/Base64.html) module** — the
deterministic, interpreter-independent core of MRI 4.0.5's `require "base64"`. It
reproduces, byte-for-byte, what MRI computes for every method: the RFC-2045
60-column line wrapping of `encode64`, the lenient stream semantics of `decode64`,
the strict variants' `ArgumentError` on malformed input, and the url-safe
alphabet with optional padding — **without any Ruby runtime**.

The hot standard-alphabet encode/decode paths are built on the SIMD kernels of
[**go-simd/base64**](https://github.com/go-simd/base64) (go-asmgen: amd64
SSE/AVX2, arm64 NEON, ppc64le VSX, s390x vector; stdlib fallback elsewhere), so
the output stays bit-identical to `encoding/base64.StdEncoding` while going faster
on the supported arches. Only the MRI-specific framing — the 60-column wrap, the
lenient `unpack("m")` quad/padding state machine, and the url-safe padding rules —
is hand-written here.

It is the Base64 backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module with no dependency on the Ruby runtime — a sibling
of [go-ruby-yaml](https://github.com/go-ruby-yaml/yaml) (Psych),
[go-ruby-regexp](https://github.com/go-ruby-regexp/regexp) (the Onigmo engine) and
[go-ruby-erb](https://github.com/go-ruby-erb/erb) (the ERB compiler).

> **What it is — and isn't.** Base64 encoding/decoding is fully deterministic and
> needs **no interpreter**, so it lives here as pure Go. This package operates on
> Go `string`s (Ruby strings are byte sequences); binding the methods to a live
> Ruby `Base64` module — argument coercion, raising `ArgumentError` — is the
> host's job, and this library returns a Go `error` (`ErrInvalid`) the host maps
> to MRI's exception.

## Features

Faithful port of MRI's `Base64`, validated against the `ruby` binary on every
platform that has one:

- **`Encode64`** — RFC 2045 (`Array#pack("m")`): the standard `+/` alphabet, a
  newline every 60 output characters, and a trailing newline. The empty string
  encodes to the empty string (no newline), as in MRI.
- **`Decode64`** — lenient (`String#unpack1("m")`): non-alphabet bytes are
  skipped, padding is optional, a `=` on a 2- or 3-char partial quad finalises it
  and **stops** decoding (trailing-padding terminates the stream) while a `=` on a
  quad boundary is ignored, and a lone orphaned final sextet is discarded.
- **`StrictEncode64` / `StrictDecode64`** — `pack("m0")` / `unpack1("m0")`: no
  newlines on encode; decode rejects any stray byte (including the embedded
  newlines `encoding/base64` would tolerate) with `ErrInvalid`.
- **`UrlsafeEncode64` / `UrlsafeDecode64`** — the `-_` alphabet (RFC 4648);
  padding is on by default and stripped when `padding=false`; decode accepts
  correctly-padded or unpadded input and rejects everything else.

CGO-free, **100% test coverage**, `gofmt` + `go vet` clean, and green across the
six 64-bit Go targets (amd64, arm64, riscv64, loong64, ppc64le, s390x) on Linux,
macOS, and Windows.

## Install

```sh
go get github.com/go-ruby-base64/base64
```

## Usage

```go
package main

import (
	"fmt"

	"github.com/go-ruby-base64/base64"
)

func main() {
	fmt.Printf("%q\n", base64.Encode64("hello world"))
	// "aGVsbG8gd29ybGQ=\n"            — RFC-2045, trailing newline

	fmt.Printf("%q\n", base64.StrictEncode64("hello"))
	// "aGVsbG8="                      — no newline

	fmt.Println(base64.Decode64("aGVs bG8=\n junk")) // hello — lenient

	s, err := base64.StrictDecode64("not base64!!!")
	fmt.Printf("%q %v\n", s, err)      // "" invalid base64

	fmt.Println(base64.UrlsafeEncode64("\xfb\xff\xfe"))        // -__-
	fmt.Println(base64.UrlsafeEncode64("ab", false))           // YWI  (unpadded)
}
```

## API

```go
// Encode64 — RFC 2045 (pack("m")): standard alphabet, \n every 60 chars + trailing \n.
func Encode64(s string) string

// StrictEncode64 — pack("m0"): standard alphabet, no newlines.
func StrictEncode64(s string) string

// Decode64 — lenient unpack1("m"): skips stray bytes, optional padding, padding stops.
func Decode64(s string) string

// StrictDecode64 — unpack1("m0"): returns ErrInvalid on any malformed input.
func StrictDecode64(s string) (string, error)

// UrlsafeEncode64 — -_ alphabet; padding defaults to true (pass false to strip it).
func UrlsafeEncode64(s string, padding ...bool) string

// UrlsafeDecode64 — -_ alphabet, RFC 4648; accepts padded or unpadded input.
func UrlsafeDecode64(s string) (string, error)

// ErrInvalid is returned by the strict decoders, mirroring MRI's ArgumentError.
var ErrInvalid = errors.New("invalid base64")
```

## SIMD acceleration

`StrictEncode64`, `Encode64`, `StrictDecode64` and `UrlsafeDecode64` route the
standard-alphabet body through `go-simd/base64`, whose go-asmgen kernels cover the
SIMD-capable 64-bit arches and fall back to `encoding/base64` everywhere else, so
the bytes are always identical to `StdEncoding`. On amd64/arm64 the encode kernel
measures markedly faster than the scalar `encoding/base64` reference; the package
ships `go test -bench` benchmarks (`Benchmark*SIMD` vs `Benchmark*Scalar`) so the
speedup can be measured on each target.

## Tests & coverage

The suite pairs deterministic, ruby-free golden tests — captured from MRI 4.0.5,
which alone hold coverage at 100% so the Windows and qemu cross-arch lanes pass
the gate — with a **differential MRI oracle** that drives a wide corpus (every
length class, a line wrap, non-ASCII bytes, and malformed/strict inputs) through
the system `ruby` and asserts byte-equality. The oracle scripts `binmode` both
stdin and stdout so Windows text-mode never pollutes the bytes, gate on
`RUBY_VERSION >= "4.0"`, and skip themselves where `ruby` is absent.

```sh
COVERPKG=$(go list ./... | paste -sd, -)
go test -race -coverpkg="$COVERPKG" -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1   # 100.0%
```

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-ruby-base64/base64 authors.

## WebAssembly

Being pure Go (CGO=0), this library also compiles to **WebAssembly** — both
`GOOS=js GOARCH=wasm` (browser / Node.js) and `GOOS=wasip1 GOARCH=wasm` (WASI).
CI builds both targets on every push, alongside the six 64-bit native/qemu arches.

```sh
GOOS=js     GOARCH=wasm go build ./...   # browser / Node
GOOS=wasip1 GOARCH=wasm go build ./...   # WASI (wasmtime, wasmer, wasmedge, …)
```
