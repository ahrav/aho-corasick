#!/bin/bash
# Interleaved A/B benchmark runner for noisy shared boxes.
# usage: bench.sh <out.txt> <bench-regex> <count> [extra go test args...]
# Runs the CURRENT tree. Pins to CPUs 2-9, GOMAXPROCS=8.
set -e
OUT=$1; RE=$2; CNT=$3; shift 3
: > "$OUT"
for i in $(seq 1 "$CNT"); do
  load=$(awk '{print int($1)}' /proc/loadavg)
  while [ "$load" -gt 16 ]; do sleep 5; load=$(awk '{print int($1)}' /proc/loadavg); done
  GOMAXPROCS=8 taskset -c 2-9 go test -run xxx -bench "$RE" -benchtime .3s -count 1 "$@" >> "$OUT"
done
grep -c "ns/op" "$OUT" || true
