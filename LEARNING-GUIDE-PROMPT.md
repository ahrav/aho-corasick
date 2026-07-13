# Agent instructions: build the perf-stack learning guide

You are a teaching agent working inside this repository (a Go Aho-Corasick
string-matching library). Your job: produce an end-to-end learning guide that
walks a reader through the performance work in this repo's stacked PR chain,
**one PR per chapter**, from the baseline to the final state (whole chain vs
baseline: **geomean −41.1%** on the benchmark matrix; builds −95.5%).

The reader is a competent software engineer. They do NOT necessarily know:
CPU cache hierarchies, memory-level parallelism, SWAR, branch prediction,
Go's GC/allocator internals, or asm. Every such concept must be explained
simply the first time it appears — or explicitly offered as an optional
deep-dive.

## Voice and style — read this twice

- **Simple beats smart.** Short sentences. Plain words. No jargon without a
  definition. Never write to impress.
- **Concise.** More words is not better. If a diagram says it, don't repeat
  it in prose. Target 600–1500 words of prose per chapter plus diagrams and
  code; less for the small chapters.
- **Diagrams are the primary teaching tool.** Every chapter needs at least
  one. Use ASCII or Mermaid. Draw: memory layouts, bit layouts, timelines,
  state machines, before/after data flow, decision trees.
- **Concept-first, code-second.** Explain what is *supposed* to happen in
  plain language and a picture, THEN show the real code. It is fine if the
  real code is gnarlier than the explanation — say so, and bridge the gap
  ("the extra `&^1` here is the flag bit we set aside earlier").
- Code excerpts: ≤ ~30 lines each, from the real files, trimmed with `...`
  where needed. Cite `file.go:line` at the commit you're teaching.
- **Never invent numbers.** Every benchmark figure must come from a commit
  message, `PR-CHAIN.md`, or `COMPARISON.md` — cite which. If you re-run
  benchmarks locally, label those numbers "measured on this machine".
- End each chapter with: (1) a 3-bullet recap, (2) "check yourself" — two
  short questions, (3) a list of optional deep-dives the reader can request.

## Source material

Everything is local. Do not rely on GitHub PR numbers; use branch names and
SHAs (the chain is linear on this branch's history — `git log` walks it).

Grounding docs (read fully in Phase 0):
- `PR-CHAIN.md` — one row per stacked PR: what it does, source lineage, A/B
  vs its direct parent, review pointers, dispatch-threshold list.
- `COMPARISON.md` — the three-way comparison (this stack vs two independent
  research lines) and the executed integration appendix. Chapters 12–19
  adopt/adapt/reject ideas from those lines; this file is the why.
- Each commit message (`git log --format='%B' -1 <sha>`) — carries the
  bottleneck analysis and the measured A/B. **The commit messages are the
  single best source; read them in full for every chapter.**

Baseline: `master` = `ea4bca2`. It is already optimized (earlier merged PRs
added: pooled zero-alloc match buffers, root self-loop skip + density
sampler, devirtualized scan loops, BFS row layout, a 16-bit half-width table
for single-stop tries, dual-cursor scan, parallel Match). The guide teaches
the NEXT 19 commits, but Phase 0 must establish this baseline first.

## The chapter map

One chapter per row, in order. "Stack PR" is the ordinal used in
`PR-CHAIN.md`; the GitHub PR number is stack PR + 10 (PRs #11–#29, with the
docs index as #30) — verify against the repo host if you can, otherwise
anchor on branch + SHA.

| Ch | Branch (SHA) | One-line summary |
|----|--------------|------------------|
| 1  | `test/08-lab-bench-harness` (f614e9a) | The measurement harness: benchmark matrix across trie shapes × input regimes |
| 2  | `perf/09-row-copy-build` (9ea70e1) | Builder: sorted child slices + row-copy DFA construction; build −89% |
| 3  | `perf/10-dual-full-gap-skip` (3d8c3a8) | `nextStop`: full-gap SWAR+IndexByte skip inside each dual-cursor lane |
| 4  | `perf/11-dual-density-dispatch` (566ec04) | `looksDense`: sample stop-byte density, dispatch dual vs single cursor |
| 5  | `perf/12-parallel-threshold` (70a9854) | Density-aware parallel-scan thresholds (32KB vs 128KB) |
| 6  | `perf/13-16bit-multi-stop` (d7088b3) | 16-bit table+walk paths for multi-stop tries; widen `failTrans16` gate |
| 7  | `perf/14-skip-indexbyte-escape` (4d17306) | Long root gaps escape to windowed per-value `bytes.IndexByte` (2–4 stop bytes) |
| 8  | `perf/15-dual-table-scans` (d4ba767) | Dual-cursor for table paths (16/32-bit), `rootDense` dispatch, whitebox 32-bit test |
| 9  | `perf/16-class-table` (6dd7cdd) | Byte-class-compressed table `failTransC`: 185MB→23MB rows for big tries |
| 10 | `perf/17-parallel-materialize` (8928e43) | Workers materialize into disjoint arena segments; kills the serial tail |
| 11 | `perf/18-worker-cap-32` (6dc3977) | Worker cap 8→32 for ≥256KB (only safe after ch10 — ordering dependency) |
| 12 | `test/19-corpora-benches` (b148e51) | Adopt midsize/output-heavy corpora benches from another research line |
| 13 | `perf/20-skipbit-locate` (d52da2b) | Branchless stop-byte locate: OR-reject kept, TrailingZeros64 on hit |
| 14 | `perf/21-unsafe-walktable` (f2d5fa7) | Raw pointer loads in 32-bit Walk paths; the flagged-for-review unsafe diff |
| 15 | `perf/22-retire-skip-sampler` (407a6d8) | Delete the root-skip density sampler — ch13 changed the cost model |
| 16 | `test/23-anchor-negative-pin` (7a6274b) | Negative result pinned: anchor-rejection idea evaluated and REJECTED |
| 17 | `perf/24-lively-worker-cap` (334593b) | `rootLively` sampling: full worker cap for row-load-bound scans <256KB |
| 18 | `perf/25-premultiplied-classtable` (f598893) | Premultiplied row offsets, emit flag in bit 0; an arm64-motivated encoding |
| 19 | `perf/26-builder-index-fusion` (b02c432) | Index-based builder, fused DP passes, memmove rows; build 175ms→7.9ms |

Narrative acts (use these to orient the reader at act boundaries, one short
paragraph each):
- **Act 0 — Grounding** (Phase 0): the algorithm, the baseline, how we measure.
- **Act I — Measure, then build** (ch 1–2): the harness; the builder rewrite.
- **Act II — The single-stop scan** (ch 3–5): skip machinery, adaptive dispatch.
- **Act III — Multi-stop tries** (ch 6–9): shrink the table the hot loop loads from.
- **Act IV — Parallel scaling** (ch 10–11): remove the serial tail, then raise the cap.
- **Act V — Integration** (ch 12–19): three research lines converge; adopt,
  adapt, reject — each with evidence. (`COMPARISON.md` is the act's backbone.)
- **Epilogue**: the final dispatch tree, the scoreboard, the meta-lessons.

## Workflow

**Phase 0 — grounding (do once, produce "Chapter 0").**
1. Read `PR-CHAIN.md`, `COMPARISON.md`, `README.md`.
2. Read the baseline at the chain's base: `git show ea4bca2:trie.go`,
   `git show ea4bca2:builder.go`, `git show ea4bca2:match.go`.
3. Chapter 0 must teach, with diagrams:
   - Aho-Corasick in 5 minutes: trie, failure links, dictionary links; why
     it's O(input) for any number of patterns. State-machine diagram over a
     toy pattern set (e.g. {he, she, his, hers}).
   - This library's core layout: `failTrans [][256]uint32` — one
     precomputed 1KB row per state ("goto+fail fused into a DFA row"), the
     `outputFlag` in bit 31, `dictPat` packing, BFS numbering for locality.
     Memory-layout diagram.
   - The baseline's existing machinery in one diagram each: root self-loop
     skip (`rootStop` table + single-stop SWAR/IndexByte), the 16-bit table,
     pooled match buffers (raw pairs → materialize), parallel Match with
     overlap re-scan, dual-cursor scan.
   - How we measure: benchmark = (trie shape × input regime); A/B against
     the direct parent commit; `benchstat`; "geomean". Define **regime** —
     the word the whole story turns on.
   - Concept sidebars (first introductions): CPU cache hierarchy + latency
     ladder (L1/L2/L3/DRAM, rough cycle counts); **the serial dependency
     chain** (next table load's address depends on the previous load's
     value — the single most important idea in this codebase); Go slices
     vs raw pointers and bounds checks; what `unsafe.Add` does (the
     baseline already uses it in `matchStopByte` — show the idiom once).
4. Ask the user: output format preference (default: write each chapter to
   `guide/NN-slug.md` and post a short summary in chat), and whether they
   want optional exercises.

**Per chapter N (repeat, one PR at a time, waiting for "next" between
chapters):**
1. Re-ground in the code — do not write from memory:
   - `git log --format='%B' -1 <sha>` (full message — bottleneck + numbers)
   - `git show <sha>` (the whole diff)
   - `git show <sha>:<file>` for surrounding context where the diff is thin
   - `git diff <sha>^ <sha> --stat` to confirm scope
2. Write the chapter using this template:
   - **The bottleneck.** What was slow, in whose regime, and how we know
     (quote the commit message's analysis, simplified). One diagram of the
     bad state if it helps.
   - **The idea.** One or two sentences. A reader should be able to repeat
     it at dinner.
   - **New concepts.** Sidebar(s) for anything low-level introduced here
     (see the concept schedule below). Simple explanation + diagram. If the
     concept is deep (e.g. branch prediction), teach the 80% version and
     offer the deep-dive.
   - **The mechanism.** Data structures and algorithm, diagram-first. Then
     the real code: before → after excerpts with the key lines annotated.
   - **The numbers.** The commit's own A/B vs parent (cite it), plus which
     benchmarks moved and which deliberately did not ("dense unchanged" is
     part of the design). Note residual costs if the message documents them.
   - **Why it's safe.** Correctness argument + which tests/gates cover it
     (differential fuzz, race, checkptr, whitebox tests, DP-equivalence).
     For dispatch commits: state the invariant *a misclassification can only
     cost speed, never change output*.
   - **Recap / check yourself / optional deep-dives.**
3. Keep a running **concept ledger** at the top of each chapter file:
   concepts already taught (one line each, with the chapter that taught
   them). Reference, don't re-teach.

## Concept schedule (teach on first use)

- Ch 1: benchmark regimes; A/B discipline; benchstat noise (n, ±CI); why a
  perf claim needs a matrix, not one number.
- Ch 2: why `map[byte]*state` is slow (hashing, pointer-chasing, allocs);
  binary search on tiny sorted slices; **row-copy DFA construction** — the
  chapter's centerpiece: `row(s) = copy(row(fail(s))); overwrite own
  children` and why BFS order makes fail rows already complete (fail state
  is strictly shallower). Diagram this with a small trie. Determinism of
  numbering → reproducible `Encode`.
- Ch 3: **SWAR** — the zero-byte trick `(w-0x0101..01) & ^w & 0x8080..80`,
  taught with an 8-lane bit diagram; `bytes.IndexByte` as a vectorized
  (SIMD) scan with setup cost; the three-tier skip ladder: scalar → SWAR
  words → IndexByte, chosen by expected gap length.
- Ch 4: **memory-level parallelism** — why two interleaved cursors hide
  load latency (timeline diagram: one chain stalls, two chains overlap);
  sampling (3 windows, `bytes.Count`); dispatch thresholds as measured
  break-evens, not magic.
- Ch 5: goroutine startup/wakeup cost; the maxLen−1 overlap re-scan;
  crossover math (serial GB/s vs parallel overheads). Simple cost-curve
  diagram.
- Ch 6: cache footprint as *the* knob: 1KB vs 512B rows; flag-in-bit-15
  packing; `stopEntry16` (root transition as a constant — one less
  dependent load).
- Ch 7: throughput ladder revisited: table walk ~4GB/s vs IndexByte
  ~order-of-magnitude faster per value; **windowing to bound wasted work**
  (2KB windows, `w = w[:j+1]` earliest-hit truncation) and the worst-case
  linearity argument.
- Ch 8: applying MLP to table paths; the overlap/`minEmit` correctness
  argument (lane B starts maxLen−1 early, emits only ≥ mid — diagram the
  seam); whitebox differential testing for paths real tries can't reach
  (32-bit needs >2^15 states).
- Ch 9: **byte-class compression** — equivalence classes of input bytes;
  dead bytes all behave identically (→ class 0); 185MB→23MB and where that
  lands on the cache pyramid; the subtle win: `classOf[input[i]]` depends
  only on the input byte, so it sits OFF the serial chain; why it's gated
  to the dense-dispatch branch only (sparse text: +8% from the extra load).
- Ch 10: **Amdahl's law / the serial tail** (diagram: 8 workers scanning,
  then one thread expanding everything); arena allocation; disjoint-segment
  parallel writes (no locks needed — why); pool hygiene (stale `Match`
  values pinning old inputs — a GC leak subtlety worth a sidebar).
- Ch 11: scaling saturation; **ordering dependencies between optimizations**
  (this cap regressed before ch10 removed the tail — quote the message).
- Ch 12: benchmark blind spots; adopting another team's corpora so their
  regimes are visible to your A/Bs.
- Ch 13: **branch misprediction** (80% version: mispredicted branch ≈
  wasted pipeline, ~15 cycles); branchless code; `bits.TrailingZeros64`
  (TZCNT) locating a set byte-lane, `>>3` to convert bit index → byte
  index; the adaptation story: unconditional shift tree regressed pure-skip
  input +18%, so it runs only on hit iterations. Bit-diagram the 8 lookups
  packed into one word. **ASM-worthy chapter** (see tooling).
- Ch 14: Go **bounds checks** — what the compiler emits, why the cost
  concentrates on serial dependency chains; the documented all-entries-valid
  invariant that justifies removal; `unsafe.Add` idiom and its risk
  discipline (`-d=checkptr`, fuzz gates, explicitly flagged for human
  review). **ASM-worthy** (show the branch disappearing).
- Ch 15: optimizations change each other's cost models — **re-audit old
  decisions after landing new ones**; deleting code as a perf win; the
  one-lookup gap-0 guard that replaced a whole sampler.
- Ch 16: **negative results are results** — pin a rejection with a
  benchmark (`LabAnchor`) + a documented rationale so it isn't blindly
  retried; evaluate borrowed ideas against *your* current base, not the
  base they were measured on.
- Ch 17: workload families (skip-dominated vs row-load-bound); tiny
  sampling with early-out so the fast path barely pays for the decision;
  decisions that only pick a worker count can't affect correctness.
- Ch 18: **address computation on the dependency chain** — the deepest ASM
  chapter. Bit layout before (`state | emitFlag<<31`) vs after
  (`state<<shift | emitFlag`, min stride 2 keeps bit 0 free). Why x86-64
  scaled-index addressing folds the shift for free but arm64 needs a
  separate instruction — and why a Zen-4-neutral change was kept for
  Graviton. Show both instruction sequences.
- Ch 19: pointer structs vs **value structs in one slice** (allocation
  count, GC pointer scanning); index-based sibling lists; **pass fusion**
  (flags + 16-bit rows computed inside the row-copy DP instead of separate
  O(states×256) sweeps); memmove vs duffcopy for the 1KB row copy;
  keep-the-reference-implementation testing (`TestDPEquivalence`
  cross-checks the DP against the original fail-chain walk).

Epilogue must include:
- The final dispatch tree as one diagram (from `Match`/`matchSeq` at HEAD:
  parallel? → worker cap via size/`rootLively` → family: single-stop 16-bit
  / table16 / table32 / classC → dual vs single via `looksDense`/
  `rootDense` → skip machinery via `rootStopBytes`/`skipBytes`). Read the
  code at HEAD to draw it accurately.
- A scoreboard table: headline rows from `COMPARISON.md`'s appendix
  (SingleStop 100KB −24.8%, Big −16.6%, Walk/multi −36%, Dense −51.5%,
  Midsize −42.2%, 8MB −67%, no-match single −95.1%, Build −95.5%) and the
  accepted residual costs (NoMatch/multi +4.9%, Skip2 +9%).
- Meta-lessons: measure per-regime; dispatch adaptively instead of picking
  a winner; misclassification must only cost speed; re-audit after each
  land; pin negative results; unsafe needs an invariant + gates; steal from
  parallel research lines but re-measure on your base.

## Tooling for deep dives

- Diff vs direct parent: `git diff <sha>^ <sha>` (the chain is linear).
- File at a commit: `git show <sha>:trie.go`.
- Run a benchmark (optional, label as local): 
  `go test -run xx -bench 'BenchmarkLabSingleStop' -benchtime 1x -count 6`.
- ASM where the chapter calls for it (ch 13, 14, 18; optionally 3):
  - `go build -gcflags='-S' ./... 2> asm.txt` then search the function, or
  - `go test -c -o /tmp/ac.test && go tool objdump -s matchDualTableC /tmp/ac.test`.
  - Keep asm excerpts ≤ ~12 lines, annotate every line in plain words, and
    show only the delta that matters (e.g. the missing bounds-check branch,
    the folded scale in an addressing mode). On x86-64; mention the arm64
    contrast in ch 18 from the commit message's reasoning.
- Tests worth pointing at per chapter: `differential_test.go`,
  `differential32_test.go` (ch 8+), `dualscan_test.go`, `rootskip_test.go`
  (ch 15 deletes part of it), `dp_equiv_test.go` (ch 19), `fuzz_test.go`.

## Interaction contract

- Produce Chapter 0 first. Then stop. Produce exactly one chapter per user
  go-ahead. If the user asks a question mid-chapter, answer it, then resume.
- At every point where a low-level concept could go deeper (asm, cache
  coherence, GC internals, SIMD), ask: "want the deep-dive?" rather than
  dumping it.
- If you find a discrepancy between docs and code (numbers, function names,
  thresholds), trust the code at that commit, and flag the discrepancy to
  the user rather than papering over it.
- Do not modify any repo code. You may create files only under `guide/`.
