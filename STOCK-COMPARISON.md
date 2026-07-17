# Upstream performance comparison

This compares upstream `BobuSumisu/aho-corasick` at `b4b5728` with this fork
at `1e0b467`. The upstream commit is 12 commits after v1.0.3 (`58861e9`).
Both revisions used the same public-API benchmark source and byte-identical
corpora.

## Results

All primary results use one pinned AWS Graviton3 core. Times are medians
across 31 separate process executions per revision. Reductions are geometric
means of paired time ratios. The table reports Bonferroni-adjusted 99.1667%
percentile-bootstrap intervals, giving nominal 95% simultaneous coverage
across the six endpoints.

| Workload | Upstream median | Fork median | Time reduction (99.1667% CI) |
|---|---:|---:|---:|
| Natural text, spread 10k dictionary, 100 KiB | 464.921 us | 325.286 us | 30.02% (29.95% to 30.09%) |
| No match, spread 10k dictionary, 1 MiB | 2.983 ms | 336.482 us | 88.72% (88.71% to 88.73%) |
| Dense overlapping matches, 64 KiB | 10.626 ms | 897.377 us | 91.51% (91.45% to 91.57%) |
| `MatchFirst`, late match in 100 KiB | 282.567 us | 5.177 us | 98.17% (98.16% to 98.17%) |
| Build 10k-pattern trie | 111.702 ms | 13.124 ms | 88.26% (88.11% to 88.41%) |
| Natural text, sorted 10k dictionary, 8 MiB | 37.780 ms | 8.724 ms | 76.87% (76.81% to 76.92%) |

The reported `Match` scan rows had zero allocations per operation in the fork
and one to four upstream. `MatchFirst` allocation traffic is omitted because
its setup ran before the timed loop without a timer reset, making the
setup-inclusive per-operation values non-comparable. The 10k-pattern build
reported 32 allocations in the fork and about 54,261 upstream.

These numbers describe these workloads on this machine. They are not a suite
geomean or an end-to-end application claim.

## Experiment design

- Machine: AWS `m7g.8xlarge` (Graviton3), Linux arm64.
- Toolchain: Go 1.25.11.
- Experimental unit: one benchmark process execution.
- Pairing: upstream and fork ran in adjacent blocks, alternating order
  `upstream/fork` then `fork/upstream`.
- CPU control: `taskset -c 19`, `GOMAXPROCS=1`, and `-test.cpu=1`.
- Final timing: 1 second for scan benchmarks; 3 seconds for build and the
  8 MiB scan. The excluded pilot used 500 milliseconds for scans.
- Warmup: Go's benchmark calibration ran before each reported measurement.
- Stopping: fixed at 31 executions per revision before final collection.
- Timing exclusions: none. The setup-inclusive `MatchFirst` allocation
  diagnostic is omitted from allocation comparisons.
- Reproduction controls: the current runner rejects ignored checkout files
  and clears inherited build/runtime settings before invoking Go. The archived
  collection predates that hardening; its runner hash and recorded environment
  identify the controls captured at collection time.

A separate 10-pair pilot estimated per-execution CV. Sizing assumed a more
conservative 5% CV, a 5% minimum detectable effect, 90% power, and
`alpha=0.05/6` for six primary endpoints:

```text
n = ceil(2 * (z_(1-alpha/2) + z_(1-beta))^2 * (CV/MDE)^2) = 31
```

Final primary-endpoint sample CV was at most 2.74%. For analysis, each pair
produced `log(fork_time / upstream_time)`. The fork/upstream geometric mean
time ratio is the exponentiated mean log ratio; the reported reduction is
`100 * (1 - exp(mean(log ratio)))`. Intervals use 500,000 paired percentile
bootstrap resamples from NumPy's `Generator(PCG64)`, seed `20260717`, and
linear quantiles. The recorded environment used Python 3.9.25, NumPy 2.0.2,
and SciPy 1.13.1.

At `alpha=0.05`, a Welch test comparing log ratios by first arm did not
detect a statistically significant order effect (`p=0.185` to `0.985`).
Spearman tests did not detect a statistically significant monotonic trend
across pair number (`p=0.308` to `0.952`).

## Integrity

Both revisions passed `go test -count=1 ./...`. The benchmark source, corpus,
build flags, and toolchain were identical. Both binaries used
`-trimpath -buildvcs=false`. The
[raw pilot and primary samples](benchmarks/upstream-20260717/) are included
with the report, including the environment, every command timestamp, and
fresh test output from both revisions.

| Input | SHA-256 |
|---|---|
| `bench_public_test.go` | `e936d64744524c24b1e9bfaecebadb1ba416d4491b8ea4638b2fe59790ba42f3` |
| `test_data/NSF-ordlisten.cleaned.txt` | `2d9ad4e5838dc03b438d1881ba52dbb8b6702d9aaf78a979e6a412068e712ae5` |
| `test_data/Ibsen.txt` | `d5fb85f811c2954ff7bb47d90b72e9140585c99fd2b4e333181de1e2c5a48200` |

## Reproduction

Prepare checkouts at the two revisions above. Add the fork's
`bench_public_test.go` unchanged to the upstream checkout, then run:

```bash
tools/run_upstream_benchmark.sh \
  /path/to/fork-at-1e0b467 \
  /path/to/upstream-at-b4b5728 \
  /tmp/aho-upstream-comparison \
  19
```

The runner verifies revisions and input hashes, records the environment and
commands, executes the excluded 10-pair pilot, then collects 31 final pairs
without invoking the analyzer. The optional final argument selects one logical
CPU and defaults to `19` for this archived run; choose an allowed CPU on other
hosts. The runner validates and records the selected CPU. Analyze the completed
output with:

```bash
python3 tools/analyze_upstream_benchmark.py \
  /tmp/aho-upstream-comparison
```

The committed analyzer defines endpoint ordering, sample CV, paired
percentile bootstrap, PRNG, quantile method, and diagnostic tests.
Its Python dependencies are pinned in `tools/benchmark-requirements.txt`.

`benchstat` provides an independent nonparametric summary:

```bash
benchstat \
  -alpha 0.008333333333333333 \
  -confidence 0.9916666666666667 \
  /tmp/aho-upstream-comparison/final-scan-upstream.txt \
  /tmp/aho-upstream-comparison/final-scan-fork.txt
```
