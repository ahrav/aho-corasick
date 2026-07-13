package ahocorasick

// Tests for the chain-occupancy dispatch gate (chainHeavy): the second
// dual-cursor routing signal for inputs whose stop-byte density is low
// but whose bytes are almost all serial transition loads (long-pattern
// excursions). looksDense alone misses that regime; these tests pin the
// routing on both sides of the occupancy boundary and cross-check the
// rescued path against the naive reference.

import (
	"math/rand"
	"testing"
)

// buildLongPatterns returns npat patterns of length plen, all starting
// with 'q' (single root stop byte) and avoiding 'q' elsewhere, so a
// concatenation has stop-byte density 1/plen while keeping the scanner
// in-chain on nearly every byte.
func buildLongPatterns(plen, npat int) []string {
	rng := rand.New(rand.NewSource(42))
	pats := make([]string, npat)
	for i := range pats {
		b := make([]byte, plen)
		b[0] = 'q'
		for j := 1; j < plen; j++ {
			b[j] = byte('a' + rng.Intn(16))
			if b[j] == 'q' {
				b[j] = 'r'
			}
		}
		pats[i] = string(b)
	}
	return pats
}

// concat repeats patterns round-robin until the result reaches size.
func concat(patterns []string, size int) []byte {
	var in []byte
	for i := 0; len(in) < size; i++ {
		in = append(in, patterns[i%len(patterns)]...)
	}
	return in[:size]
}

func TestChainHeavyLongPatternInput(t *testing.T) {
	for _, plen := range []int{32, 64, 100} {
		pats := buildLongPatterns(plen, 200)
		tr := buildStopByte16Trie(t, pats)
		in := concat(pats, 64<<10)

		// The workload this gate exists for: ~1-3% stop-byte density,
		// ~100% chain occupancy. looksDense must reject it (density is
		// below dualDenseThreshold) and chainHeavy must rescue it.
		if looksDense(in, tr.rootStopBytes[0]) {
			t.Fatalf("plen=%d: looksDense=true; input no longer exercises the low-density regime", plen)
		}
		if !tr.chainHeavy(in) {
			t.Errorf("plen=%d: chainHeavy=false for concatenated long patterns", plen)
		}
	}
}

func TestChainHeavySkipFriendlyInput(t *testing.T) {
	pats := buildLongPatterns(32, 200)
	tr := buildStopByte16Trie(t, pats)

	// Filler-separated occurrences: every fourth 32-byte pattern is a
	// 96-byte 'x' run the root skip leaps over (~25% occupancy), the
	// regime where the single cursor's inline skip wins.
	var in []byte
	for i := 0; len(in) < 64<<10; i++ {
		in = append(in, pats[i%len(pats)]...)
		in = append(in, bytesFill(96, 'x')...)
	}
	in = in[:64<<10]
	if tr.chainHeavy(in) {
		t.Error("chainHeavy=true for skip-friendly filler input")
	}

	// No stop byte at all: the skip budget must exhaust immediately.
	if tr.chainHeavy(bytesFill(64<<10, 'x')) {
		t.Error("chainHeavy=true for input without stop bytes")
	}

	// At or below 4KB the sample would be a significant fraction of the
	// scan, so the gate stays closed regardless of content.
	if tr.chainHeavy(concat(pats, 4096)) {
		t.Error("chainHeavy=true for 4KB input; small inputs must not pay the sample walk")
	}
}

// TestChainHeavyMidChainWindows pins the ambiguous-prefix exclusion: the
// middle and tail sample windows usually start inside an excursion, and
// the walk (which restarts from the root) must not charge the in-chain
// bytes before the window's first stop byte as root skips. Kilobyte-scale
// patterns make that mis-charge fatal - one unaligned window start would
// spend the whole skip budget - so the verdict must hold at every
// sampling phase.
func TestChainHeavyMidChainWindows(t *testing.T) {
	for _, plen := range []int{256, 512, 1024} {
		npat := min(30, ((1<<15)-64)/plen)
		pats := buildLongPatterns(plen, npat)
		tr := buildStopByte16Trie(t, pats)
		full := concat(pats, 66000+plen)
		for ph := 0; ph < 64; ph++ {
			in := full[ph*plen/64:]
			in = in[:66000]
			if !(len(in) >= dualThreshold && int(tr.maxLen)*4 < len(in)/2) {
				t.Fatalf("plen=%d: dispatch guard fails, bad test setup", plen)
			}
			if !tr.chainHeavy(in) {
				t.Errorf("plen=%d phase=%d: chainHeavy=false; window phase must not decide the verdict", plen, ph)
			}
		}
	}
}

// buildInternalStopPatterns returns npat patterns of length plen whose
// stop byte 'q' appears at offset 0 and again at plen/2, with an all-'z'
// tail: a mid-window sample usually lands after an internal stop byte,
// which must not derail the occupancy estimate.
func buildInternalStopPatterns(plen, npat int) []string {
	rng := rand.New(rand.NewSource(7))
	pats := make([]string, npat)
	for i := range pats {
		b := make([]byte, plen)
		b[0] = 'q'
		for j := 1; j < plen/2; j++ {
			b[j] = byte('a' + rng.Intn(16))
			if b[j] == 'q' {
				b[j] = 'r'
			}
		}
		b[plen/2] = 'q'
		for j := plen/2 + 1; j < plen; j++ {
			b[j] = 'z'
		}
		pats[i] = string(b)
	}
	return pats
}

// TestChainHeavyExactStateRecovery pins the warm-up replay: the sample
// walk must hold the real scan's state at every counted byte, so the
// verdict cannot be derailed by where a window happens to land. Three
// window placements that defeated the previous window-local heuristics:
//
//   - internal stop bytes: a window starting after a pattern's internal
//     'q' must not re-enter the automaton at the wrong prefix and charge
//     the rest of the occurrence as skips;
//   - patterns wider than the 1KB window: a window wholly inside an
//     excursion has no stop byte at all and must still count as in-chain
//     evidence;
//   - both fully in-chain shapes must pass at every sampling phase.
func TestChainHeavyExactStateRecovery(t *testing.T) {
	cases := []struct {
		name string
		pats []string
	}{
		{"internal-stop-plen1024", buildInternalStopPatterns(1024, 30)},
		{"wider-than-window-plen2048", buildLongPatterns(2048, 15)},
	}
	for _, tc := range cases {
		tr := buildStopByte16Trie(t, tc.pats)
		full := concat(tc.pats, 68000)
		for ph := 0; ph < 32; ph++ {
			in := full[ph*63:]
			in = in[:64000]
			if !(len(in) >= dualThreshold && int(tr.maxLen)*4 < len(in)/2) {
				t.Fatalf("%s: dispatch guard fails, bad test setup", tc.name)
			}
			if !tr.chainHeavy(in) {
				t.Errorf("%s phase=%d: chainHeavy=false on a fully in-chain input", tc.name, ph)
			}
		}
	}
}

// TestChainHeavyLargeMaxLenFillerInput pins the false-positive side of
// the warm-up replay: with kilobyte-scale maxLen, real root-skip gaps
// inside the windows must still be charged (the previous
// ambiguous-prefix exclusion hid up to maxLen-1 leading skip bytes per
// window and misrouted ~1/3-occupancy inputs to the dual scan on most
// phases). A phase or two can still land all three windows inside
// pattern bodies - that is sampling variance, not systematic bias - so
// the test bounds the accept rate well below the biased behavior.
func TestChainHeavyLargeMaxLenFillerInput(t *testing.T) {
	pats := buildLongPatterns(1024, 30)
	tr := buildStopByte16Trie(t, pats)
	var sb []byte
	for i := 0; len(sb) < 132<<10; i++ {
		sb = append(sb, pats[i%len(pats)]...)
		sb = append(sb, bytesFill(2048, 'x')...)
	}
	trues := 0
	for ph := 0; ph < 32; ph++ {
		in := sb[ph*96:]
		in = in[:128<<10]
		if tr.chainHeavy(in) {
			trues++
		}
	}
	if trues > 8 {
		t.Errorf("chainHeavy accepted %d/32 phases of a ~1/3-occupancy input; want a small minority (sampling variance only)", trues)
	}
}

// TestDifferentialLongPatternDual cross-checks Match against the naive
// reference on the chain-heavy input that chainHeavy routes to the
// dual-cursor scan, so the rescued dispatch path stays correct end to end.
func TestDifferentialLongPatternDual(t *testing.T) {
	pats := buildLongPatterns(64, 100)
	tr := buildStopByte16Trie(t, pats)
	// Sized inside the serial window: big enough for chainHeavy's 4KB
	// floor, below Match's 2*parallelChunk parallel dispatch, so the
	// scan is guaranteed to go matchSeq -> chainHeavy -> dual-cursor
	// regardless of GOMAXPROCS.
	in := concat(pats, 12<<10)
	if len(in) >= 2*parallelChunk {
		t.Fatal("test setup: input must stay below the parallel dispatch threshold")
	}
	if looksDense(in, tr.rootStopBytes[0]) {
		t.Fatal("test setup: input must stay below the dense dispatch threshold")
	}
	if !tr.chainHeavy(in) {
		t.Fatal("test setup: input must take the chain-heavy dual path")
	}

	want := naiveMatch(pats, in)
	ms := tr.Match(in)
	defer tr.ReleaseMatches(ms)
	if len(ms) != len(want) {
		t.Fatalf("match count: got %d, want %d", len(ms), len(want))
	}
	for i, m := range ms {
		if m.Pos() != want[i][0] || m.Pattern() != want[i][1] || len(m.Match()) != int(want[i][2]) {
			t.Fatalf("match %d: got (pos=%d, pat=%d, len=%d), want (%d, %d, %d)",
				i, m.Pos(), m.Pattern(), len(m.Match()), want[i][0], want[i][1], want[i][2])
		}
	}
}
