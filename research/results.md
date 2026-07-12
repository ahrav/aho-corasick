
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
