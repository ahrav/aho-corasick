#!/usr/bin/env python3

import tempfile
import unittest
from pathlib import Path

import analyze_upstream_benchmark as analyzer


class ReadMetricTests(unittest.TestCase):
    def test_rejects_misdistributed_samples_across_process_blocks(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "benchmark.txt"
            path.write_text(
                "BenchmarkExample-8 1 10 ns/op\n"
                "BenchmarkExample-8 1 20 ns/op\n"
                "PASS\n"
                "PASS\n",
                encoding="utf-8",
            )

            with self.assertRaisesRegex(
                ValueError,
                r"process block 1 ended at line 3 with 2 samples",
            ):
                analyzer.read_metric(path, "BenchmarkExample", 2, "ns")


if __name__ == "__main__":
    unittest.main()
