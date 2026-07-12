
## r4 (final validation, n=12, tight CIs — load was low)
keep (EE byte-class + EH builder) vs integ:
- spread10k/Ibsen −19.0%, spread10k/GPL −16.7%, Midsize1k −15.4%/−12.4%,
  Midsize100 −6.0%, DenseSpread −46.0%, Huge100k −3.1%, DenseWords −1.3%.
- Single-stop paths and MatchIbsen suite: all ~0 (no regression, p>0.26).
- TrieBuild: −83% time both sizes, −27.5% allocs, B/op −6.8%/+0.4%
  (transC pays for itself at 10k; +10MB at spread10k scale = 12.5% of failTrans).
keepdual (dual-cursor over transC) vs keep: MIXED — DenseSpread −37.6%,
GPL −6.7%, OutputHeavy_Extreme −10.4% BUT Midsize1k/Ibsen +13.3%,
spread10k/Ibsen +4.3%, Huge +2.3%. No robust win; state-count gate
insufficient (Midsize1k has 9.7K states and regressed). **REJECT dual-table.**
EA2 standalone r3 (−16% spread10k) is dominated by EE alone (−19% same bench,
plus Midsize wins EA2 lacks). **EA/EA2 closed: superseded by EE.**

## Final gates on keep-candidate tip (0e0dc74 + stride fix)
go test ./... PASS; -race PASS; checkptr PASS (full suite);
FuzzMatch 45s PASS; FuzzEncodeDecode 20s PASS; encode hash deterministic
(2 runs identical, matches EH reference).

## r6 (keep vs keep2 = keep + failTrans16 gating, n=10)
ft16 gating removes 512B/state of never-used table on multi-stop tries ≤2^15
states (spread1k: −5MB; ft16 unusable there since matchSeq's 16-bit paths
require a single stop byte — same rationale as the reviewed p05 change).
Match-loop code is unchanged on every affected path; benchstat shows geomean
+0.43% with a few p<0.05 rows (+1.1% MatchIbsen/10000, +2.6% spread10k/GPL) on
paths whose machine code is IDENTICAL (spread10k never built ft16 at 78K
states) — pure code-layout/alignment noise from the function extraction, a
known hazard on this box. Memory win kept.

## Adversarial audit round 2 (resumed workflow)
- EC "−5.6%" flagged as statistically weak (p=0.023, ±313% CI, 2 outlier reps)
  → caveat added to SUMMARY. HEAD-310µs-baseline provenance flagged → pinned
  to raw data (dab4544:research/e0rc.a.txt, median 309µs) in SUMMARY.
- transC-semantics + builder-correctness + unsafe-bounds review agents that
  completed found no defects; coverage gaps they would have flagged were
  closed directly with TestTransCEquivalence / TestTransCDecodeRebuild /
  TestDegenerateTries (all green incl. -race).
