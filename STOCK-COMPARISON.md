# Stock vs optimized: full before/after comparison

**Before:** stock upstream `BobuSumisu/aho-corasick` @ b4b5728 (v1.0.3, no
fork optimizations of any kind).
**After:** this fork's full chain tip (`perf/26-builder-index-fusion`,
master + PRs #3–#10 + the 19-branch stack #11–#29).

**Machine:** AMD EPYC 9R14 (Zen 4), 48 cores, Go 1.25.11, linux/amd64.
**Stability:** load average 0.9–1.0 at start (idle but for the session
agent), no other benchmark processes, no cpufreq scaling exposed (fixed-
frequency VM). Interleaved A/B binaries; benchstat n=8; hyperfine 10+ runs
with warmup. Dispersion: benchstat CIs mostly ≤3%, hyperfine σ ≤3.5% —
consistent with a quiet machine.

**Fairness:** identical public-API-only benchmark source compiled against
both trees (`bench_public_test.go`, pub-prefixed; no internals touched).
Cross-version semantics verified: both binaries report identical match
counts on every workload (e.g. 6,178 on Ibsen/10k-dict; 110,249 on the
400MB spread scan).

## benchstat (Go microbenchmarks, n=8 interleaved)

**geomean: −90.2% (10.2x)**

| Benchmark | stock | tip | Δ |
|---|---|---|---|
| Text sorted-dict 1KB | 2.87µs | 435ns | **−84.9%** |
| Text sorted-dict 4KB | 11.7µs | 1.78µs | **−84.8%** |
| Text sorted-dict 100KB | 322µs | 45.2µs | **−86.0%** |
| Text spread-dict 4KB | 12.8µs | 6.27µs | **−51.1%** |
| Text spread-dict 100KB | 348µs | 88.4µs | **−74.6%** |
| Text 100k-pattern dict 100KB | 728µs | 160µs | **−78.0%** |
| Large input 512KB | 1.65ms | 189µs | **−88.5%** |
| Large input 2MB | 6.62ms | 429µs | **−93.5%** |
| Large input 8MB | 28.3ms | 1.34ms | **−95.3% (21x)** |
| No-match 100KB (sorted) | 198µs | 1.20µs | **−99.4% (165x)** |
| No-match 1MB (sorted) | 2.02ms | 30.8µs | **−98.5%** |
| No-match 100KB (spread) | 197µs | 25.4µs | **−87.1%** |
| No-match 1MB (spread) | 2.02ms | 79.0µs | **−96.1%** |
| Dense overlaps 64KB | 6.49ms | 1.15ms | **−82.3%** |
| Concatenated words 64KB | 1.18ms | 181µs | **−84.7%** |
| Walk sorted-dict 100KB | 233µs | 38.1µs | **−83.7%** |
| Walk spread-dict 100KB | 266µs | 141µs | **−46.8%** |
| MatchFirst (late needle) | 201µs | 28.4µs | **−85.9%** |
| Build 1k patterns | 15.9ms | 970µs | **−93.9%** |
| Build 10k patterns | 163ms | 8.2ms | **−95.0%** |
| Build 100k patterns | 2.27s | 92.6ms | **−95.9% (24.5x)** |

Allocations per Match call: **−94…−100%** on every match workload (the
pool + arena machinery; e.g. Dense 64KB: 2.24MB/op → 13KB/op, sorted-text
scans: zero allocations at steady state). Build allocations −26…−44%.
The only rows with allocation increases are no-match inputs above the
parallel threshold (+3.7KB/op of worker scratch on 1MB inputs — noise
against the 25x speed win there).

## hyperfine (whole-process wall clock, warmup + 10 runs)

| Scenario | stock | tip | Speedup |
|---|---|---|---|
| Build 100k-pattern trie | 2.477s ± 34ms | 312.7ms ± 8.7ms | **7.9x** |
| Scan 400MB prose, sorted dict | 1.554s ± 5ms | 306.0ms ± 10.6ms | **5.1x** |
| Scan 400MB prose, spread dict | 2.26s | 513.8ms ± 2.4ms | **4.4x** |
| Cold end-to-end (build 10k + scan Ibsen once) | 276ms | 113.8ms ± 5.7ms | **2.4x** |

(Whole-process numbers include Go runtime startup and pattern-file
loading, which is why the cold e2e ratio is smaller than the library-only
ratios.)

## Where the wins come from (chain attribution)

- **No-match / long-gap 165x:** SWAR + vectorized IndexByte root skipping,
  windowed multi-stop escape, density-aware parallel dispatch.
- **Build 24.5x:** row-copy DP construction, index-based value-struct
  states with inline flags/failTrans16 fusion (vs stock's per-(state,byte)
  fail-chain walks through per-node maps).
- **Large inputs 21x:** parallel scan with per-worker segment materialize
  and size/liveliness-scaled worker caps (stock is single-threaded).
- **Text scans 4–7x:** devirtualized specialized loops, 16-bit half-width
  tables, stop-entry constant, dual-cursor scans, branchless skip locate.
- **Dense/overlap-heavy 5.6x:** dual-cursor + byte-class-compressed table
  + pooled zero-allocation buffers.
- **Allocation elimination:** pooled match buffers and arena
  materialization (stock allocates every Match struct and slice per call).

## Reproduction

- `bench_public_test.go` (this directory) compiles unmodified against
  both trees; run with `-bench Pub -count 8` per tree and compare with
  benchstat.
- `cmd/acbench` builds against both trees for the hyperfine scenarios:
  `hyperfine -N --warmup 2 './acbench-stock build 100000 1' './acbench-tip build 100000 1'`
  etc. `ACBENCH_DATA` points at `test_data/`.
