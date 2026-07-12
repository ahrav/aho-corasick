package ahocorasick

// Lab benchmark matrix. Fixed across all experiments; committed once.
// Covers the paths the upstream benchmarks miss: multi-stop-byte tables,
// no-match skipping, dense outputs, Walk, MatchFirst, and the 100k-pattern
// 32-bit automaton.

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"math/rand"
	"testing"
)

var labState struct {
	loaded   bool
	patterns []string
	ibsen    []byte
}

// labLoad returns the shared corpus, reading it from disk on first use.
// The cache makes later calls cheap but means the first timed invocation
// of a run would include file I/O: benchmarks that time work in their
// own body (no sub-benchmarks) must call b.ResetTimer() after setup.
func labLoad(b *testing.B) ([]string, []byte) {
	if !labState.loaded {
		p, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
		if err != nil {
			b.Fatal(err)
		}
		ib, err := ioutil.ReadFile("./test_data/Ibsen.txt")
		if err != nil {
			b.Fatal(err)
		}
		labState.patterns = p
		labState.ibsen = ib
		labState.loaded = true
	}
	return labState.patterns, labState.ibsen
}

// stride10k picks every 10th pattern: spreads first bytes across the
// alphabet so the automaton has many root stop bytes. The result is
// large (~64k states on the NSF corpus, above the 2^15 failTrans16
// limit, so 32-bit rows only); BenchmarkLabMultiSmall covers the
// 16-bit-eligible multi-stop shape.
func stride10k(patterns []string) []string {
	out := make([]string, 0, 10000)
	for i := 0; i < len(patterns) && len(out) < 10000; i += 10 {
		out = append(out, patterns[i])
	}
	return out
}

func benchMatch(b *testing.B, tr *Trie, input []byte) {
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		ms := tr.Match(input)
		tr.ReleaseMatches(ms)
	}
}

// benchMatchAt times the scan at a fixed worker count, bypassing Match's
// input-size/GOMAXPROCS dispatch. matchParallel with p=1 spawns no
// goroutines and runs a single matchSeq over the whole input, so it is
// the forced-sequential case; p>1 forces exactly p workers. This lets
// the crossover sweeps pit both implementations against each other at
// every size — including below Match's 16 KiB dispatch floor — with
// results that do not depend on the host's GOMAXPROCS.
func benchMatchAt(b *testing.B, tr *Trie, input []byte, p int) {
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		ms := tr.matchParallel(input, p)
		tr.ReleaseMatches(ms)
	}
}

// Single-stop-byte automaton (sorted 10k prefix), 16-bit paths.
func BenchmarkLabSingleStop(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	b.Run("ibsen-1k", func(b *testing.B) { benchMatch(b, tr, ibsen[:1000]) })
	b.Run("ibsen-4k", func(b *testing.B) { benchMatch(b, tr, ibsen[:4000]) })
	b.Run("ibsen-100k", func(b *testing.B) { benchMatch(b, tr, ibsen[:100000]) })
}

// Multi-stop-byte automaton, >2^15 states: 32-bit matchTable rows. (See
// BenchmarkLabMultiSmall for the small 16-bit-eligible multi-stop shape.)
func BenchmarkLabMultiStop(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(stride10k(patterns)).Build()
	if len(tr.rootStopBytes) == 1 || len(tr.failTrans) <= 1<<15 {
		b.Fatalf("want large multi-stop automaton, got states=%d stops=%d",
			len(tr.failTrans), countStop(tr))
	}
	b.Run("ibsen-1k", func(b *testing.B) { benchMatch(b, tr, ibsen[:1000]) })
	b.Run("ibsen-4k", func(b *testing.B) { benchMatch(b, tr, ibsen[:4000]) })
	b.Run("ibsen-100k", func(b *testing.B) { benchMatch(b, tr, ibsen[:100000]) })
}

// 100k patterns: >2^15 states, only 32-bit rows possible.
func BenchmarkLabBig(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:100000]).Build()
	b.Run("ibsen-100k", func(b *testing.B) { benchMatch(b, tr, ibsen[:100000]) })
}

// No-match inputs: all scanning is root skipping.
func BenchmarkLabNoMatch(b *testing.B) {
	patterns, _ := labLoad(b)
	rng := rand.New(rand.NewSource(7))
	input := make([]byte, 100000)
	for i := range input {
		input[i] = byte('0' + rng.Intn(10)) // digits never start NSF words
	}
	trSingle := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	trMulti := NewTrieBuilder().AddStrings(stride10k(patterns)).Build()
	b.Run("single-stop-100k", func(b *testing.B) { benchMatch(b, trSingle, input) })
	b.Run("multi-stop-100k", func(b *testing.B) { benchMatch(b, trMulti, input) })
}

// Dense outputs: short overlapping patterns over matching text.
// Every position emits at least one match; stresses the emit path.
func BenchmarkLabDense(b *testing.B) {
	tr := NewTrieBuilder().AddStrings([]string{"a", "ab", "aba", "abab", "b", "ba"}).Build()
	input := make([]byte, 65536)
	for i := range input {
		if i%2 == 0 {
			input[i] = 'a'
		} else {
			input[i] = 'b'
		}
	}
	b.Run("ab-64k", func(b *testing.B) { benchMatch(b, tr, input) })
}

// Walk on the flagship automaton (callback API, 32-bit rows today).
func BenchmarkLabWalk(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	trMulti := NewTrieBuilder().AddStrings(stride10k(patterns)).Build()
	input := ibsen[:100000]
	b.Run("single-100k", func(b *testing.B) {
		b.SetBytes(int64(len(input)))
		var cnt int
		for n := 0; n < b.N; n++ {
			tr.Walk(input, func(end, l, p uint32) bool { cnt++; return true })
		}
	})
	b.Run("multi-100k", func(b *testing.B) {
		b.SetBytes(int64(len(input)))
		var cnt int
		for n := 0; n < b.N; n++ {
			trMulti.Walk(input, func(end, l, p uint32) bool { cnt++; return true })
		}
	})
}

// MatchFirst with the match late in the input (worst case for the
// callback-based implementation).
func BenchmarkLabMatchFirstLate(b *testing.B) {
	_, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddString("imorges").Build() // first occurrence at byte 99805
	input := ibsen[:100000]
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if m := tr.MatchFirst(input); m == nil {
			b.Fatal("expected a match")
		}
	}
}

// Build cost on the 10k set (allocation-heavy today).
func BenchmarkLabBuild10k(b *testing.B) {
	patterns, _ := labLoad(b)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	}
}

// Parallel crossover: same automaton, growing inputs. Per size, the
// "dispatch" run measures the production Match policy (sequential vs
// parallel chosen from input length and GOMAXPROCS), while "seq" and
// "p<N>" force one implementation at an explicit worker count so the
// true crossover of matchParallel is visible on any host, including
// below the 16 KiB dispatch floor.
func BenchmarkLabParaCross(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	big := make([]byte, 0, 8<<20)
	for len(big) < 8<<20 {
		big = append(big, ibsen...)
	}
	for _, size := range []int{4 << 10, 8 << 10, 16 << 10, 32 << 10, 48 << 10, 64 << 10, 96 << 10, 128 << 10, 1 << 19, 1 << 21, 1 << 23} {
		input := big[:size]
		b.Run(fmt.Sprintf("%dk-dispatch", size>>10), func(b *testing.B) { benchMatch(b, tr, input) })
		b.Run(fmt.Sprintf("%dk-seq", size>>10), func(b *testing.B) { benchMatchAt(b, tr, input, 1) })
		for _, p := range []int{2, 4, 8} {
			b.Run(fmt.Sprintf("%dk-p%d", size>>10, p), func(b *testing.B) { benchMatchAt(b, tr, input, p) })
		}
	}
}

// Multi-stop parallel crossover (slower per-byte scan => parallelism may
// pay earlier than for the single-stop path). Same mode split as
// BenchmarkLabParaCross.
func BenchmarkLabParaCrossMulti(b *testing.B) {
	patterns, ibsen := labLoad(b)
	tr := NewTrieBuilder().AddStrings(stride10k(patterns)).Build()
	big := make([]byte, 0, 1<<20)
	for len(big) < 1<<20 {
		big = append(big, ibsen...)
	}
	for _, size := range []int{4 << 10, 8 << 10, 16 << 10, 32 << 10, 64 << 10, 128 << 10, 256 << 10, 512 << 10} {
		input := big[:size]
		b.Run(fmt.Sprintf("%dk-dispatch", size>>10), func(b *testing.B) { benchMatch(b, tr, input) })
		b.Run(fmt.Sprintf("%dk-seq", size>>10), func(b *testing.B) { benchMatchAt(b, tr, input, 1) })
		for _, p := range []int{2, 4, 8} {
			b.Run(fmt.Sprintf("%dk-p%d", size>>10, p), func(b *testing.B) { benchMatchAt(b, tr, input, p) })
		}
	}
}

// In-automaton-heavy input: concatenated dictionary words with minimal
// separators, so the scanner spends most bytes in non-root states. This
// is the regime the dual-cursor scan targets (serial transition-load
// chain dominant, root skip rare).
func BenchmarkLabInAutomaton(b *testing.B) {
	patterns, _ := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	var sb []byte
	i := 0
	for len(sb) < 256<<10 {
		sb = append(sb, patterns[i%10000]...)
		i++
	}
	for _, size := range []int{4 << 10, 64 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { benchMatch(b, tr, sb[:size]) })
	}
}

// Density sweep: word runs separated by tunable filler gaps, giving
// stop-byte densities between Ibsen text (~4%) and pure concatenated
// words (~13%). Calibrates the dual-vs-single dispatch threshold.
func BenchmarkLabDensitySweep(b *testing.B) {
	patterns, _ := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	c := tr.rootStopBytes[0]
	for _, gap := range []int{0, 4, 8, 16, 32, 64} {
		var sb []byte
		i := 0
		for len(sb) < 96<<10 {
			sb = append(sb, patterns[i%10000]...)
			for g := 0; g < gap; g++ {
				sb = append(sb, 'x')
			}
			i++
		}
		input := sb[:96<<10]
		density := float64(bytes.Count(input, []byte{c})) / float64(len(input))
		b.Run(fmt.Sprintf("gap%d-d%.3f", gap, density), func(b *testing.B) { benchMatch(b, tr, input) })
	}
}

// Dense-input size sweep: pure concatenated dictionary words at growing
// sizes. Maps the size dependence of the dual-cursor win in isolation.
func BenchmarkLabDenseSize(b *testing.B) {
	patterns, _ := labLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	var sb []byte
	i := 0
	for len(sb) < 256<<10 {
		sb = append(sb, patterns[i%10000]...)
		i++
	}
	for _, size := range []int{2 << 10, 4 << 10, 8 << 10, 16 << 10, 32 << 10, 64 << 10, 96 << 10, 256 << 10} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { benchMatch(b, tr, sb[:size]) })
	}
}

// Multi-stop automaton that fits in 16 bits: a few thousand patterns
// spread across first letters. Exercises the (multi-stop, small) shape
// where a 16-bit table variant could pay.
func BenchmarkLabMultiSmall(b *testing.B) {
	patterns, ibsen := labLoad(b)
	sel := make([]string, 0, 3000)
	for i := 0; i < len(patterns) && len(sel) < 3000; i += 33 {
		sel = append(sel, patterns[i])
	}
	tr := NewTrieBuilder().AddStrings(sel).Build()
	if len(tr.failTrans) > 1<<15 || countStop(tr) < 2 {
		b.Fatalf("want small multi-stop automaton, got states=%d stops=%d", len(tr.failTrans), countStop(tr))
	}
	b.Run("ibsen-1k", func(b *testing.B) { benchMatch(b, tr, ibsen[:1000]) })
	b.Run("ibsen-4k", func(b *testing.B) { benchMatch(b, tr, ibsen[:4000]) })
	b.Run("ibsen-100k", func(b *testing.B) { benchMatch(b, tr, ibsen[:100000]) })
}

// Dense multi-stop input: concatenated stride-selected words, so nearly
// every byte is an in-automaton transition on the multi-stop automaton.
// The only remaining regime where a dual-cursor table scan could pay.
func BenchmarkLabMultiDense(b *testing.B) {
	patterns, _ := labLoad(b)
	sel := stride10k(patterns)
	tr := NewTrieBuilder().AddStrings(sel).Build()
	var sb []byte
	i := 0
	for len(sb) < 32<<10 {
		sb = append(sb, sel[i%len(sel)]...)
		i++
	}
	for _, size := range []int{4 << 10, 16 << 10, 31 << 10} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { benchMatch(b, tr, sb[:size]) })
	}
}

// Two-stop-byte automaton over mostly-gap input: the regime for the
// windowed multi-IndexByte root skip.
func BenchmarkLabSkip2(b *testing.B) {
	patterns, ibsen := labLoad(b)
	// Words starting with two distinct letters, interleaved.
	var ks, vs []string
	for _, p := range patterns {
		if len(p) == 0 {
			continue
		}
		if p[0] == 'k' && len(ks) < 1000 {
			ks = append(ks, p)
		} else if p[0] == 'v' && len(vs) < 1000 {
			vs = append(vs, p)
		}
	}
	sel := append(append([]string{}, ks...), vs...)
	tr := NewTrieBuilder().AddStrings(sel).Build()
	if countStop(tr) != 2 {
		b.Fatalf("want 2 stop bytes, got %d", countStop(tr))
	}
	rng := rand.New(rand.NewSource(3))
	nomatch := make([]byte, 100000)
	for i := range nomatch {
		nomatch[i] = byte('0' + rng.Intn(10))
	}
	b.Run("nomatch-100k", func(b *testing.B) { benchMatch(b, tr, nomatch) })
	b.Run("ibsen-100k", func(b *testing.B) { benchMatch(b, tr, ibsen[:100000]) })
}
