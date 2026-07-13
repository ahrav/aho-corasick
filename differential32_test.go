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
		{
			// Dense sample windows but asymmetric root gaps: the first
			// half is ~half filler while the second half is nearly all
			// stop bytes, so rootDense still passes but lane A root-skips
			// through its gaps and exits the interleaved loop early,
			// leaving scanRangeTable32 a substantial lane-B remainder
			// with gaps of its own — the skipRootTable and early-return
			// branches the lockstep dense case can never reach. A
			// pattern planted across mid pins the lane boundary: matches
			// ending < mid belong to lane A, >= mid to lane B.
			"multi-stop-gappy",
			[]string{"ab", "bc", "ca", "abc", "cab", "bbb"},
			func(n int) []byte {
				input := make([]byte, n)
				for i := range input {
					gap := 8 // first half: ~half the bytes are root gaps
					if i >= n/2 {
						gap = 14 // second half: occasional gaps only
					}
					if rng.Intn(16) < gap {
						input[i] = byte('a' + rng.Intn(3))
					} else {
						input[i] = byte('x' + rng.Intn(8))
					}
				}
				if n >= 8 {
					copy(input[n/2-1:], "cab")
					// Deterministic filler tail: the lane-B finisher's
					// root skip finds no further stop byte and takes the
					// i >= to early return.
					input[n-2], input[n-1] = 'x', 'y'
				}
				return input
			},
		},
		{
			// A long root gap spanning mid: rootDense still passes on
			// the dense head/tail windows, lane A's bounded root skip
			// stops at exactly mid, and lane B enters mid-gap and skips
			// to the gap's far edge — the lane-boundary geometry the
			// other fixtures never produce.
			"multi-stop-midgap",
			[]string{"ab", "bc", "ca", "abc", "cab", "bbb"},
			func(n int) []byte {
				input := make([]byte, n)
				for i := range input {
					input[i] = byte('a' + rng.Intn(3))
				}
				if n >= 1024 {
					for i := n/2 - 200; i < n/2+200; i++ {
						input[i] = 'z'
					}
				}
				return input
			},
		},
	}

	sizes := []int{0, 1, 100, 1000, 4096, 5000, 8191, 20000, 80000}

	for _, c := range cases {
		trie := NewTrieBuilder().AddStrings(c.patterns).Build()
		trie.failTrans16 = nil // force 32-bit scan paths
		trie.setStopEntry()

		for _, size := range sizes {
			input := c.mkInput(size)
			want := naiveMatch(c.patterns, input)
			got := trie.Match(input)

			if len(got) != len(want) {
				t.Fatalf("%s size=%d: got %d matches, want %d", c.name, size, len(got), len(want))
			}
			for k, m := range got {
				w := want[k]
				if m.Pos() != w[0] || m.Pattern() != w[1] || uint32(len(m.Match())) != w[2] {
					t.Fatalf("%s size=%d match %d: got (pos=%d pat=%d len=%d), want (pos=%d pat=%d len=%d)",
						c.name, size, k, m.Pos(), m.Pattern(), len(m.Match()), w[0], w[1], w[2])
				}
			}
			trie.ReleaseMatches(got)
		}
	}
}
