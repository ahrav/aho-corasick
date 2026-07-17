#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "usage: $0 FORK_CHECKOUT UPSTREAM_CHECKOUT OUTPUT_DIR" >&2
    exit 2
}

[[ $# -eq 3 ]] || usage

fork_checkout=$(realpath "$1")
upstream_checkout=$(realpath "$2")
output_dir=$(realpath -m "$3")
runner_path=$(realpath "$0")

fork_revision=1e0b4674b45cdb58dc7dafbf3e91d3f48027a6e3
upstream_revision=b4b5728e36fcc048a77abcea3eb2fcb28c021e2d
benchmark_hash=e936d64744524c24b1e9bfaecebadb1ba416d4491b8ea4638b2fe59790ba42f3
patterns_hash=2d9ad4e5838dc03b438d1881ba52dbb8b6702d9aaf78a979e6a412068e712ae5
text_hash=d5fb85f811c2954ff7bb47d90b72e9140585c99fd2b4e333181de1e2c5a48200

if [[ -d "$output_dir" && -n $(ls -A "$output_dir") ]]; then
    echo "output directory is not empty: $output_dir" >&2
    exit 1
fi
mkdir -p "$output_dir"
: >"$output_dir/commands.log"

check_revision() {
    local checkout=$1
    local expected=$2
    local actual status
    actual=$(git -C "$checkout" rev-parse HEAD)
    [[ "$actual" == "$expected" ]] || {
        echo "$checkout: expected $expected, found $actual" >&2
        exit 1
    }
    status=$(
        git -C "$checkout" status --porcelain=v1 --untracked-files=all |
            sed '/^?? bench_public_test.go$/d'
    )
    [[ -z "$status" ]] || {
        echo "$checkout: unexpected worktree changes:" >&2
        echo "$status" >&2
        exit 1
    }
}

check_hash() {
    local checkout=$1
    local path=$2
    local expected=$3
    local actual
    actual=$(sha256sum "$checkout/$path" | awk '{print $1}')
    [[ "$actual" == "$expected" ]] || {
        echo "$checkout/$path: expected $expected, found $actual" >&2
        exit 1
    }
}

check_checkout() {
    local checkout=$1
    local revision=$2
    check_revision "$checkout" "$revision"
    check_hash "$checkout" bench_public_test.go "$benchmark_hash"
    check_hash "$checkout" test_data/NSF-ordlisten.cleaned.txt "$patterns_hash"
    check_hash "$checkout" test_data/Ibsen.txt "$text_hash"
}

check_checkout "$fork_checkout" "$fork_revision"
check_checkout "$upstream_checkout" "$upstream_revision"
taskset -c 19 true

printf '%s\tsetup\tfork\ttest\tgo test -count=1 ./...\n' \
    "$(date --iso-8601=seconds)" >>"$output_dir/commands.log"
(
    cd "$fork_checkout"
    go test -count=1 ./...
) >"$output_dir/test-fork.txt" 2>&1
printf '%s\tsetup\tfork\tbuild\tgo test -c -trimpath -buildvcs=false -o %q .\n' \
    "$(date --iso-8601=seconds)" "$output_dir/fork.test" \
    >>"$output_dir/commands.log"
(
    cd "$fork_checkout"
    go test -c -trimpath -buildvcs=false -o "$output_dir/fork.test" .
) >>"$output_dir/test-fork.txt" 2>&1

printf '%s\tsetup\tupstream\ttest\tgo test -count=1 ./...\n' \
    "$(date --iso-8601=seconds)" >>"$output_dir/commands.log"
(
    cd "$upstream_checkout"
    go test -count=1 ./...
) >"$output_dir/test-upstream.txt" 2>&1
printf '%s\tsetup\tupstream\tbuild\tgo test -c -trimpath -buildvcs=false -o %q .\n' \
    "$(date --iso-8601=seconds)" "$output_dir/upstream.test" \
    >>"$output_dir/commands.log"
(
    cd "$upstream_checkout"
    go test -c -trimpath -buildvcs=false -o "$output_dir/upstream.test" .
) >>"$output_dir/test-upstream.txt" 2>&1

print_hash() {
    local path=$1
    local label=$2
    local hash
    hash=$(sha256sum "$path" | awk '{print $1}')
    printf '%s  %s\n' "$hash" "$label"
}

{
    date --iso-8601=seconds
    uname -srm
    if [[ -r /sys/devices/virtual/dmi/id/product_name ]]; then
        printf 'instance_type='
        cat /sys/devices/virtual/dmi/id/product_name
    fi
    go version
    go env GOOS GOARCH GOARM64
    printf 'fork_revision=%s\n' "$fork_revision"
    printf 'upstream_revision=%s\n' "$upstream_revision"
    printf 'loadavg='
    cat /proc/loadavg
    lscpu
    taskset -c 19 sh -c "grep '^Cpus_allowed_list:' /proc/self/status"
    print_hash "$fork_checkout/bench_public_test.go" bench_public_test.go
    print_hash \
        "$fork_checkout/test_data/NSF-ordlisten.cleaned.txt" \
        test_data/NSF-ordlisten.cleaned.txt
    print_hash "$fork_checkout/test_data/Ibsen.txt" test_data/Ibsen.txt
    print_hash "$runner_path" run_upstream_benchmark.sh
    print_hash "$output_dir/fork.test" fork.test
    print_hash "$output_dir/upstream.test" upstream.test
} >"$output_dir/environment.txt"

run_benchmark() {
    local arm=$1
    local output=$2
    local benchmark=$3
    local benchtime=$4
    local checkout binary
    if [[ "$arm" == fork ]]; then
        checkout=$fork_checkout
        binary=$output_dir/fork.test
    else
        checkout=$upstream_checkout
        binary=$output_dir/upstream.test
    fi

    printf '%s\t%s\t%s\t%s\t%s\n' \
        "$(date --iso-8601=seconds)" "$arm" "$output" "$benchmark" "$benchtime" \
        >>"$output_dir/commands.log"
    (
        cd "$checkout"
        taskset -c 19 env GOMAXPROCS=1 "$binary" \
            -test.run='^$' \
            -test.bench="$benchmark" \
            -test.benchtime="$benchtime" \
            -test.cpu=1 \
            -test.count=1
    ) >>"$output_dir/$output" 2>&1
}

scan_bench='^(BenchmarkPubText_Spread10k|BenchmarkPubNoMatch|BenchmarkPubDense|BenchmarkPubMatchFirstLate)$'
build_bench='^BenchmarkPubBuild$/^10000$'
large_bench='^BenchmarkPubLarge_Sorted10k$/^8192k$'

run_primary_arm() {
    local stage=$1
    local arm=$2
    local scan_time=$3
    local selector=$4
    run_benchmark "$arm" "$stage-scan-$arm.txt" "$selector" "$scan_time"
    run_benchmark "$arm" "$stage-build-$arm.txt" "$build_bench" 3s
}

run_primary_stage() {
    local stage=$1
    local pairs=$2
    local scan_time=$3
    local selector=$4
    local first second
    : >"$output_dir/$stage-order.tsv"
    for arm in fork upstream; do
        : >"$output_dir/$stage-scan-$arm.txt"
        : >"$output_dir/$stage-build-$arm.txt"
    done
    for ((pair = 1; pair <= pairs; pair++)); do
        if ((pair % 2 == 1)); then
            first=upstream
            second=fork
        else
            first=fork
            second=upstream
        fi
        printf '%d\t%s\t%s\n' \
            "$pair" "$first" "$(date --iso-8601=seconds)" \
            >>"$output_dir/$stage-order.tsv"
        run_primary_arm "$stage" "$first" "$scan_time" "$selector"
        run_primary_arm "$stage" "$second" "$scan_time" "$selector"
    done
}

run_large_stage() {
    local stage=$1
    local pairs=$2
    local first second
    : >"$output_dir/$stage-large1-order.tsv"
    for arm in fork upstream; do
        : >"$output_dir/$stage-large1-$arm.txt"
    done
    for ((pair = 1; pair <= pairs; pair++)); do
        if ((pair % 2 == 1)); then
            first=upstream
            second=fork
        else
            first=fork
            second=upstream
        fi
        printf '%d\t%s\t%s\n' \
            "$pair" "$first" "$(date --iso-8601=seconds)" \
            >>"$output_dir/$stage-large1-order.tsv"
        run_benchmark \
            "$first" "$stage-large1-$first.txt" "$large_bench" 3s
        run_benchmark \
            "$second" "$stage-large1-$second.txt" "$large_bench" 3s
    done
}

run_matchfirst_allocation_check() {
    local output=matchfirst-benchmem-fork.txt
    printf '%s\tfork\t%s\t%s\t1s\tcount=5,benchmem\n' \
        "$(date --iso-8601=seconds)" "$output" '^BenchmarkPubMatchFirstLate$' \
        >>"$output_dir/commands.log"
    (
        cd "$fork_checkout"
        taskset -c 19 env GOMAXPROCS=1 "$output_dir/fork.test" \
            -test.run='^$' \
            -test.bench='^BenchmarkPubMatchFirstLate$' \
            -test.benchtime=1s \
            -test.cpu=1 \
            -test.count=5 \
            -test.benchmem
    ) >"$output_dir/$output" 2>&1
}

# Pilot samples are excluded from final inference.
run_primary_stage pilot 10 500ms "$scan_bench"
run_large_stage pilot 10

# The fixed stopping rule is 31 executions per arm.
run_primary_stage final 31 1s "$scan_bench"
run_large_stage final 31
run_matchfirst_allocation_check

{
    date --iso-8601=seconds
    printf 'loadavg='
    cat /proc/loadavg
} >"$output_dir/environment-end.txt"
