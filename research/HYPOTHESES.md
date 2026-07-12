# Perf-gap research backlog — written BEFORE coding (evidence standard §1)

Baseline: HEAD 02f31eb (test/differential-fuzz-harness). Sibling worktree `aho-research`,
branch `research/perf-gaps`. Box: 32-core Graviton (Neoverse), Go 1.25.11, SHARED + noisy →
all comparisons interleaved A/B, pinned to CPUs 2-9, GOMAXPROCS=8, benchstat count≥10,
load-gated (<16) before each rep.

Metric suite ("the seven"):
- MatchIbsen/{1000,10000,100000} — sorted10k dict, single-stop-byte path (guard: must not regress)
- RootSkip_RealCorpora/spread10k_multistop/{Ibsen_no,GPL_en} — realistic multi-first-byte dict (matchTable path; PRIMARY TARGET)
- RootSkip_RealCorpora/sorted10k_1stopbyte/Ibsen_no — cross-check
- OutputHeavy/{extreme,moderate} — dictLink emission pressure
Build/memory measured separately: TrieBuild/{10000,100000}, table bytes.

Correctness gates for any KEEP: go test ./...; FuzzMatch 30s; -race for concurrency;
-d=checkptr for unsafe/table-layout changes; exact Match/MatchFirst/Walk semantics incl.
ordering; ReleaseMatches contract; Encode/Decode compat + deterministic serialization.

## E0. Adopt reviewed integration stack (density gate, deterministic BFS, failTrans16 gating, Decode bounds)
Hypothesis: perf/integration (63116c0) content = HEAD + already-reviewed fixes. Gate removes
the known +30% realistic-dict regression of the ungated root skip; deterministic BFS fixes
nondeterministic Encode output (correctness gate!); failTrans16 gating halves build memory on
multi-stop tries. Expect: spread10k/Ibsen improves vs HEAD; sorted paths unchanged.
Risk: none new (reviewed + tested on that branch). KEEP-candidate re-validated here.

## EA. Dual-cursor scan for matchTable (multi-stop realistic path)  [BIGGEST OPEN GAP]
Hypothesis: matchTable's serial load chain (~6 cyc/byte L1) is the bottleneck on realistic
dicts; two interleaved cursors overlap the latency exactly as matchDualStopByte16 did for the
single-stop path (-10% on 10KB there). Expect -15..-35% on spread10k/{Ibsen,GPL} for inputs
≥1KB. Risk: emission-order and midpoint-ownership bugs → differential fuzz; code bloat.
Prior session never tried this (p07 was single-stop only, hence "moot for realistic dicts").

## EB. Premultiplied transition entries: store (state<<10)|flag so the next row offset needs
no shift on the serial chain. 32-bit path only (16-bit can't hold s<<9). States ≤2^21.
Hypothesis: removes 1 ALU op from a ~6-cycle serial chain → up to ~10% on matchTable.
Risk: changes in-memory table encoding → Encode must still write canonical format (encode
from failTrans unchanged if we keep failTrans canonical and add derived table... that doubles
memory; alternative re-derive on encode). Check feasibility; may morph into "derived
premultiplied copy" with memory cost. Measure win first with quick hack.

## EC. matchTable16: 16-bit rows for multi-stop tries ≤2^15 states (+ stop-entry N/A).
Hypothesis: halved row size (512B) → smaller cache footprint helps mid-size realistic dicts
(spread1k=9.7K states, spread3k=26K states). spread10k (78K states) does NOT fit → also
evaluate ED. Expect -5..-15% on spread1k/3k-style dicts. Risk: low (mirror of existing 16-bit
loop). Requires building failTrans16 for multi-stop tries when used (memory +512B/state —
conflicts with p05's gating rationale; make it replace not duplicate? failTrans stays
canonical for Encode).

## ED. 24-bit packed transition entries (flat, 768B rows) for 2^15..2^23-state tries.
Hypothesis: spread10k (78K states, 80MB table) is bigger than L2; 25% smaller rows cut
L2/TLB misses; offset math (s*768) is 2 ALU ops vs 1. Only wins if scan is cache-bound, not
chain-bound. First MEASURE hot-set size + cache misses (perf stat) on spread10k/Ibsen; if
L1-resident, REJECT without building.

## EE. Byte-class compressed table, revisited for BIG tries only.
Prior REJECT (+19%) was on sorted10k (hot set L1-resident). Only viable if ED's measurement
shows cache-bound behavior. Same measurement decides. Expect REJECT with evidence.

## EF. Output-heavy: flattened output spans / per-state output lists, re-tested on a
high-match-density corpus (prior +7.5% REJECT was on 1-match-common corpus).
Hypothesis: with deep dictLink chains (overlapping patterns), a contiguous span of
(len,pattern) pairs per emitting state beats pointer-chasing dictLink+dictPat loads.
Expect -10..-30% on OutputHeavy/extreme, neutral-to-negative on Ibsen suite → maybe
density-adaptive or REJECT.

## EG. Multi-stop-byte SWAR skip for 2-4 stop bytes (currently table-OR path).
Hypothesis: k zero-tests per 8-byte word (k=2..4) beats 8 table loads + OR per 8 bytes at low
stop density. Only matters below gate threshold (~6%) with 2-4 first bytes — narrow corpus
region. Small experiment; likely marginal → close with evidence either way.

## EH. Builder performance + memory: replace per-state map[byte]*state with sorted
slice/small-array children; arena state allocation.
Hypothesis: map alloc/iteration dominates Build; sorted-children also makes BFS numbering
naturally deterministic (needed anyway). Expect -30..-60% TrieBuild, big alloc reduction.
Risk: builder-only, semantics unchanged; deterministic Encode preserved (gate: byte-identical
encode vs sorted-key reference).

## EI. Hot/cold state split or sparse deep rows (memory-focused redesign).
Measure hot-state footprint during realistic scans; if cold rows dominate memory (they do:
distinct-row data shows 87-89% distinct so interning is dead) sparse-cold-rows is the only
big memory lever → likely DEFER (representation redesign, Encode compat work) with evidence.

## EJ. Row interning. CLOSED by measurement before coding: distinct rows = 87.4% (spread10k),
69.9% (sorted100k) → ≤30% dedup at best, plus an extra serial indirection. REJECT.

## EK. arm64 asm / SIMD match loop. Inspect compiler asm for matchTable; if codegen is clean,
DEFER (portability/maintenance; Go SIMD is x86-only in 1.26).

## EL. API-adjacent: bufferless Count, iterator API. DEFER unless free win found (public API).

## EM. PGO default.pgo in repo. Prior +0.7%; local-build only. Low value; note only.
