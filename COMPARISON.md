# Three-way comparison: perf-stack vs research/keep-candidate vs ac-lab

**Date:** 2026-07-12 · **Box:** AMD EPYC 9R14 (Zen 4), 48 cores, Go 1.25.11
**Method:** all four binaries share one benchmark superset (our lab matrix +
keep-candidate's midsize/output-heavy/spread corpora). Round-robin interleaved,
n=6, quiet machine, benchstat. Baseline column = origin/master (ea4bca2).

## Headline (sec/op vs master)

| Benchmark | perf-stack | keep-candidate | ac-lab |
|---|---|---|---|
| **geomean (union suite)** | **−44.1%** | −17.3% | −31.6% |
| SingleStop text 100KB | **−26.7%** | ~ | ~ |
| MultiStop 63k-state trie, text | −1.6% | ~ | **−9.7%** |
| Big 190k-state trie, text | −9.0% | +3.2% | **−28.5%** |
| NoMatch single-stop | **−95.1%** | ~ | −76.1% |
| NoMatch multi-stop | ~ | ~ | **+24.7% (regression)** |
| Dense emit (ab-64k) | **−52.1%** | ~ | −52.4% |
| Walk multi-stop | ~ | ~ | **−41.3%** |
| Build 10k patterns | −89.2% | −89.5% | **−95.5%** |
| Parallel 2MB | **−51.0%** | ~ | −10.0% |
| MultiSmall (3k-state multi trie) | +3.3% | −7.3% | **−10.8%** |
| MultiDense (dense words, 63k trie) | **−63.0%** | −50.0% | ~ |
| Skip2 no-match (2 stop bytes) | **−68.8%** | ~ | **+37.1% (regression)** |
| Midsize_Spread1k (keep's flagship) | −24.9% | −25.7% | **−41.3%** |
| OutputHeavy_DenseSpread | **−83.3%** | −50.4% | −63.1% |
| spread10k/Ibsen | −32.0% | −30.5% | **−36.8%** |

Neither effort dominates: **each wins regimes the others miss, and the wins are
largely disjoint → they are combinable.** Two regressions in ac-lab (NoMatch/multi,
Skip2) are cap/skip interactions our stack already solves.

## What each body of work contains

### perf-stack (this branch stack, rebased on master ea4bca2)
Single-stop specializations and dense/parallel machinery:
density-dispatched dual-cursor (single-stop AND table paths), density-aware
parallel thresholds + size-scaled cap 32, 16-bit multi-stop paths, dense-only
byte-class table, windowed IndexByte skip escape (2–4 stop bytes), parallel
segment materialize, row-copy builder. 10 perf commits + harness.

### research/keep-candidate (rebased on master; measured on Graviton, cross-checked here)
1. **transC**: byte-class table, *premultiplied* entries (`state<<log2 | emitFlag`
   in bit 0), exact active-byte classes, min stride 2, **always on** for
   multi-stop tries. On Zen 4: real wins on midsize prose (−25.7%) and
   spread10k (−30.5%), **+3.2% regression on the 190k-state trie** (its
   Graviton Huge100k −3.1% did not transfer).
2. **Sorted-children builder** — same idea as our row-copy builder (−89.5%).
3. Valuable REJECT evidence: dual-table ungated by density regresses prose
   (+13.3% Midsize) — consistent with our density-gate design; flattened
   outputs, row interning, rare-byte prefilters all closed.
4. New corpora (midsize/output-heavy/spread) — adopted into our harness.

### ac-lab (based on old 02f31eb; needs the same master-port our stack got)
Independent effort; convergent with ours on: full-gap dual-lane skip (=D1),
walk16 specialization (=C3), segment materialize (=C10), deterministic BFS,
pooled-raw fix (already upstream). **Novel and valuable:**
1. **Index-based builder** (value-struct states, sibling lists, DP with inline
   flags/failTrans16, memmove rows): Build 10k **8.0ms vs our 18.9ms** — the
   best builder of the three.
2. **M-skipbit**: rootStop lookups shifted into disjoint bytes of one word +
   TrailingZeros positioning — kills the scalar re-scan in skipRootTable
   (Binary −32%, and a big part of its Big/Walk-multi wins).
3. **M-anchor (depth1Cont)**: inert-pair false-anchor rejection in single-stop
   loops (Hedvig −7.6%, MatchIbsen −3.7%) — nothing like it in our stack.
4. **Unsafe loads in the 32-bit Walk/matchTable paths** (their flagged-for-review
   item): Walk/multi **−41%** — our stack left plain walkTable with checked
   indexing.
5. **Family-based worker cap** (single-stop 8 / table 32): wins Big-trie scans
   but **regresses skip-dominated table inputs** (NoMatch/multi +24.7%,
   Skip2 +37%) — our size(+density) cap avoids exactly that; merge needed.
6. Their M1 byte-class REJECT is *not* in conflict with our C4: they tested it
   always-on against prose (shallow, cache-resident) — same reason we gate
   ours to dense inputs only. keep-candidate's premultiplied variant shows
   always-on can pay on midsize prose; the three results triangulate to:
   **class table pays on deep-dwell/dense scans and midsize prose, hurts or
   does nothing on big-trie prose** — dispatch, don't hard-enable.

## Conflict / overlap matrix

| Area | perf-stack | keep-candidate | ac-lab | Resolution when combining |
|---|---|---|---|---|
| Builder | row-copy + slices | sorted-children | **index-based DP (fastest)** | take ac-lab's, port to master, keep upstream guard + determinism test |
| skipRootTable | C8 IndexByte escape + guard | — | **skipbit** | compose: skipbit inner loop + C8 escape after 128B |
| Single-stop loop | D3 density dispatch | — | **depth1Cont anchor rejection** | both (independent mechanisms; re-tune D3 threshold after anchor lands) |
| Walk paths | walk16 (ft16 tries) | — | **unsafe 32-bit walkTable** | both (disjoint trie shapes) |
| Dual-cursor table | density-gated, 16+32-bit+class | REJECTED (ungated) | size-gated (ft16 only) | keep density gate (their rejects validate it) |
| Class table | dense-only, plain ids | **always-on, premultiplied** | rejected (always-on, plain) | keep dense-only dispatch; adopt premultiplied encoding + exact classes; A/B always-on-for-midsize as a follow-up |
| Worker cap | size ≥256KB → 32 | — | family-based | merge: table-family AND (≥256KB OR rootDense) → 32; else 8 |
| Parallel materialize | segments + parallel expand | — | segments incl. dual lanes | ours + their dual-lane segmentation (measure) |
| Parallel threshold | 32KB/128KB by density | — | — | ours |
| 16-bit multi-stop | matchTable16/walk16 | rejected ("dominated by transC") | present (dual gated on it) | keep; revisit vs premultiplied transC on midsize |

## Combined-stack integration plan (proposed order, each its own commit + A/B)

1. Current perf-stack (10 commits, done, branches `perf/09`–`perf/18`).
2. `perf/19-skipbit` — ac-lab M-skipbit merged into skipRootTable (compose with C8).
3. `perf/20-unsafe-walktable` — ac-lab unsafe loads in walkTable/walkStopByte
   (32-bit) — the flagged-for-review unsafe-idiom extension; checkptr+fuzz gates.
4. `perf/21-depth1cont` — ac-lab inert-pair anchor rejection; re-validate D3
   threshold after.
5. `perf/22-builder-dp` — replace builder with ac-lab index-based version
   (subsumes perf/09; expected Build −55% further).
6. `perf/23-family-cap` — merged cap rule (fixes ac-lab's two regressions,
   keeps its Big-trie scaling).
7. `perf/24-transc-encoding` — premultiplied + exact-class encoding for
   failTransC (keep-candidate's contribution), still dense-dispatched;
   then a measured decision on midsize-prose enablement.

Expected combined profile ≈ per-benchmark min of the three columns above:
geomean vs master estimated ≈ −50% or better, with no known regression rows.

## Verification status

- perf-stack: every commit green (`go test ./...` at each of 11 positions);
  full gates at HEAD (race, checkptr, FuzzMatch 120s, FuzzEncodeDecode 60s).
- keep-candidate: upstream branch, own gates per its SUMMARY; our re-measure
  on Zen 4 confirms midsize wins, finds the Big-trie regression.
- ac-lab: own gates per its RESEARCH-LOG (race/checkptr/fuzz 300s); based on
  02f31eb — must be ported commit-by-commit onto master like our stack was
  (upstream sampler + Decode hardening interactions), re-benchmarked per step.
