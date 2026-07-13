package ahocorasick

import (
	"math/rand"
	"testing"
)

// TestDifferential32BitPaths forces the full-width (32-bit) scan paths by
// dropping the failTrans16 acceleration table, then cross-checks Match
// against the naive reference. Real automata only take these paths above
// 2^15 states, which is too large to differential-test directly; the
// scan code is byte-for-byte the same either way, so this whitebox
// downgrade gives the 32-bit loops (matchStopByte, matchTable,
// matchDualTable32, scanRangeTable32) the same coverage as the 16-bit
// ones.
func TestDifferential32BitPaths(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	cases := []struct {
		name     string
		patterns []string
		mkInput  func(n int) []byte
	}{
		{
			// Single stop byte: all patterns start with 'a'.
			"single-stop",
			[]string{"ab", "abc", "abca", "aa", "aab"},
			func(n int) []byte {
				input := make([]byte, n)
				for i := range input {
					if rng.Intn(3) == 0 {
						input[i] = 'a'
					} else {
						input[i] = byte('b' + rng.Intn(20))
					}
				}
				return input
			},
		},
		{
			// Several stop bytes, sparse input: table paths, low density.
			"multi-stop-sparse",
			[]string{"ab", "bc", "ca", "abc", "cab"},
			func(n int) []byte {
				input := make([]byte, n)
				for i := range input {
					if rng.Intn(6) == 0 {
						input[i] = byte('a' + rng.Intn(3))
					} else {
						input[i] = byte('x' + rng.Intn(8))
					}
				}
				return input
			},
		},
		{
			// Several stop bytes, dense input: exercises the dual-cursor
			// 32-bit table scan (rootDense passes).
			"multi-stop-dense",
			[]string{"ab", "bc", "ca", "abc", "cab", "bbb"},
			func(n int) []byte {
				input := make([]byte, n)
				for i := range input {
					input[i] = byte('a' + rng.Intn(3))
				}
				return input
			},
		},
	}

	sizes := []int{0, 1, 100, 1000, 4096, 5000, 20000, 80000}

	for _, c := range cases {
		for _, classed := range []bool{false, true} {
			trie := NewTrieBuilder().AddStrings(c.patterns).Build()
			trie.failTrans16 = nil // force 32-bit scan paths
			trie.setStopEntry()
			if classed {
				// Also exercise the byte-class-compressed loops
				// (matchDualTableC, scanRangeTableC). Single-stop
				// tries take matchStopByte and never read the class
				// table, so buildClassTable must refuse to build one
				// for them; there is no classed path to exercise.
				trie.buildClassTable(trie.derivedLiveBytes())
				if wantTable := len(trie.rootStopBytes) != 1; (trie.failTransC != nil) != wantTable {
					t.Fatalf("%s: failTransC != nil is %v, want %v", c.name, !wantTable, wantTable)
				}
				if trie.failTransC == nil {
					continue
				}
			} else {
				trie.failTransC = nil
			}

			for _, size := range sizes {
				input := c.mkInput(size)
				want := naiveMatch(c.patterns, input)
				got := trie.Match(input)

				if len(got) != len(want) {
					t.Fatalf("%s classed=%v size=%d: got %d matches, want %d", c.name, classed, size, len(got), len(want))
				}
				for k, m := range got {
					w := want[k]
					if m.Pos() != w[0] || m.Pattern() != w[1] || uint32(len(m.Match())) != w[2] {
						t.Fatalf("%s classed=%v size=%d match %d: got (pos=%d pat=%d len=%d), want (pos=%d pat=%d len=%d)",
							c.name, classed, size, k, m.Pos(), m.Pattern(), len(m.Match()), w[0], w[1], w[2])
					}
				}
				trie.ReleaseMatches(got)
			}
		}
	}
}
