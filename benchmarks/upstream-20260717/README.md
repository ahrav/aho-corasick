# Benchmark evidence

These files support `STOCK-COMPARISON.md`.

- `pilot-*`: 10 exploratory executions per revision, excluded from inference.
- `final-*`: 31 primary executions per revision.
- `pilot-order.tsv` and `pilot-large1-order.tsv`: first arm and timestamp for
  each excluded pilot block.
- `final-order.tsv` and `final-large1-order.tsv`: first arm and timestamp for
  each paired block.
- `analysis.txt`: output from the committed analyzer.
- `commands.log`: timestamps and parameters for the main collection's test,
  build, and benchmark processes.
- `environment.txt` and `environment-end.txt`: host, toolchain, affinity,
  hashes, and start/end load. The runner hash identifies the exact collection
  script. The current runner additionally rejects ignored files and clears and
  records inherited Go/runtime settings; the archived collection predates
  those controls.
- `matchfirst-benchmem-fork.txt`: one setup-inclusive diagnostic invocation
  with five in-process repetitions. It is excluded from the allocation report
  because the benchmark did not reset its timer after setup.
- `test-fork.txt` and `test-upstream.txt`: fresh `go test -count=1 ./...`
  output from the measured revisions.

Recompute the report with:

```bash
python3 tools/analyze_upstream_benchmark.py \
  benchmarks/upstream-20260717
```
