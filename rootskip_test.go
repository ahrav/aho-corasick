package ahocorasick

// Correctness tests for the root self-loop skip, checking Match against the
// naive reference (naiveMatch, differential_test.go) at boundaries a random
// fuzzer reaches only by chance:
//
//   - the SWAR word scan reads 8-byte words, so a stop byte's offset within a
//     word (0..7) and gaps that cross word boundaries take different branches;
//   - after k==3 (4 SWAR words, 32 skipped bytes) walkStopByte switches to
//     bytes.IndexByte for the rest of the gap;
//   - the sub-8-byte tail (i+8 > inputLen) uses a byte-at-a-time loop and the
//     return-on-i==inputLen guard;
//   - one stop byte selects walkStopByte, and any other count (zero, or two
//     or more) selects walkTable;
//   - matches whose end is the final input byte, right after a skip.

import (
	"fmt"
	"sort"
	"testing"
)

// checkAgainstNaive builds a trie and asserts Match agrees with naiveMatch
// exactly, comparing as sorted (pos, pattern, len) sets to avoid depending on
// emission order.
func checkAgainstNaive(t *testing.T, patterns []string, input []byte) {
	t.Helper()
	trie := NewTrieBuilder().AddStrings(patterns).Build()
	got := trie.Match(input)
	defer trie.ReleaseMatches(got)

	gotTriples := make([][3]uint32, 0, len(got))
	for _, m := range got {
		gotTriples = append(gotTriples, [3]uint32{m.Pos(), m.Pattern(), uint32(len(m.Match()))})
	}
	want := naiveMatch(patterns, input)

	norm := func(s [][3]uint32) [][3]uint32 {
		c := make([][3]uint32, len(s))
		copy(c, s)
		sort.Slice(c, func(i, j int) bool {
			if c[i][0] != c[j][0] {
				return c[i][0] < c[j][0]
			}
			if c[i][2] != c[j][2] {
				return c[i][2] < c[j][2]
			}
			return c[i][1] < c[j][1]
		})
		return c
	}
	g, w := norm(gotTriples), norm(want)
	if len(g) != len(w) {
		t.Fatalf("match count: got %d, want %d\n  got=%v\n want=%v", len(g), len(w), g, w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("match %d: got %v, want %v\n  full got=%v\n full want=%v", i, g[i], w[i], g, w)
		}
	}
}

// bytesFill makes a slice of n copies of c.
func bytesFill(n int, c byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}

// TestRootSkipSingleStopByteAlignments places the single stop byte 'a' at
// every offset relative to the 8-byte SWAR word, with fillers 'x' that
// self-loop at the root, so the SWAR scan, tail loop, and match-at-end paths
// are all exercised.
func TestRootSkipSingleStopByteAlignments(t *testing.T) {
	patterns := []string{"ab", "a"}
	// gaps chosen to land the stop byte before/at/after word boundaries and
	// to trigger the IndexByte escalation (gaps >= 32).
	for _, gap := range []int{0, 1, 7, 8, 9, 15, 16, 31, 32, 33, 40, 63, 64, 100} {
		for _, trailer := range []string{"ab", "a", "xx", ""} {
			input := append(bytesFill(gap, 'x'), []byte("ab")...)
			input = append(input, []byte(trailer)...)
			t.Run(fmt.Sprintf("gap=%d/trailer=%q", gap, trailer), func(t *testing.T) {
				checkAgainstNaive(t, patterns, input)
			})
		}
	}
}

// TestRootSkipLongGapIndexByte forces the bytes.IndexByte escalation: a run
// of self-loop bytes far longer than 4 SWAR words (32 bytes) before the stop
// byte, plus the no-more-stop-bytes case (skip to end).
func TestRootSkipLongGapIndexByte(t *testing.T) {
	patterns := []string{"needle", "n"}
	for _, gap := range []int{33, 64, 65, 127, 128, 1000, 5000} {
		input := append(bytesFill(gap, ' '), []byte("needle")...)
		// Also a long trailing gap with no further stop byte.
		input = append(input, bytesFill(gap, ' ')...)
		t.Run(fmt.Sprintf("gap=%d", gap), func(t *testing.T) {
			checkAgainstNaive(t, patterns, input)
		})
	}
	// Pure self-loop input: never leaves root, must return no matches.
	checkAgainstNaive(t, patterns, bytesFill(10000, ' '))
}

// TestRootSkipMultiStopByteTable exercises walkTable: two or more distinct
// first bytes route through the rootStop OR-table skip instead of the SWAR
// path. Sweeps density-like patterns and alignments.
func TestRootSkipMultiStopByteTable(t *testing.T) {
	patterns := []string{"apple", "banana", "cherry", "a", "b", "c"} // first bytes a,b,c => table
	inputs := [][]byte{
		[]byte("apple banana cherry"),
		append(bytesFill(50, ' '), []byte("apple")...),
		append(append(bytesFill(9, ' '), []byte("banana")...), bytesFill(40, ' ')...),
		[]byte("aaaabbbbccccapplebananacherry"),
		bytesFill(100, ' '), // never leaves root
	}
	for i, in := range inputs {
		t.Run(fmt.Sprintf("input%d", i), func(t *testing.T) {
			checkAgainstNaive(t, patterns, in)
		})
	}
}

// TestRootSkipEveryByteStops is the walkStopByte worst case: the haystack is
// all stop bytes, so the skip never advances past the current position and
// the automaton must still find every match.
func TestRootSkipEveryByteStops(t *testing.T) {
	patterns := []string{"aa", "a"}
	for _, n := range []int{1, 7, 8, 9, 16, 33, 100} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			checkAgainstNaive(t, patterns, bytesFill(n, 'a'))
		})
	}
}

// TestRootSkipReturnToRootMidInput checks skips that re-engage after the
// automaton returns to the root partway through: a match, then a long gap,
// then another match — the s==rootState re-entry into the skip must be
// correct, including when the trailing match ends the input.
func TestRootSkipReturnToRootMidInput(t *testing.T) {
	patterns := []string{"cat", "dog"} // single first-byte? c and d => table path
	for _, gap := range []int{5, 8, 33, 200} {
		input := []byte("cat")
		input = append(input, bytesFill(gap, ' ')...)
		input = append(input, []byte("dog")...)
		t.Run(fmt.Sprintf("gap=%d", gap), func(t *testing.T) {
			checkAgainstNaive(t, patterns, input)
		})
	}
	// Single stop byte variant (walkStopByte) with re-entry.
	single := []string{"aXa", "a"} // first byte only 'a'
	for _, gap := range []int{5, 8, 33, 200} {
		input := []byte("aXa")
		input = append(input, bytesFill(gap, ' ')...)
		input = append(input, []byte("aXa")...)
		t.Run(fmt.Sprintf("single/gap=%d", gap), func(t *testing.T) {
			checkAgainstNaive(t, single, input)
		})
	}
}

// TestRootSkipMatchAtExactEnd matches ending at the final byte, right after a
// skip of each boundary length, so the i==inputLen guard in each walk function
// does not drop a final match. The single case uses patterns "ab" and "a"
// (both start with 'a', one stop byte => walkStopByte); the multi case uses
// "cd" and "ef" (two stop bytes => walkTable). Gap bytes ('x', ' ') are not a
// first byte of either pattern set, so they self-loop and trigger the skip.
func TestRootSkipMatchAtExactEnd(t *testing.T) {
	for _, gap := range []int{0, 7, 8, 32, 33, 100} {
		in1 := append(bytesFill(gap, 'x'), []byte("ab")...)
		t.Run(fmt.Sprintf("single/gap=%d", gap), func(t *testing.T) {
			checkAgainstNaive(t, []string{"ab", "a"}, in1) // one stop byte => walkStopByte
		})
		in2 := append(bytesFill(gap, ' '), []byte("cd")...)
		t.Run(fmt.Sprintf("multi/gap=%d", gap), func(t *testing.T) {
			checkAgainstNaive(t, []string{"cd", "ef"}, in2) // two stop bytes => walkTable
		})
	}
}

// TestRootSkipSplitBoundaries plants matches at and around the input midpoint
// and every 1/8 chunk boundary, over inputs spanning the size thresholds at
// which a chunked or parallel scan may partition the work (>1KB and >16KB). A
// match that straddles a chunk boundary must be reported exactly once —
// neither dropped nor duplicated — however the input is partitioned; an
// off-by-one in the boundary ownership arithmetic breaks that. Matches sit at
// every offset within maxLen of a boundary so a straddling match is exercised
// on both sides.
func TestRootSkipSplitBoundaries(t *testing.T) {
	patterns := []string{"needle", "haystackNEEDLE", "abcdefghij", "xyz"}
	maxLen := 0
	for _, p := range patterns {
		if len(p) > maxLen {
			maxLen = len(p)
		}
	}
	// planted returns a fresh sz-byte self-loop haystack with s copied in at
	// pos, so each boundary case is validated in its own buffer and no plant
	// overwrites another.
	planted := func(sz, pos int, s string) []byte {
		in := bytesFill(sz, ' ')
		if pos >= 0 && pos+len(s) <= len(in) {
			copy(in[pos:], s)
		}
		return in
	}
	for _, sz := range []int{1100, 2048, 20000, 50000, 131072} {
		mid := sz / 2
		for d := -maxLen; d <= maxLen; d++ {
			in := planted(sz, mid+d, "needle")
			t.Run(fmt.Sprintf("sz=%d/mid%+d", sz, d), func(t *testing.T) {
				checkAgainstNaive(t, patterns, in)
			})
		}
		for w := 1; w <= 8; w++ {
			cb := sz * w / 8
			for _, tc := range []struct {
				pos int
				s   string
			}{
				{cb - 3, "abcdefghij"},
				{cb, "xyz"},
			} {
				in := planted(sz, tc.pos, tc.s)
				t.Run(fmt.Sprintf("sz=%d/cb%d/%s", sz, w, tc.s), func(t *testing.T) {
					checkAgainstNaive(t, patterns, in)
				})
			}
		}
		t.Run(fmt.Sprintf("sz=%d/end", sz), func(t *testing.T) {
			checkAgainstNaive(t, patterns, planted(sz, sz-len("needle"), "needle"))
		})
		t.Run(fmt.Sprintf("sz=%d/start", sz), func(t *testing.T) {
			checkAgainstNaive(t, patterns, planted(sz, 0, "needle"))
		})
	}
}

// TestRootSkipDensityGate checks that walkTable's density gate — an
// output-invisible optimization that only toggles whether root self-loops are
// skipped — never changes Match output. It feeds a low-density input (sparse
// stop bytes, long self-loop runs) and a high-density input (nearly every byte
// leaves the root) and asserts both still equal the naive reference. The
// skip is invisible in Match output; this test guards correctness across
// densities. Patterns start with several distinct
// bytes so the trie takes the multi-stop-byte walkTable path where the gate
// lives.
func TestRootSkipDensityGate(t *testing.T) {
	patterns := []string{"apple", "banana", "cherry", "date", "elder", "a", "e"}
	n := 20000

	// Low density: mostly spaces (self-loop), rare planted matches.
	low := bytesFill(n, ' ')
	for pos := 0; pos+6 < n; pos += 500 {
		copy(low[pos:], "cherry")
	}

	// High density: cycle through the stop-byte first letters so almost
	// every byte leaves the root.
	high := make([]byte, n)
	firsts := []byte{'a', 'b', 'c', 'd', 'e'}
	for i := range high {
		high[i] = firsts[i%len(firsts)]
	}
	// Sprinkle full patterns so there is real match work too.
	for pos := 0; pos+6 < n; pos += 137 {
		copy(high[pos:], "banana")
	}

	t.Run("low_density", func(t *testing.T) {
		checkAgainstNaive(t, patterns, low)
	})
	t.Run("high_density", func(t *testing.T) {
		checkAgainstNaive(t, patterns, high)
	})
}
