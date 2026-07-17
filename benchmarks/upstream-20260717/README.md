# Benchmark evidence

These files support `STOCK-COMPARISON.md`.

- `pilot-*`: 10 exploratory executions per revision, excluded from inference.
- `final-*`: 31 primary executions per revision.
- `pilot-order.tsv` and `pilot-large1-order.tsv`: first arm and timestamp for
  each excluded pilot block.
- `final-order.tsv` and `final-large1-order.tsv`: first arm and timestamp for
  each paired block.
- `analysis.txt`: output from the committed analyzer.
- `commands.log`: timestamp and parameters for every test, build, and
  benchmark process.
- `environment.txt` and `environment-end.txt`: host, toolchain, affinity,
  hashes, and start/end load.
- `matchfirst-benchmem-fork.txt`: separate allocation check for `MatchFirst`.
- `test-fork.txt` and `test-upstream.txt`: fresh `go test -count=1 ./...`
  output from the measured revisions.

Recompute the report with:

```bash
python3 tools/analyze_upstream_benchmark.py \
  benchmarks/upstream-20260717
```
