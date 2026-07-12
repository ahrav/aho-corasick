package ahocorasick

// Benchmarks for mid-size multi-stop-byte dictionaries (states fit in 15
// bits), where a half-width transition table is a candidate: the 20MB+
// tables of the 10k-word dictionaries dwarf L2, but a 1k-word dictionary's
// ~10MB full table vs ~5MB half table straddles cache boundaries.

import "testing"

func benchSpreadN(b *testing.B, n int, hay string) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	trie := NewTrieBuilder().AddStrings(spreadDict(all, n)).Build()
	benchMatchOver(b, trie, benchReadFile(b, hay))
}

func BenchmarkMidsize_Spread1k_Ibsen(b *testing.B)  { benchSpreadN(b, 1000, "./test_data/Ibsen.txt") }
func BenchmarkMidsize_Spread1k_GPL(b *testing.B)    { benchSpreadN(b, 1000, "./test_data/gpl.txt") }
func BenchmarkMidsize_Spread100_Ibsen(b *testing.B) { benchSpreadN(b, 100, "./test_data/Ibsen.txt") }
