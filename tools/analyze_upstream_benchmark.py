#!/usr/bin/env python3
"""Analyze the paired upstream comparison benchmark."""

from __future__ import annotations

import argparse
import math
import platform
import re
from dataclasses import dataclass
from pathlib import Path

import numpy as np
import scipy
from scipy import stats


BENCHMARK_LINE = re.compile(
    r"^(?P<name>\S+)\s+\d+\s+(?P<ns>[0-9.]+) ns/op(?:\s|$)"
)


@dataclass(frozen=True)
class Endpoint:
    label: str
    prefix: str
    benchmark: str
    order_file: str


ENDPOINTS = (
    Endpoint(
        "Natural text, spread 10k, 100 KiB",
        "final-scan",
        "BenchmarkPubText_Spread10k/100k",
        "final-order.tsv",
    ),
    Endpoint(
        "No match, spread 10k, 1 MiB",
        "final-scan",
        "BenchmarkPubNoMatch/spread-1m",
        "final-order.tsv",
    ),
    Endpoint(
        "Dense overlaps, 64 KiB",
        "final-scan",
        "BenchmarkPubDense/ab-64k",
        "final-order.tsv",
    ),
    Endpoint(
        "MatchFirst, late match, 100 KiB",
        "final-scan",
        "BenchmarkPubMatchFirstLate",
        "final-order.tsv",
    ),
    Endpoint(
        "Build 10k-pattern trie",
        "final-build",
        "BenchmarkPubBuild/10000",
        "final-order.tsv",
    ),
    Endpoint(
        "Natural text, sorted 10k, 8 MiB",
        "final-large1",
        "BenchmarkPubLarge_Sorted10k/8192k",
        "final-large1-order.tsv",
    ),
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "results",
        nargs="?",
        type=Path,
        default=Path("benchmarks/upstream-20260717"),
    )
    parser.add_argument("--expected-samples", type=int, default=31)
    parser.add_argument("--resamples", type=int, default=500_000)
    parser.add_argument("--seed", type=int, default=20_260_717)
    return parser.parse_args()


def normalized_benchmark_name(name: str) -> str:
    return re.sub(r"-\d+$", "", name)


def read_samples(path: Path, benchmark: str, expected: int) -> np.ndarray:
    samples: list[float] = []
    passes = 0
    for line in path.read_text(encoding="utf-8").splitlines():
        if line == "PASS":
            passes += 1
        match = BENCHMARK_LINE.match(line)
        if match and normalized_benchmark_name(match["name"]) == benchmark:
            samples.append(float(match["ns"]))

    if passes != expected or len(samples) != expected:
        raise ValueError(
            f"{path}: expected {expected} PASS records and samples for "
            f"{benchmark}, found {passes} and {len(samples)}"
        )
    values = np.asarray(samples, dtype=np.float64)
    if np.any(values <= 0):
        raise ValueError(f"{path}: benchmark times must be positive")
    return values


def read_order(path: Path, expected: int) -> np.ndarray:
    order: list[str] = []
    for expected_index, line in enumerate(
        path.read_text(encoding="utf-8").splitlines(), start=1
    ):
        fields = line.split("\t")
        if len(fields) != 3 or int(fields[0]) != expected_index:
            raise ValueError(f"{path}: malformed row {expected_index}")
        if fields[1] not in {"upstream", "fork"}:
            raise ValueError(f"{path}: unknown first arm {fields[1]!r}")
        order.append(fields[1])

    if len(order) != expected:
        raise ValueError(f"{path}: expected {expected} rows, found {len(order)}")
    expected_order = ["upstream" if i % 2 else "fork" for i in range(1, expected + 1)]
    if order != expected_order:
        raise ValueError(f"{path}: arm order does not alternate as planned")
    return np.asarray(order)


def bootstrap_interval(
    log_ratios: np.ndarray,
    *,
    rng: np.random.Generator,
    resamples: int,
    endpoint_alpha: float,
) -> tuple[float, float]:
    means = np.empty(resamples, dtype=np.float64)
    chunk_size = 10_000
    for start in range(0, resamples, chunk_size):
        stop = min(start + chunk_size, resamples)
        indexes = rng.integers(
            0, len(log_ratios), size=(stop - start, len(log_ratios))
        )
        means[start:stop] = log_ratios[indexes].mean(axis=1)

    low_log, high_log = np.quantile(
        means,
        [endpoint_alpha / 2, 1 - endpoint_alpha / 2],
        method="linear",
    )
    return 100 * (1 - math.exp(high_log)), 100 * (1 - math.exp(low_log))


def format_time(ns: float) -> str:
    if ns >= 1_000_000:
        return f"{ns / 1_000_000:.3f} ms"
    if ns >= 1_000:
        return f"{ns / 1_000:.3f} us"
    return f"{ns:.3f} ns"


def main() -> None:
    args = parse_args()
    family_alpha = 0.05
    endpoint_alpha = family_alpha / len(ENDPOINTS)
    rng = np.random.default_rng(args.seed)

    z_significance = stats.norm.ppf(1 - endpoint_alpha / 2)
    z_power = stats.norm.ppf(0.90)
    planned_samples = math.ceil(
        2 * (z_significance + z_power) ** 2 * (0.05 / 0.05) ** 2
    )

    print(
        f"python={platform.python_version()} "
        f"numpy={np.__version__} scipy={scipy.__version__}"
    )
    print(
        f"planned_samples={planned_samples} resamples={args.resamples} "
        f"seed={args.seed}"
    )
    print()
    print("| Workload | Upstream | Fork | Reduction (99.1667% CI) |")
    print("|---|---:|---:|---:|")

    diagnostics: list[tuple[str, float, float, float, float]] = []
    for endpoint in ENDPOINTS:
        upstream = read_samples(
            args.results / f"{endpoint.prefix}-upstream.txt",
            endpoint.benchmark,
            args.expected_samples,
        )
        fork = read_samples(
            args.results / f"{endpoint.prefix}-fork.txt",
            endpoint.benchmark,
            args.expected_samples,
        )
        order = read_order(
            args.results / endpoint.order_file, args.expected_samples
        )

        log_ratios = np.log(fork / upstream)
        reduction = 100 * (1 - math.exp(float(log_ratios.mean())))
        ci_low, ci_high = bootstrap_interval(
            log_ratios,
            rng=rng,
            resamples=args.resamples,
            endpoint_alpha=endpoint_alpha,
        )

        upstream_first = log_ratios[order == "upstream"]
        fork_first = log_ratios[order == "fork"]
        order_p = float(
            stats.ttest_ind(
                upstream_first, fork_first, equal_var=False
            ).pvalue
        )
        trend_p = float(
            stats.spearmanr(
                np.arange(1, len(log_ratios) + 1), log_ratios
            ).pvalue
        )
        upstream_cv = float(upstream.std(ddof=1) / upstream.mean())
        fork_cv = float(fork.std(ddof=1) / fork.mean())
        diagnostics.append(
            (endpoint.label, upstream_cv, fork_cv, order_p, trend_p)
        )

        print(
            f"| {endpoint.label} | {format_time(float(np.median(upstream)))} "
            f"| {format_time(float(np.median(fork)))} | {reduction:.3f}% "
            f"({ci_low:.3f}% to {ci_high:.3f}%) |"
        )

    print()
    print("| Workload | Upstream CV | Fork CV | Order p | Trend p |")
    print("|---|---:|---:|---:|---:|")
    for label, upstream_cv, fork_cv, order_p, trend_p in diagnostics:
        print(
            f"| {label} | {100 * upstream_cv:.3f}% | {100 * fork_cv:.3f}% "
            f"| {order_p:.3f} | {trend_p:.3f} |"
        )


if __name__ == "__main__":
    main()
