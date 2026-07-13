package ahocorasick

// Benchmarks for output-heavy workloads: many overlapping patterns whose
// matches chain through dictLink, so emission cost — not the transition
// chain — dominates. The Ibsen/RootSkip benchmarks measure sparse-match
// corpora; these measure the other extreme and a middle point.
//
// Only the public API is used, so this file can be dropped onto any branch
// of the perf stack for A/B comparison.

import (
	"bytes"
	"strings"
	"testing"
)

// BenchmarkOutputHeavy_Extreme: patterns a, aa, ..., a^16 over an all-'a'
// input. Every byte emits min(i+1,16) matches through a 15-deep dictLink
// chain — the worst case for emission.
func BenchmarkOutputHeavy_Extreme(b *testing.B) {
	pats := make([]string, 16)
	for i := range pats {
		pats[i] = strings.Repeat("a", i+1)
	}
	trie := NewTrieBuilder().AddStrings(pats).Build()
	hay := bytes.Repeat([]byte{'a'}, 1<<18)
	benchMatchOver(b, trie, hay)
}

// BenchmarkOutputHeavy_DenseWords: the sorted-10k dictionary matched
// against a haystack made of the dictionary's own words — every word is a
// match and shorter words match inside longer ones. High match density
// with realistic pattern shapes.
func BenchmarkOutputHeavy_DenseWords(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	pats := all[:10000]
	var sb strings.Builder
	for i := 0; sb.Len() < 1<<18; i = (i + 7919) % len(pats) {
		sb.WriteString(pats[i])
		sb.WriteByte(' ')
	}
	trie := NewTrieBuilder().AddStrings(pats).Build()
	benchMatchOver(b, trie, []byte(sb.String()))
}

// BenchmarkOutputHeavy_DenseSpread: same haystack construction but with the
// evenly-spread dictionary, so the multi-stop-byte (matchTable) path carries
// the emission load.
func BenchmarkOutputHeavy_DenseSpread(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	pats := spreadDict(all, 10000)
	var sb strings.Builder
	for i := 0; sb.Len() < 1<<18; i = (i + 7919) % len(pats) {
		sb.WriteString(pats[i])
		sb.WriteByte(' ')
	}
	trie := NewTrieBuilder().AddStrings(pats).Build()
	benchMatchOver(b, trie, []byte(sb.String()))
}
