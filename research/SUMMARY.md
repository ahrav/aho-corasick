# Perf-gap research — final classification

**Scope:** every candidate area from the goal, closed with KEEP / REJECT / DEFER.
**Baseline for all A/B:** perf/integration 63116c0 (HEAD 02f31eb + already-reviewed
density gate, deterministic-BFS fix, Decode hardening). HEAD itself lacks those
reviewed fixes; adopting them is a prerequisite (E0).
**Method:** hypotheses written before coding (research/HYPOTHESES.md); round-robin
interleaved A/B on pinned cores with load gating; benchstat n=10–12; correctness
gates per change: full suite, differential fuzz (FuzzMatch vs naiveMatch), -race,
checkptr, Encode determinism. Box: 32-core Graviton, Go 1.25.

## KEEP (branch `research/keep-candidate`, all gates green)

### 1. Byte-class compressed transition table (transC) for multi-stop-byte tries
Natural-language dictionaries use ~31–33 byte classes out of 256; rows shrink
1KB→128B (padded to 32 entries ×4B). The scan is cache-bound on realistic
dicts (measured: hot-99% set = 2.9K states = 2.9MB full rows ≫ L1; transC hot
set = 363KB). Entries store premultiplied row offsets (state<<log2 | emitFlag),
and classOf[input[i]] depends only on the input byte, so it stays off the
serial dependency chain — this is what the previously-rejected version got
wrong (it was tested on the cache-resident sorted corpus and indexed on-chain).
- r4 (n=12, tight CIs): spread10k/Ibsen **−19.0%**, spread10k/GPL **−16.7%**,
  Midsize1k **−15.4%/−12.4%**, DenseSpread **−46.0%**, Huge100k −3.1%.
- No regression: single-stop paths, MatchIbsen suite all ~0 (p>0.26).
- perf stat (pinned): −31% wall, L1 refills 194M→96M, L2 refills 18.5M→6.2M.
- Cost: +12.5% table memory on spread10k (transC 10MB atop 80MB failTrans;
  failTrans kept as canonical/encode source), +3.9% TrieBuild/100k time.
- Single-stop-byte tries skip transC entirely (their specialized path is faster).

### 2. Sorted-children builder (map-free construction)
Replaces per-state map[byte]*state with a value-sorted []*state (linear ≤8,
binary search above) and replaces the per-(state,byte) failure-chain walk with
BFS row-copy: child row := fail-state row (already complete, smaller BFS index)
overwritten at own-child bytes.
- TrieBuild/10000 **−83.5%** (118ms→19.6ms), /100000 **−82.8%** (1.60s→275ms
  incl. transC build); allocs **−27.5%**; B/op −6.8% (10k) / +0.4% (100k, transC).
- Match paths unchanged (~0 across r1/r4).
- Encode output deterministic run-to-run (verified by hash; the integration
  map-order builder was NOT deterministic across runs — this also fixes that
  correctness gap with less code than the sort-keys patch on perf/05).

## REJECT (closed with evidence)

- **Dual-cursor for the multi-stop table path** (EA/EA2, keepdual): real wins on
  some corpora (DenseSpread −38%, GPL −7%) but repeatable regressions on others
  (Midsize1k/Ibsen +13%, spread10k/Ibsen +4% over keep); the state-count gate
  did not separate the regimes (9.7K-state trie regressed). Superseded by
  transC, which wins everywhere it wins and regresses nowhere measured.
- **Premultiplied 32-bit full-row entries** (EB): −2..−3.5% mid, +1% sorted10k;
  subsumed by transC (premultiplies anyway). Not worth a second table format.
- **16-bit rows for multi-stop tries** (EC): −5.6% on one mid bench, ~0
  elsewhere; strictly dominated by transC (4x smaller rows vs 2x, no 2^15 cap).
- **Flattened dictLink output spans** (EF): re-tested on purpose-built
  output-heavy corpora (extreme overlaps, dense words): OutputHeavy_Extreme
  **+24.9%**, MatchIbsen/10000 +5.8%. Chains are short and L1-resident; the
  span indirection loses. Closed for both sparse AND dense corpora now.
- **Multi-stop-byte SWAR root skip, 2–4 bytes** (EG): 2 rare stop bytes −25%,
  3 neutral, 4 **+18%**; win region too narrow (exactly-2-rare-first-bytes
  dictionaries) to justify another skip mode. Documented; rebuildable from
  branch `research/eg-multiswar` if that workload ever matters.
- **Row interning**: measured 87.4% distinct rows (spread10k), 69.9%
  (sorted100k) — nothing to intern. Rejected without building.
- **Rare-byte/shared-byte prefilter**: requires one byte present in every
  pattern; holds only for the sorted-prefix corpus ('a'), never for realistic
  spread dictionaries (measured: empty intersection). The root-skip +
  density gate already covers the viable cases.
- **Hugepage/TLB work**: dTLB walks 11.4M per 4s scan — not the bottleneck
  (L1/L2 refills are); THP=madvise already. transC shrinks reach anyway.

## DEFER (documented, not worth a bounded patch now)

- **Sparse/two-level cold rows** (EI): the real memory lever (94% of states are
  cold), but it changes the row addressing invariant every loop relies on and
  needs an Encode-compat strategy. transC already cut the *hot* footprint 8x.
- **arm64 asm / SIMD match loop** (EK): codegen of the hot loops is already
  minimal (load/and/add chains, no bounds checks — verified via -S and perf
  annotate: 83% of samples on the single L1-load-dependent instruction, i.e.
  latency-bound, not instruction-bound). Asm buys little beyond what
  interleaving already failed to buy portably; Go SIMD is x86-only (1.26).
- **Bufferless Count / iterator API** (EL): public API addition; no evidence of
  need. The zero-alloc arena path already avoids per-match allocation.
- **PGO profile in repo** (EM): +0.7% historically, local-only effect.
- **Double-array trie / hybrid automata**: whole-representation rewrite;
  transC captured the cache-footprint win at ~60 lines.

## Where the throughput now stands (spread10k/Ibsen, the realistic case)
HEAD 02f31eb ≈ 310µs → integration 292µs → **keep-candidate 237µs** (−24% vs
HEAD, −19% vs integration), zero-alloc steady state preserved, build 5–8x
faster, encode deterministic. Sorted-corpus (MatchIbsen) numbers unchanged
from the prior 7.65x-optimized state.

Branches (worktree `aho-research`): keep-candidate (final), plus experiment
branches ea/eb/ec/ee2/eg/eh/ef and keepdual for the rejected/superseded work.
Raw benchstat inputs: aho-bench/r1–r4, build*.txt.
