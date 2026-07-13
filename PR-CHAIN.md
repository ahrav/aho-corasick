# PR chain review index

One strictly linear chain of 19 stacked branches on top of `master`
(ea4bca2, which already contains the merged perf PRs #3–#10). Each branch
is exactly one commit; each PR targets the branch above it. Every position
passes `go test ./...` on its own; the full chain passes `-race`,
`-gcflags=all=-d=checkptr`, `go vet`, FuzzMatch 120s, FuzzEncodeDecode 60s,
Encode-determinism, and DP-equivalence.

**Whole chain vs master: geomean −41.1%** across the benchmark matrix
(n=8, interleaved A/B binaries, quiet machine, benchstat). Machine for all
numbers: AMD EPYC 9R14 (Zen 4), 48 cores, Go 1.25.11. Per-commit A/B
numbers below are each measured against that commit's direct parent
(n=6–8); the commit messages carry the same numbers plus method notes.

Source lineage legend — *ours*: the perf-lab research line (this stack's
own experiments); *ac-lab*: the independent ac-lab research line
(`lab/RESEARCH-LOG.md` on its branch); *keep-candidate*: the
`research/keep-candidate` branch (Graviton-validated); *emergent*: found
during integration, not in any prior line.

| PR | Branch (commit) | Source | What it does | A/B vs parent |
|----|-----------------|--------|--------------|---------------|
| 1 | `test/08-lab-bench-harness` (f614e9a) | ours | Benchmark matrix used by every A/B below: single/multi-stop, dense, no-match, parallel-crossover, build, density sweeps. Test-only. | — |
| 2 | `perf/09-row-copy-build` (9ea70e1) | ours | Builder: sorted child slices + row-copy construction (row := fail row, overwrite own children) instead of per-(state,byte) fail-chain walks through maps. Deterministic Encode preserved. | build −89% (10k), −95% (100k) |
| 3 | `perf/10-dual-full-gap-skip` (3d8c3a8) | ours ≡ ac-lab (convergent) | Dual-cursor lanes skip whole root gaps (SWAR → IndexByte) per step instead of ≤8 bytes. | text −4…−9% |
| 4 | `perf/11-dual-density-dispatch` (566ec04) | ours | Dual-cursor scan only at ≥10% sampled stop-byte density (3×1KB `bytes.Count` windows); single-cursor wins below it. | text 4k −11%; dense wins kept |
| 5 | `perf/12-parallel-threshold` (70a9854) | ours | Parallel scan from 32KB; 128KB for sparse single-stop input (density-aware via the same sampler). | 32–100KB sparse −25…−36% |
| 6 | `perf/13-16bit-multi-stop` (d7088b3) | ours | `matchTable16`/`walkTable16`/`walkStopByte16`; widens the `failTrans16` gate back to every ≤2^15-state trie (the half-width table is no longer dead weight on multi-stop tries). Changes `TestFailTrans16Gate` semantics — see commit message. | multi small −5…−9%; Walk single −9.5%; MatchFirst −6% |
| 7 | `perf/14-skip-indexbyte-escape` (4d17306) | ours | Root gaps >128B escape the byte-table walk to windowed per-value `bytes.IndexByte` when the trie has 2–4 stop bytes (2KB windows bound the waste). | 2-stop no-match −72%; text ~ |
| 8 | `perf/15-dual-table-scans` (d4ba767) | ours | Dual-cursor for the multi-stop table paths (16/32-bit), dispatched by sampled root-leaving density @~28% (`rootDense`). Adds the whitebox 32-bit differential test. | dense multi −36…−42%; text/no-match ~ |
| 9 | `perf/16-class-table` (6dd7cdd) | ours | Byte-class-compressed table (`failTransC`) for >2^15-state tries, dense-dispatch branch only (on sparse text the classOf load costs ~8%; measured). 185MB→23MB rows at 190k states. | dense multi −23…−39% further |
| 10 | `perf/17-parallel-materialize` (8928e43) | ours ≡ ac-lab M-seg (convergent) | Workers materialize into disjoint arena segments (parallel ≥4096 matches); removes the serial merge-copy + expansion tail. Preserves upstream's pool-hygiene fixes. Race-gated. | dense −39%; 2MB −24% |
| 11 | `perf/18-worker-cap-32` (6dc3977) | ours | Worker cap 8→32 for ≥256KB inputs (scan saturates ~32 workers; only safe after PR 10 removed the serial tail — ordering dependency, see commit). | 2MB −25%; 8MB −32% |
| 12 | `test/19-corpora-benches` (b148e51) | keep-candidate | Adopts its midsize/output-heavy/spread corpora benchmarks + adds large no-match benchmarks. Test-only; later A/Bs use these. | — |
| 13 | `perf/20-skipbit-locate` (d52da2b) | ac-lab, **adapted** | Branchless stop-byte locate in the root-skip loop: keep the cheap OR reject, on hit re-shift lookups into one word + TrailingZeros64 (no scalar re-scan). ac-lab's unconditional form regressed pure-skip input +18% — hybridized. | Midsize −7.5%; spread10k −6.5%; pure-skip ~ |
| 14 | `perf/21-unsafe-walktable` (f2d5fa7) | ac-lab | Raw pointer loads in the 32-bit Walk paths (`walkTable`, `walkStopByte`) under the documented all-entries-valid invariant — the last checked-index scan loops. **The flagged-for-human-review unsafe extension.** checkptr+fuzz gated. | Walk/multi −3.7% (and enabled PR 15's finding) |
| 15 | `perf/22-retire-skip-sampler` (407a6d8) | **emergent** | Removes upstream's root-skip density sampler: PR 13 changed the skip's cost model, and the no-sampler form now wins or ties at *every* point of upstream's own 1–100% density sweep. Keeps a one-lookup gap-0 guard. Retires the sampler white-box test with the type. | Walk/multi −30%; sweeps −2% or ~ |
| 16 | `test/23-anchor-negative-pin` (7a6274b) | ac-lab depth1cont — **REJECTED** | Negative-result pin: ac-lab's inert-pair anchor rejection cost +7.6–8.7% on bushy-depth-1 tries even when gated off, and won only −1.7% in its target regime on this stack (the IndexByte-escalated skip already makes false anchors cheap). `LabAnchor` bench pins the regime; commit message carries the full evidence. Test-only. | — |
| 17 | `perf/24-lively-worker-cap` (334593b) | ac-lab, **adapted** | Full worker cap below 256KB for row-load-bound table scans, gated by `rootLively` content sampling (3×64B windows, ~2% threshold, early-out). ac-lab's unconditional family cap regressed skip-dominated inputs +25…37% — the sampling fixes that. | Midsize −16.5%; spread10k −17.8%; Big −5.7%; skip-dominated ~ |
| 18 | `perf/25-premultiplied-classtable` (f598893) | keep-candidate | `failTransC` entries become premultiplied row offsets with the emit flag in bit 0 — no shift on the serial chain. Neutral on Zen 4 (scaled addressing folds it); kept for keep-candidate's measured −2…−3.5% on Graviton (no scaled-index fold). | ~ on Zen 4 (MultiDense/4k −1.4%) |
| 19 | `perf/26-builder-index-fusion` (b02c432) | ac-lab | Builder: value-struct states in one slice with sorted sibling lists (no per-node allocations), output flags + `failTrans16` fused into the row-copy DP, memmove row copy. `TestDPEquivalence` cross-checks the DP against the reference fail-chain walk. | build −59% further → cumulative 175ms→7.9ms (−95.5%) |

## Review pointers

- **Highest-risk diffs (unsafe / concurrency):** PR 14 (unsafe loads in Walk
  paths — the one item explicitly flagged for a human eye), PR 10 (parallel
  disjoint-segment writes; race-detector gated), PRs 8/9/18 (new unsafe scan
  loops; each covered by the whitebox 32-bit differential test and fuzz).
- **Semantics-adjacent test changes:** PR 6 rewrites `TestFailTrans16Gate`
  (gate widened by design); PR 15 deletes the sampler white-box test (type
  removed) but keeps the density-gate correctness test.
- **Dispatch thresholds** (all speed-only — a misclassification can never
  change output): 10% dual density (PR 4), 32/128KB parallel min (PR 5),
  28% rootDense (PR 8), 128B escape trigger (PR 7), 2% rootLively (PR 17),
  256KB/32-worker cap (PR 11). Each constant's comment carries the measured
  regimes; all tuned on Zen 4 and re-tunable per host without correctness
  risk.
- **Known residual costs** (accepted, documented in commit messages):
  NoMatch/multi +4.9% (dispatch sampling), Skip2/ibsen +9% (±13–18% CI,
  worker-cap trade), MultiStop 1k/4k +1–3% (sampling on small inputs).
- **Cross-line attribution:** convergent results (two research lines
  independently reaching the same change) are marked in Source; adapted
  items name what the adaptation fixed and cite the original line's
  evidence. `COMPARISON.md` holds the full three-way analysis and the
  integration appendix.

## Merge workflow

Push all 19 branches; open PRs bottom-up, each with base = the branch
above (`test/08-lab-bench-harness` → `master`). Merge in order; after each
merge, retarget or rebase the remainder — the same workflow used for
PRs #3–#10. Branch ordinals are unique across `perf/*` and `test/*`
(08–26, no duplicates).
