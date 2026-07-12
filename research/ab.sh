#!/bin/bash
# A/B interleave two prebuilt test binaries: ab.sh <binA> <binB> <outdirprefix> <bench-regex> <count>
set -e
A=$1; B=$2; PRE=$3; RE=$4; CNT=$5
: > "$PRE.a.txt"; : > "$PRE.b.txt"
for i in $(seq 1 "$CNT"); do
  load=$(awk '{print int($1)}' /proc/loadavg)
  while [ "$load" -gt 16 ]; do sleep 5; load=$(awk '{print int($1)}' /proc/loadavg); done
  GOMAXPROCS=8 taskset -c 2-9 "$A" -test.run xxx -test.bench "$RE" -test.benchtime .3s -test.count 1 >> "$PRE.a.txt"
  GOMAXPROCS=8 taskset -c 2-9 "$B" -test.run xxx -test.bench "$RE" -test.benchtime .3s -test.count 1 >> "$PRE.b.txt"
done
