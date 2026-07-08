package ahocorasick

import (
	"math/rand"
	"testing"
)

// naiveMatch is a reference implementation: for every position, try every
// pattern by direct comparison. Returns (pos, pattern id, length) triples
// ordered by end position, then by pattern length ascending (the order the
// automaton reports overlapping matches at one end position is by suffix
// length, i.e. shortest dictLink chain entry last; we sort instead).
func naiveMatch(patterns []string, input []byte) [][3]uint32 {
	maxLen := 0
	for _, p := range patterns {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	var out [][3]uint32
	for end := 0; end < len(input); end++ {
		// Collect all patterns ending at end, longest first (the automaton
		// emits the state's own (longest) match first, then dictLinks in
		// decreasing suffix length).
		for l := min(end+1, maxLen); l >= 1; l-- {
			start := end - l + 1
			for pid, p := range patterns {
				if len(p) == l && string(input[start:end+1]) == p {
					out = append(out, [3]uint32{uint32(start), uint32(pid), uint32(l)})
				}
			}
		}
	}
	return out
}

// TestDifferentialRandom cross-checks Match against the naive reference on
// random inputs sized to exercise the single-cursor, dual-cursor, and
// parallel scan paths.
func TestDifferentialRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	alphabets := []string{"ab", "abc", "abcdefgh"}
	sizes := []int{0, 1, 7, 100, 1000, 3000, 20000, 80000}

	for _, alpha := range alphabets {
		// Random pattern set over the alphabet, including duplicates of
		// prefixes/suffixes to stress dictLink chains.
		numPat := 20
		patterns := make([]string, 0, numPat)
		seen := map[string]bool{}
		for len(patterns) < numPat {
			l := 1 + rng.Intn(6)
			b := make([]byte, l)
			for i := range b {
				b[i] = alpha[rng.Intn(len(alpha))]
			}
			s := string(b)
			if !seen[s] {
				seen[s] = true
				patterns = append(patterns, s)
			}
		}

		trie := NewTrieBuilder().AddStrings(patterns).Build()

		for _, size := range sizes {
			input := make([]byte, size)
			for i := range input {
				// Mix pattern-alphabet bytes with outside bytes so the
				// root skip paths engage.
				if rng.Intn(4) == 0 {
					input[i] = byte('x' + rng.Intn(10))
				} else {
					input[i] = alpha[rng.Intn(len(alpha))]
				}
			}

			want := naiveMatch(patterns, input)
			got := trie.Match(input)

			if len(got) != len(want) {
				t.Fatalf("alpha=%q size=%d: got %d matches, want %d", alpha, size, len(got), len(want))
			}
			for k, m := range got {
				w := want[k]
				if m.Pos() != w[0] || m.Pattern() != w[1] || uint32(len(m.Match())) != w[2] {
					t.Fatalf("alpha=%q size=%d match %d: got (pos=%d pat=%d len=%d), want (pos=%d pat=%d len=%d)",
						alpha, size, k, m.Pos(), m.Pattern(), len(m.Match()), w[0], w[1], w[2])
				}
			}
			trie.ReleaseMatches(got)
		}
	}
}
