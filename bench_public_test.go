package ahocorasick

// Portable before/after benchmark suite: public API only, no internals,
// so the identical file compiles against the stock upstream library and
// the optimized fork. Helpers are pub-prefixed to avoid colliding with
// either tree's existing benchmarks.

import (
	"bufio"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

var pubState struct {
	loaded   bool
	patterns []string
	ibsen    []byte
}

func pubLoad(b *testing.B) ([]string, []byte) {
	if !pubState.loaded {
		f, err := os.Open("./test_data/NSF-ordlisten.cleaned.txt")
		if err != nil {
			b.Fatal(err)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		var pats []string
		for sc.Scan() {
			if s := strings.TrimSpace(sc.Text()); s != "" {
				pats = append(pats, s)
			}
		}
		ib, err := os.ReadFile("./test_data/Ibsen.txt")
		if err != nil {
			b.Fatal(err)
		}
		pubState.patterns = pats
		pubState.ibsen = ib
		pubState.loaded = true
	}
	return pubState.patterns, pubState.ibsen
}

// pubStride spreads the dictionary across the alphabet (the sorted list's
// prefix is all 'a' words; every 10th entry gives many first letters).
func pubStride(patterns []string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < len(patterns) && len(out) < n; i += 10 {
		out = append(out, patterns[i])
	}
	return out
}

func pubBench(b *testing.B, tr *Trie, input []byte) {
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		ms := tr.Match(input)
		tr.ReleaseMatches(ms)
	}
}

// --- Match over natural text ---

func BenchmarkPubText_Sorted10k(b *testing.B) {
	patterns, ibsen := pubLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	for _, size := range []int{1 << 10, 4 << 10, 100 << 10} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { pubBench(b, tr, ibsen[:size]) })
	}
}

func BenchmarkPubText_Spread10k(b *testing.B) {
	patterns, ibsen := pubLoad(b)
	tr := NewTrieBuilder().AddStrings(pubStride(patterns, 10000)).Build()
	for _, size := range []int{4 << 10, 100 << 10} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { pubBench(b, tr, ibsen[:size]) })
	}
}

func BenchmarkPubText_Big100k(b *testing.B) {
	patterns, ibsen := pubLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:100000]).Build()
	b.Run("100k", func(b *testing.B) { pubBench(b, tr, ibsen[:100<<10]) })
}

// --- Large inputs (ibsen repeated) ---

func BenchmarkPubLarge_Sorted10k(b *testing.B) {
	patterns, ibsen := pubLoad(b)
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	big := make([]byte, 0, 8<<20)
	for len(big) < 8<<20 {
		big = append(big, ibsen...)
	}
	for _, size := range []int{512 << 10, 2 << 20, 8 << 20} {
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) { pubBench(b, tr, big[:size]) })
	}
}

// --- No-match input (digits never start a Norwegian word) ---

func BenchmarkPubNoMatch(b *testing.B) {
	patterns, _ := pubLoad(b)
	rng := rand.New(rand.NewSource(7))
	input := make([]byte, 1<<20)
	for i := range input {
		input[i] = byte('0' + rng.Intn(10))
	}
	trS := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	trM := NewTrieBuilder().AddStrings(pubStride(patterns, 10000)).Build()
	b.Run("sorted-100k", func(b *testing.B) { pubBench(b, trS, input[:100<<10]) })
	b.Run("sorted-1m", func(b *testing.B) { pubBench(b, trS, input) })
	b.Run("spread-100k", func(b *testing.B) { pubBench(b, trM, input[:100<<10]) })
	b.Run("spread-1m", func(b *testing.B) { pubBench(b, trM, input) })
}

// --- Dense outputs: short overlapping patterns, every byte matches ---

func BenchmarkPubDense(b *testing.B) {
	tr := NewTrieBuilder().AddStrings([]string{"a", "ab", "aba", "abab", "b", "ba"}).Build()
	input := make([]byte, 64<<10)
	for i := range input {
		if i%2 == 0 {
			input[i] = 'a'
		} else {
			input[i] = 'b'
		}
	}
	b.Run("ab-64k", func(b *testing.B) { pubBench(b, tr, input) })
}

// --- Concatenated dictionary words: automaton-resident scanning ---

func BenchmarkPubConcat(b *testing.B) {
	patterns, _ := pubLoad(b)
	sel := pubStride(patterns, 10000)
	tr := NewTrieBuilder().AddStrings(sel).Build()
	var sb []byte
	i := 0
	for len(sb) < 64<<10 {
		sb = append(sb, sel[i%len(sel)]...)
		i++
	}
	b.Run("64k", func(b *testing.B) { pubBench(b, tr, sb[:64<<10]) })
}

// --- Walk (callback API) ---

func BenchmarkPubWalk(b *testing.B) {
	patterns, ibsen := pubLoad(b)
	trS := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	trM := NewTrieBuilder().AddStrings(pubStride(patterns, 10000)).Build()
	input := ibsen[:100<<10]
	for _, tc := range []struct {
		name string
		tr   *Trie
	}{{"sorted-100k", trS}, {"spread-100k", trM}} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(input)))
			var cnt int
			for n := 0; n < b.N; n++ {
				tc.tr.Walk(input, func(end, l, p uint32) bool { cnt++; return true })
			}
		})
	}
}

// --- MatchFirst with a late needle ---

func BenchmarkPubMatchFirstLate(b *testing.B) {
	_, ibsen := pubLoad(b)
	tr := NewTrieBuilder().AddString("imorges").Build() // first occurrence ~byte 99805
	input := ibsen[:100<<10]
	b.SetBytes(int64(len(input)))
	for n := 0; n < b.N; n++ {
		if m := tr.MatchFirst(input); m == nil {
			b.Fatal("expected a match")
		}
	}
}

// --- Build ---

func BenchmarkPubBuild(b *testing.B) {
	patterns, _ := pubLoad(b)
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				NewTrieBuilder().AddStrings(patterns[:n]).Build()
			}
		})
	}
}
