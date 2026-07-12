package ahocorasick

import (
	"bytes"
	"math/rand"
	"testing"
)

// naiveMatch is a reference implementation. For every end position it
// reports the pattern ending there at each length, longest first, which
// is exactly the automaton's emission order at one position (the state's
// own longest match, then dictLinks in decreasing suffix length).
//
// Both callers pass a deduplicated pattern set, so at most one pattern
// occupies any given (start, length): the substring at that span either
// equals a pattern or it does not. That lets the inner scan over every
// pattern collapse to one map lookup keyed on the substring — the
// map[string] lookup does not allocate — turning an O(len·maxLen·patterns)
// oracle with a string copy per check into O(len·maxLen). The output is
// byte-for-byte identical to the naive triple loop for deduplicated input.
func naiveMatch(patterns []string, input []byte) [][3]uint32 {
	maxLen := 0
	pid := make(map[string]uint32, len(patterns))
	for i, p := range patterns {
		if _, ok := pid[p]; !ok {
			pid[p] = uint32(i) // first index wins; deduped input has no ties
		}
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	var out [][3]uint32
	for end := 0; end < len(input); end++ {
		for l := min(end+1, maxLen); l >= 1; l-- {
			start := end - l + 1
			if id, ok := pid[string(input[start:end+1])]; ok {
				out = append(out, [3]uint32{uint32(start), id, uint32(l)})
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

// TestMatchParallelDifferential drives matchParallel with explicit worker
// counts and compares against the naive reference. Match only dispatches
// to matchParallel when runtime.GOMAXPROCS(0) > 1, so on a single-CPU
// runner every GOMAXPROCS-gated caller (including the fuzz seeds and
// TestDifferentialRandom) silently falls back to matchSeq; injecting p
// here keeps the drop/rebase and chunk-boundary logic deterministically
// covered regardless of the host's CPU count.
func TestMatchParallelDifferential(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		input    []byte
	}{
		// 1 root stop byte → dual-cursor matchSeq inside each chunk.
		{"singleStopByte", []string{"ab", "abc", "abca"}, fillWithMatches(40000, 'x', "abca")},
		// >1 root stop byte → table matchSeq inside each chunk.
		{"multiStopByte", []string{"ab", "bc", "ca"}, fillWithMatches(40000, 'z', "abca")},
		// A match ends at every position, so matches land exactly on
		// every chunk boundary — the drop/rebase conditions all fire.
		{"denseBoundaries", []string{"a", "aa", "aaa"}, bytes.Repeat([]byte("a"), 40000)},
		// No matches at all: every worker returns an empty buffer.
		{"noMatches", []string{"zzz"}, bytes.Repeat([]byte("a"), 40000)},
	}

	for _, tc := range cases {
		trie := NewTrieBuilder().AddStrings(tc.patterns).Build()
		want := naiveMatch(tc.patterns, tc.input)

		for _, p := range []int{2, 3, 4, 8} {
			got := trie.matchParallel(tc.input, p)
			if len(got) != len(want) {
				t.Fatalf("%s p=%d: got %d matches, want %d", tc.name, p, len(got), len(want))
			}
			for k, m := range got {
				w := want[k]
				if m.Pos() != w[0] || m.Pattern() != w[1] || uint32(len(m.Match())) != w[2] {
					t.Fatalf("%s p=%d match %d: got (pos=%d pat=%d len=%d), want (pos=%d pat=%d len=%d)",
						tc.name, p, k, m.Pos(), m.Pattern(), len(m.Match()), w[0], w[1], w[2])
				}
			}
			trie.ReleaseMatches(got)
		}
	}
}
