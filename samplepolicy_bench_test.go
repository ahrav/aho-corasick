package ahocorasick

// samplepolicy_bench_test.go - benchmark matrix for the sampling-based
// dispatch and routing gates of the single-stop-byte 16-bit family:
// looksDense at the parallel gate, the per-chunk dual-vs-single routing
// inside matchParallel workers, and chainSample's size-scaled budget.
//
// The gate-sampled parallel band is narrow. It requires, simultaneously:
//   - a multi-pattern, single-stop-byte automaton with 16-bit rows,
//   - input length in [parallelMin, parallelSparseMin) = [32 KiB, 128 KiB),
//   - a dense whole-input verdict from looksDense (>= ~10% stop bytes).
// Only then does Match take the parallel branch with a sampled verdict;
// workers then route chunk-locally (density below the chain floor, chain
// vote with a density tie-break above it).
//
// Every benchmark asserts its dispatch regime before its timed loop (and
// resets the timer) so a benchmark cannot silently measure the wrong path.

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
)

// spTrie builds the single-stop-byte 16-bit automaton (sorted10k: first
// 10k NSF words) and asserts the automaton shape the experiment needs.
func spTrie(b *testing.B) *Trie {
	patterns, _ := labLoad(b)
	tr := buildStopByte16Trie(b, patterns[:10000])
	if tr.single != nil {
		b.Fatal("want a multi-pattern automaton, got the single-pattern fast path")
	}
	return tr
}

// spGate8 skips benchmarks whose rows pin the 8-worker chunk geometry:
// below GOMAXPROCS=8 the dispatcher hands out fewer, larger chunks
// (96KiB across 4 workers is 24KiB chunks), which take the full
// sampling budget - or cross the chain floor - under row names that
// promise the chunk-scale policy, so constrained runners skip loudly
// instead of reporting numbers for the wrong regime. Sequential
// controls and the direct looksDense/chainSample microbenchmarks have
// no worker-count dependency and stay portable.
func spGate8(b *testing.B) {
	if procs := runtime.GOMAXPROCS(0); procs < 8 {
		b.Skipf("rows pin 8-worker chunk geometry; GOMAXPROCS=%d changes the chunk sizes and sampling budgets they document", procs)
	}
}

// spDenseCorpus returns 256KiB of concatenated dictionary words: the
// stop-byte-dense, excursion-heavy regime (measured ~11-15% density).
func spDenseCorpus(b *testing.B) []byte {
	patterns, _ := labLoad(b)
	return concat(patterns[:10000], 256<<10)
}

// assertRegime pins the dispatch decision the experiment depends on,
// evaluated at the same GOMAXPROCS Match's own dispatch uses (parallel
// groups also call spGate8, so a parallel regime here implies the
// intended 8-worker chunk geometry, not a degenerate 2-4 worker
// split). Passing resets the timer so the assertion's sampling cost
// stays out of short benchtime runs.
func assertRegime(b *testing.B, tr *Trie, input []byte, wantParallel, wantDense, wantKnown bool) {
	b.Helper()
	procs := runtime.GOMAXPROCS(0)
	p, dense, known := tr.parallelWorkersDense(input, procs)
	if (p > 0) != wantParallel || dense != wantDense || known != wantKnown {
		density := float64(bytes.Count(input, []byte{tr.rootStopBytes[0]})) / float64(len(input))
		b.Fatalf("regime mismatch at %dKiB (density %.3f, GOMAXPROCS %d): got p=%d dense=%v known=%v, want parallel=%v dense=%v known=%v",
			len(input)>>10, density, procs, p, dense, known, wantParallel, wantDense, wantKnown)
	}
	b.ResetTimer()
}

// --- Affected band: homogeneous dense input, dispatch path ---------------
//
// Sizes chosen to straddle the chunk-size chain floor
// (dualChainFloor*(maxLen+1024)): at 32-64 KiB chunks are ~8 KiB and each
// worker pays a chunk-local looksDense; at 96-127 KiB chunks are 12-16 KiB
// and workers chain-vote instead (density only breaks ties). 127 KiB stays
// under parallelSparseMin, past which the gate stops sampling entirely.
func BenchmarkSPDenseBand(b *testing.B) {
	spGate8(b)
	tr := spTrie(b)
	corpus := spDenseCorpus(b)
	for _, size := range []int{32 << 10, 48 << 10, 64 << 10, 96 << 10, 127 << 10} {
		input := corpus[:size]
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) {
			assertRegime(b, tr, input, true, true, true)
			benchMatch(b, tr, input)
		})
	}
}

// --- Mis-routing probe: heterogeneous input, dense whole-input verdict ---
//
// Dense (concatenated words) blocks with sparse (digit) blocks placed
// BETWEEN the head/mid/tail windows looksDense samples, so the gate's
// whole-input verdict is dense while individual 8 KiB worker chunks are
// locally sparse - the pocket a whole-input sample structurally misses.
// A worker whose chunk lands in a digit run samples locally and routes
// to the single-cursor scan; these rows guard that chunk-local routing.
func spHeteroInput(b *testing.B, size int, sparseBlocks map[int]bool) []byte {
	dense := spDenseCorpus(b)
	// '0' filler: a digit never continues any pattern of the sorted10k
	// ('a'..'z' words) automaton, so these blocks contain no stop bytes
	// and scan via the vectorized root skip at full speed.
	sparse := bytesFill(size, '0')
	out := make([]byte, 0, size)
	const block = 8 << 10
	for i := 0; len(out) < size; i++ {
		src := dense
		if sparseBlocks[i] {
			src = sparse
		}
		off := (i * block) % (len(src) - block)
		out = append(out, src[off:off+block]...)
	}
	return out[:size]
}

func BenchmarkSPHetero(b *testing.B) {
	spGate8(b)
	tr := spTrie(b)
	// 64 KiB = 8 blocks of 8 KiB; looksDense windows land in blocks
	// 0 (head), 4 (mid), 7 (tail); workers get one block each (p=8).
	// 96 KiB = 12 blocks; windows land in blocks 0, 6, 11; p=8 gives
	// 12 KiB chunks, each spanning 1.5 blocks.
	for _, cfg := range []struct {
		name   string
		size   int
		sparse map[int]bool
	}{
		// Half the chunks locally sparse - maximal mis-routing exposure.
		{"64k-half-sparse", 64 << 10, map[int]bool{1: true, 2: true, 5: true, 6: true}},
		// A quarter sparse - the milder pocket.
		{"64k-quarter-sparse", 64 << 10, map[int]bool{2: true, 5: true}},
		{"96k-third-sparse", 96 << 10, map[int]bool{1: true, 3: true, 8: true, 9: true}},
	} {
		input := spHeteroInput(b, cfg.size, cfg.sparse)
		b.Run(cfg.name, func(b *testing.B) {
			assertRegime(b, tr, input, true, true, true)
			benchMatch(b, tr, input)
		})
	}
}

// --- Controls: paths the policy change must NOT touch --------------------

// Sparse verdict in the sampled band: the sequential fallback reuses the
// gate's sampled verdict (threaded to matchSeq), so this control should
// remain unchanged.
func BenchmarkSPSparseSeqControl(b *testing.B) {
	tr := spTrie(b)
	_, ibsen := labLoad(b)
	for _, size := range []int{48 << 10, 96 << 10} {
		input := ibsen[:size]
		b.Run(fmt.Sprintf("ibsen-%dk", size>>10), func(b *testing.B) {
			assertRegime(b, tr, input, false, false, true)
			benchMatch(b, tr, input)
		})
	}
}

// Past parallelSparseMin the gate never samples (denseKnown=false), so
// workers must keep routing locally in every variant. Guards against the
// plumbing regressing the unsampled large-input path.
func BenchmarkSPLargeControl(b *testing.B) {
	tr := spTrie(b)
	corpus := spDenseCorpus(b)
	_, ibsen := labLoad(b)
	big := make([]byte, 0, 1<<20)
	for len(big) < 1<<20 {
		big = append(big, ibsen...)
	}
	for _, c := range []struct {
		name  string
		input []byte
	}{
		{"dense-256k", corpus[:256<<10]},
		{"ibsen-512k", big[:512<<10]},
	} {
		b.Run(c.name, func(b *testing.B) {
			assertRegime(b, tr, c.input, true, false, false)
			benchMatch(b, tr, c.input)
		})
	}
}

// --- Cost floor: what is a density sample worth at all? ------------------
//
// Direct measurement of looksDense on an 8 KiB chunk (three vectorized
// 1 KiB bytes.Count windows). The wall-clock ceiling of the entire
// optimization is roughly ONE of these (workers sample concurrently), so
// this number bounds the best case before any benchmark runs.
func BenchmarkSPLooksDenseCost(b *testing.B) {
	tr := spTrie(b)
	corpus := spDenseCorpus(b)
	chunk := corpus[:8<<10]
	c := tr.rootStopBytes[0]
	b.SetBytes(int64(len(chunk)))
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if !looksDense(chunk, c) {
			b.Fatal("expected dense chunk")
		}
	}
}

// --- Routing-divergence probe: false-start input --------------------------
//
// Stop byte on every other position, but excursions die at depth 1 (a
// digit never continues any pattern). Density says dense (~50%), so the
// gate dispatches parallel with a dense verdict; above the chain floor
// (96k+, 12-16 KiB chunks) each worker's chain vote says SHORT and
// routes the single-cursor scan, measured 1.4x faster on this shape
// (see dualChainShortMax). Density alone would mis-route every chunk
// dual - these rows guard the chain vote's override. At 64k (8 KiB
// chunks, below the chain floor) routing falls back to density, so that
// row covers the density-routed shape of the same input.
func BenchmarkSPFalseStart(b *testing.B) {
	spGate8(b)
	tr := spTrie(b)
	corpus := spFalseStartCorpus(tr.rootStopBytes[0], 127<<10)
	for _, size := range []int{64 << 10, 96 << 10, 127 << 10} {
		input := corpus[:size]
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) {
			assertRegime(b, tr, input, true, true, true)
			benchMatch(b, tr, input)
		})
	}
}

// --- Sequential small band: chainSample also runs here -------------------
//
// Inputs in [dualChainFloor*(maxLen+1024) ~10.5k, parallelMin 32k) stay
// sequential and chain-sample the whole input. A chunk-scale sampling
// budget change (len < 16 KiB) affects the 12k row and not the 24k row.
func BenchmarkSPSeqSmall(b *testing.B) {
	tr := spTrie(b)
	corpus := spDenseCorpus(b)
	fs := spFalseStartCorpus(tr.rootStopBytes[0], 32<<10)
	for _, c := range []struct {
		name  string
		input []byte
	}{
		{"dense-12k", corpus[:12<<10]},
		{"dense-24k", corpus[:24<<10]},
		{"falsestart-12k", fs[:12<<10]},
	} {
		b.Run(c.name, func(b *testing.B) {
			assertRegime(b, tr, c.input, false, false, false)
			benchMatch(b, tr, c.input)
		})
	}
}

// --- Gray zone: separator-broken chains, dense verdict -------------------
//
// Words separated by short 'x' runs: stop-byte density stays above the
// gate's dense bar but separators cut excursions short, so the chain
// vote sits in the gray band where sampling-budget changes could flip
// windows. gap picked at setup so the whole input still gates dense.
func BenchmarkSPGray(b *testing.B) {
	spGate8(b)
	patterns, _ := labLoad(b)
	tr := spTrie(b)
	for _, gap := range []int{2, 3} {
		var sb []byte
		i := 0
		for len(sb) < 127<<10 {
			sb = append(sb, patterns[i%10000]...)
			for g := 0; g < gap; g++ {
				sb = append(sb, 'x')
			}
			i++
		}
		input := sb[:96<<10]
		b.Run(fmt.Sprintf("gap%d-96k", gap), func(b *testing.B) {
			assertRegime(b, tr, input, true, true, true)
			benchMatch(b, tr, input)
		})
	}
}

// spFalseStartCorpus returns n bytes alternating the stop byte with
// digits: maximal stop-byte density with excursions that die at depth 1.
func spFalseStartCorpus(c byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = c
		} else {
			out[i] = byte('0' + (i/2)%10)
		}
	}
	return out
}

// BenchmarkSPChainSampleCost times chainSample itself per corpus shape.
// The 12k rows take the reduced small-input budget, the 16k+ rows the
// full one. This is the uncontended per-call cost - a floor, not a
// bound on the parallel critical path: on a dense-verdict dispatch
// every worker walks its sample concurrently, where the dependent
// table loads run several-fold slower under shared-LLC contention
// (~5x at 8 workers measured on Graviton3).
func BenchmarkSPChainSampleCost(b *testing.B) {
	tr := spTrie(b)
	corpus := spDenseCorpus(b)
	_, ibsen := labLoad(b)
	falsestart := spFalseStartCorpus(tr.rootStopBytes[0], 96<<10)
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"wordlike-12k", corpus[:12<<10]},
		{"wordlike-16k", corpus[:16<<10]},
		{"wordlike-32k", corpus[:32<<10]},
		{"falsestart-12k", falsestart[:12<<10]},
		{"ibsen-12k", ibsen[:12<<10]},
	} {
		b.Run(c.name, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				long, short := tr.chainSample(c.data)
				_ = long + short
			}
		})
	}
}
