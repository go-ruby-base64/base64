# Performance

`go-ruby-base64/base64` is the pure-Go (no cgo) library that
[`rbgo`](https://github.com/go-embedded-ruby/ruby) binds for Ruby's standard
`Base64` module. This page records the **library-level parity benchmark** of its
decode paths against MRI, part of the ecosystem-wide per-module parity suite.

## What is measured

The **same operation MRI performs** — `Base64.decode64` and
`Base64.strict_decode64` over a fixed payload — run through the pure-Go library's
Go API and, side by side, through MRI's C implementation (`String#unpack1("m")` /
`unpack1("m0")`, the C `pack.c` decoder). In the `rbgo build` (AOT) path a Ruby
`Base64.decode64` call compiles straight to this library's `Decode64`, so the
Go-API number below **is** the AOT decode cost — an apples-to-apples "as fast as
the reference?" comparison, not an interpreter-dispatch measurement.

- **Host:** Apple M4 Max (`Mac16,5`, arm64, NEON), macOS 26.5.1 — **date 2026-07-03**.
- **Toolchains:** Go 1.26.4 · MRI `ruby 4.0.5 (+PRISM)`.
- **Input (decode-3KiB):** `Base64.encode64` of a deterministic 3072-byte payload
  — i.e. standard base64 **wrapped at 60 columns with a newline every line**
  (4165 chars). This is exactly the input the go-embedded-ruby campaign harness
  (`bench/modules/base64.rb`) feeds `Base64.decode64`, and the real hot input of
  the lenient decoder.
- **Strict input:** `Base64.strict_encode64` of a 4096-byte payload (5464 chars,
  no newlines).
- Correctness is proven first: `go test` matches a **live MRI oracle**
  (`oracle_test.go`) byte-for-byte on encode, lenient decode, and strict/urlsafe
  decode errors, plus a full round-trip sweep, before any timing.
- **Reproduce (Go side):**
  `GOWORK=off go test -run='^$' -bench='Decode64SIMD|Decode64Scalar|StrictDecode64SIMD' -benchmem -count=5 .`

## Where the gap was

The lenient `Decode64` and the strict pre-scan were the two slow paths; the
underlying [`go-simd/base64`](https://github.com/go-simd/base64) SIMD decode was
**not** the bottleneck (its NEON kernel decodes clean base64 at ~5.5 GB/s here) and
the pinned version already exports everything needed — no dependency bump was
required. The cost was entirely in this library's wrappers:

- **`Decode64` never touched SIMD.** It gathered sextets one byte at a time through
  a five-way `switch` and emitted the plaintext with per-byte `append` — a naive
  scalar loop ~5× slower than MRI's C.
- **`StrictDecode64`'s pre-scan** classified every byte with the same branchy
  `switch` (not inlined), which dominated its runtime.

The fix keeps both paths **byte-identical and MRI-faithful** (RFC 4648 strict +
relaxed, urlsafe, padding rules all unchanged — verified against the live oracle):

- `Decode64` now compacts each maximal run of alphabet bytes (bulk `copy`, dropping
  the stray bytes between runs, honoring MRI's `=`-stops-a-partial-quad rule) into a
  scratch buffer, then hands the clean bytes to `go-simd/base64`'s batched decoder
  in a single pass; only the 2/3-char tail is finished by hand.
- The strict pre-scan classifies each byte with one 256-entry table load
  (`decStdLUT`, the table form of the existing `stdVal`) instead of the branch
  cascade.

## Results (median ns/op, arm64 NEON)

### `Base64.decode64` — decode-3KiB (newline-wrapped, the campaign input)

| decoder | ns/op | vs MRI |
| --- | ---: | ---: |
| MRI `Base64.decode64` (C `unpack("m")`) | 1871 | 1.00× |
| **`Decode64` — after (compact + SIMD)** | **2116** | **1.13×** |
| `Decode64` — before (per-byte scalar) | 9489 | 5.07× |

**decode-3KiB went from 5.07× MRI to 1.13× MRI — a 4.5× speedup, landing at MRI
parity.** On clean (un-wrapped) strict-form input `Decode64` runs ~1.96 GB/s,
around parity/just under MRI as well.

### `Base64.strict_decode64` (5464 chars, no newlines)

| decoder | ns/op | vs MRI |
| --- | ---: | ---: |
| MRI `Base64.strict_decode64` (C `unpack("m0")`) | 1754 | 1.00× |
| **`StrictDecode64` — after (LUT pre-scan + SIMD)** | 2613 | 1.49× |
| `StrictDecode64` — before (branchy pre-scan + SIMD) | 7400 | 4.22× |

**strict decode went from 4.2× MRI to 1.5× MRI — a 2.8× speedup.**

## Honest floor

The wrapped lenient path sits at ~parity (1.1× MRI), not below it, for a
structural reason: it must strip the line-wrap bytes in a byte-wise **despacing**
pass before the vectorized decode, whereas MRI fuses that skipping into its single
C decode loop. `go-simd/base64` has no SIMD *despace* kernel, so the compaction
pass is scalar; the SIMD decode that follows is already at NEON speed and cannot
be made to swallow the newlines. A future SIMD despace kernel in `go-simd/base64`
(benefiting the whole ecosystem) is the path to push the wrapped case below MRI;
clean, un-wrapped input already decodes at ~parity without it. These are **real
measured numbers from the 2026-07-03 run** — nothing fabricated or cherry-picked.
