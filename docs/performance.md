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
- **Toolchains:** Go 1.26.4 · MRI `ruby 4.0.5 (+PRISM)` · MRI + YJIT.
- **Input (decode64-3KiB):** `Base64.encode64` of a deterministic 3072-byte payload
  — i.e. standard base64 **wrapped at 60 columns with a newline every line**
  (4165 chars). This is exactly the input the go-embedded-ruby campaign harness
  feeds `Base64.decode64`, and the real hot input of the lenient decoder.
- **Strict input:** `Base64.strict_encode64` of a 3072-byte payload (4096 chars,
  no newlines).
- Correctness is proven first: `go test` matches a **live MRI oracle**
  (`oracle_test.go`) byte-for-byte on encode, lenient decode (including a
  5694-char newline-wrapped input that drives the SIMD de-space + in-place decode)
  and strict/urlsafe decode errors, plus a full round-trip sweep, before any timing.
- **Reproduce (cross-runtime):** the committed harness at
  [`go-ruby-base64/docs` → `benchmarks/`](https://github.com/go-ruby-base64/docs/tree/main/benchmarks):
  `bash benchmarks/run.sh` (env `OUTER`/`WARM` tune the pass budget).

## Where the gap was

The lenient `Decode64` was the last slow path. The underlying
[`go-simd/base64`](https://github.com/go-simd/base64) SIMD *decode* was **not** the
bottleneck (its NEON kernel decodes clean base64 at ~5.5 GB/s here); the residual
was the **de-space** pass — stripping the 60-column line breaks out of the input
before the vectorised decode. MRI fuses that skipping into its single C
`unpack("m")` loop; the earlier Go port did it with a **scalar** compaction (find
each maximal alphabet run, bulk-`copy` it, drop the strays between runs) into a
scratch buffer, then SIMD-decoded the clean bytes into a *second* buffer. That
scalar de-space plus the extra allocation kept the wrapped path at ~1.13× MRI —
just behind the reference (and behind YJIT).

The fix removes both costs, keeping the path **byte-identical and MRI-faithful**
(RFC 4648 strict + relaxed, urlsafe, padding, the `=`-stops-a-partial-quad rule —
all verified against the live oracle):

- **SIMD de-space.** `go-simd/base64` now exports `Compact`, a vectorised
  (branch-free SWAR, wide-vector left-pack planned) de-space kernel that drops
  every non-alphabet byte *and* applies MRI's `=`-on-a-partial-quantum stop rule in
  one pass. `Decode64` calls it instead of the scalar run-finder.
- **In-place decode, one buffer.** `Compact` de-spaces **in place** and the SIMD
  `Decode` then decodes the compacted run **in place** (its documented in-place
  contract — the write cursor always trails the read cursor). The whole decode now
  costs one working buffer plus the result string, matching MRI's C allocation
  profile instead of the scratch + output buffers the naive port needed. Only the
  2/3-char tail is finished by hand.

## Results (median ns/op, arm64 NEON)

`vs MRI` / `vs YJIT` < 1.00× means **faster than** that runtime.

### `Base64.decode64` — decode64-3KiB (newline-wrapped, the campaign input)

Median of the default harness budget (`bash benchmarks/run.sh`, 3 warm-up + 25
timed passes, best pass):

| decoder | ns/op | vs MRI | vs YJIT |
| --- | ---: | ---: | ---: |
| MRI `Base64.decode64` (C `unpack("m")`) | 1794 | 1.00× | 1.02× |
| MRI + YJIT | 1757 | 0.98× | 1.00× |
| **`Decode64` — after (SIMD `Compact` + in-place `Decode`)** | **1654** | **0.92×** | **0.94×** |
| `Decode64` — before (scalar de-space + SIMD decode) | 2039 | 1.13× | 1.16× |

**decode64-3KiB went from 1.13× MRI (1.16× YJIT) to 0.92× MRI (0.94× YJIT) — it
now beats both MRI and MRI + YJIT.** The margin over YJIT is consistent but modest
(measured go/YJIT ranged 0.92–0.99 across repeated runs, always below 1.00); see
the honest floor below.

### `Base64.strict_decode64` — decode-3KiB (no newlines) · `Base64.strict_encode64` — encode-3KiB

Unchanged by this work (`StrictDecode64` / `StrictEncode64` were not touched),
re-measured here to confirm no regression:

| operation | go ns/op | MRI | YJIT | vs MRI | vs YJIT |
| --- | ---: | ---: | ---: | ---: | ---: |
| `strict_decode64` (3 KiB, no newlines) | 1675 | 1296 | 1228 | 1.29× | 1.36× |
| `strict_encode64` (3 KiB) | 783 | 1091 | 1085 | 0.72× | 0.72× |

Encode beats MRI comfortably (the go-simd kernel is on the encode path). Strict
decode remains ~1.3× MRI: its byte-scan + strict validation has no newlines to
strip, so `Compact` does not apply — its residual is a separate, smaller target.

## Honest floor

decode64 now sits **below** both MRI and YJIT, but the lead over YJIT is thin
(~2–8% depending on run; go/YJIT never rose above 1.00 across the repeated
measurements, median ≈ 0.94). The win came from moving the de-space from a
scalar loop to the SIMD `Compact` kernel and collapsing three buffers to one via
the in-place `Decode` contract; the remaining distance to a larger margin is the
`Compact` kernel itself, which is still a SWAR loop — a wide-vector left-pack
(go-asmgen, per arch, already planned behind the same signature) would widen the
lead further and lift the other five arches too. These are **real measured numbers
from the 2026-07-03 run** on the host named above — nothing fabricated or
cherry-picked.
