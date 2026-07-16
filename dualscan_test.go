package ahocorasick

// Focused unit tests for the dual-cursor scan internals: scanRange16's
// [i,to) window and minEmit filter, and matchDualStopByte16's lane-merge
// boundary at mid. TestRootSkipSplitBoundaries and TestDifferentialRandom
// cover these end-to-end through Match, but they route through matchSeq's
// size heuristics; these tests call the internals directly so the
// invariants stay pinned even if those thresholds change.

import (
	"fmt"
	"slices"
	"testing"
)

// buildStopByte16Trie builds a trie for patterns and asserts it takes the
// 16-bit single-stop-byte path that scanRange16 and matchDualStopByte16
// require.
func buildStopByte16Trie(tb testing.TB, patterns []string) *Trie {
	tb.Helper()
	tr := NewTrieBuilder().AddStrings(patterns).Build()
	if tr.failTrans16 == nil || len(tr.rootStopBytes) != 1 {
		tb.Fatalf("test setup: trie must take the 16-bit single-stop-byte path (failTrans16=%v, stop bytes=%d)",
			tr.failTrans16 != nil, len(tr.rootStopBytes))
	}
	return tr
}

// seqRaw16 returns the raw (end, dictPat) pairs of a full sequential
// matchStopByte16 scan, the oracle the range/dual scans must reproduce.
// matchStopByte16 itself is pinned to naiveMatch by the differential and
// root-skip tests.
func seqRaw16(tr *Trie, input []byte) []uint64 {
	buf := new(matchBuf)
	buf.reset()
	tr.matchStopByte16(input, buf)
	return buf.raw
}

// filterRaw returns the (end, dictPat) pairs whose end satisfies keep.
func filterRaw(raw []uint64, keep func(end uint64) bool) []uint64 {
	out := []uint64{}
	for k := 0; k+1 < len(raw); k += 2 {
		if keep(raw[k]) {
			out = append(out, raw[k], raw[k+1])
		}
	}
	return out
}

// TestScanRange16MinEmit asserts scanRange16 never emits a match ending
// before minEmit, emits a match ending exactly at minEmit (the filter is
// >=, not >), and otherwise reproduces the sequential scan restricted to
// [minEmit, to).
func TestScanRange16MinEmit(t *testing.T) {
	patterns := []string{"ab", "abc", "a"}
	tr := buildStopByte16Trie(t, patterns)

	input := bytesFill(300, 'x')
	// Matches at the start, sprinkled through the middle, and at the end.
	for _, pos := range []int{0, 40, 97, 98, 149, 150, 151, 203, 297} {
		copy(input[pos:], "abc")
	}

	full := seqRaw16(tr, input)
	if got := tr.scanRange16(input, 0, len(input), rootState, 0, nil); !slices.Equal(got, full) {
		t.Fatalf("full-range scanRange16 disagrees with matchStopByte16:\n  got=%v\n want=%v", got, full)
	}
	if len(full) == 0 {
		t.Fatal("test setup: expected matches in input")
	}

	// 150 is the end of the "a" planted at 150; ends at exactly minEmit
	// must be kept.
	for _, minEmit := range []int{0, 1, 41, 150, 152, 205, len(input) - 1, len(input)} {
		t.Run(fmt.Sprintf("minEmit=%d", minEmit), func(t *testing.T) {
			got := tr.scanRange16(input, 0, len(input), rootState, minEmit, nil)
			for k := 0; k+1 < len(got); k += 2 {
				if got[k] < uint64(minEmit) {
					t.Fatalf("emitted end %d < minEmit %d", got[k], minEmit)
				}
			}
			want := filterRaw(full, func(end uint64) bool { return end >= uint64(minEmit) })
			if !slices.Equal(got, want) {
				t.Fatalf("scanRange16 minEmit=%d:\n  got=%v\n want=%v", minEmit, got, want)
			}
		})
	}

	// Boundary sanity for one case: a match ends exactly at 150, so the
	// >= filter must retain it.
	got := tr.scanRange16(input, 0, len(input), rootState, 150, nil)
	found := false
	for k := 0; k+1 < len(got); k += 2 {
		if got[k] == 150 {
			found = true
		}
	}
	if !found {
		t.Fatal("match ending exactly at minEmit was dropped (filter must be >=, not >)")
	}
}

// TestScanRange16ToBound asserts the to bound: scanning [0,to) equals the
// sequential scan restricted to ends < to, for boundaries landing on,
// before, and after planted match ends.
func TestScanRange16ToBound(t *testing.T) {
	patterns := []string{"ab", "abc", "a"}
	tr := buildStopByte16Trie(t, patterns)

	input := bytesFill(300, 'x')
	for _, pos := range []int{0, 40, 149, 150, 151, 297} {
		copy(input[pos:], "abc")
	}
	full := seqRaw16(tr, input)

	for _, to := range []int{0, 1, 42, 150, 151, 152, 299, len(input)} {
		t.Run(fmt.Sprintf("to=%d", to), func(t *testing.T) {
			got := tr.scanRange16(input, 0, to, rootState, 0, nil)
			want := filterRaw(full, func(end uint64) bool { return end < uint64(to) })
			if !slices.Equal(got, want) {
				t.Fatalf("scanRange16 to=%d:\n  got=%v\n want=%v", to, got, want)
			}
		})
	}
}

// dualRaw16 runs matchDualStopByte16 and returns the merged raw pairs.
func dualRaw16(tr *Trie, input []byte) []uint64 {
	buf := new(matchBuf)
	buf.reset()
	tr.matchDualStopByte16(input, buf)
	return buf.raw
}

// checkDualAgainstSeq asserts the dual-cursor scan's merged output equals
// the sequential scan exactly (no overlap, gap, or reorder at mid) and
// that the output is partitioned at mid: a prefix of ends < mid (lane A)
// followed by a suffix of ends >= mid (lane B).
func checkDualAgainstSeq(t *testing.T, tr *Trie, input []byte) {
	t.Helper()
	got := dualRaw16(tr, input)
	want := seqRaw16(tr, input)
	if !slices.Equal(got, want) {
		t.Fatalf("dual scan disagrees with sequential scan (len %d vs %d):\n  got=%v\n want=%v",
			len(got)/2, len(want)/2, got, want)
	}

	mid := uint64(len(input) / 2)
	inB := false
	for k := 0; k+1 < len(got); k += 2 {
		if got[k] >= mid {
			inB = true
		} else if inB {
			t.Fatalf("end %d < mid %d after a lane-B entry: output not partitioned at mid", got[k], mid)
		}
	}
}

// TestDualStopByte16LaneMergeBoundary plants matches at every offset
// within maxLen of mid — including ones entirely inside lane B's warm-up
// overlap [mid-maxLen+1, mid), which lane B walks but must not emit — plus
// the input edges, and asserts the merged dual output is exactly the
// sequential scan. Odd sizes exercise the mid = len/2 floor.
func TestDualStopByte16LaneMergeBoundary(t *testing.T) {
	patterns := []string{"ab", "abc", "a"}
	tr := buildStopByte16Trie(t, patterns)
	maxLen := int(tr.maxLen)

	for _, sz := range []int{1024, 1101, 2048, 4097} {
		t.Run(fmt.Sprintf("sz=%d", sz), func(t *testing.T) {
			input := bytesFill(sz, 'x')
			mid := sz / 2
			for d := -2 * maxLen; d <= 2*maxLen; d++ {
				if pos := mid + d; pos >= 0 && pos+3 <= sz {
					copy(input[pos:], "abc")
				}
			}
			copy(input[0:], "abc")
			copy(input[sz-3:], "abc")
			checkDualAgainstSeq(t, tr, input)
		})
	}
}

// TestDualStopByte16UnevenLanes forces the two lanes to progress at very
// different rates so the interleaved loop exits with one lane far from its
// boundary and scanRange16 finishes a large remainder: a dense half is all
// stop bytes (one transition per byte, many emits) while a sparse half is
// all self-loop filler (SWAR skip, ~8 bytes per step).
func TestDualStopByte16UnevenLanes(t *testing.T) {
	patterns := []string{"ab", "abc", "a"}
	tr := buildStopByte16Trie(t, patterns)

	const sz = 4096
	t.Run("dense_A_sparse_B", func(t *testing.T) {
		input := bytesFill(sz, 'x')
		for i := 0; i < sz/2; i++ {
			input[i] = 'a' // lane A: every byte is a stop byte
		}
		checkDualAgainstSeq(t, tr, input)
	})
	t.Run("sparse_A_dense_B", func(t *testing.T) {
		input := bytesFill(sz, 'x')
		for i := sz / 2; i < sz; i++ {
			input[i] = 'a' // lane B: every byte is a stop byte
		}
		checkDualAgainstSeq(t, tr, input)
	})
}
