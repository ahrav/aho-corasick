package ahocorasick

// Tests for the excursion-shape dispatch gate (dualWorthwhile /
// chainSample): the routing signal that catches what stop-byte density
// alone misses. Density measures excursion starts; the dual-cursor win
// tracks excursion LENGTH (long serial transition chains), so the gate
// samples mean chain length and overrides the density verdict in the
// decisive zones (dualChainLongMin / dualChainShortMax) on both sides.
// These tests pin the routing on both sides of each boundary and
// cross-check the rescued path against the naive reference.

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

// buildInternalStopPatterns returns npat patterns of length plen whose
// stop byte 'q' appears at offset 0 and again at plen/2, with an all-'z'
// tail: a mid-window sample usually lands after an internal stop byte,
// which must not derail the excursion measurement.
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

// concat repeats patterns round-robin until the result reaches size.
func concat(patterns []string, size int) []byte {
	var in []byte
	for i := 0; len(in) < size; i++ {
		in = append(in, patterns[i%len(patterns)]...)
	}
	return in[:size]
}

// TestDualWorthwhileLongChainInput pins the long-chain override: inputs
// whose excursions are long serial transition chains must route to the
// dual scan even though their stop-byte density is far below
// dualDenseThreshold (1-3% here), and whether or not filler gaps push
// occupancy down (dual measures ~1.4-2x faster on these shapes even at
// ~25% occupancy).
func TestDualWorthwhileLongChainInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		plen int
		fill int
	}{
		{"concat-plen32", 32, 0},
		{"concat-plen64", 64, 0},
		{"concat-plen100", 100, 0},
		{"gapped-plen32-fill96", 32, 96},
		{"gapped-plen100-fill100", 100, 100},
	} {
		pats := buildLongPatterns(tc.plen, 200)
		tr := buildStopByte16Trie(t, pats)
		var in []byte
		for i := 0; len(in) < 64<<10; i++ {
			in = append(in, pats[i%len(pats)]...)
			in = append(in, bytesFill(tc.fill, 'x')...)
		}
		in = in[:64<<10]
		if looksDense(in, tr.rootStopBytes[0]) {
			t.Fatalf("%s: looksDense=true; input no longer exercises the low-density regime", tc.name)
		}
		if !tr.dualWorthwhile(in) {
			t.Errorf("%s: dualWorthwhile=false for a long-chain input", tc.name)
		}
	}
}

// TestDualWorthwhileShortChainInput pins the short-chain side: inputs
// whose excursions die within a few bytes must stay on the single
// cursor. The false-start shape (stop byte frequent, chains dying at
// depth ~2) is the critical case: it passes the density gate, so only
// the sampled chain length keeps it off the dual path (single measures
// ~1.4x faster there).
func TestDualWorthwhileShortChainInput(t *testing.T) {
	// False starts: patterns all continue 'qa...', haystack repeats
	// 'q' + filler, so every excursion is a depth-1 false start.
	pats := buildLongPatterns(16, 200)
	for i, p := range pats {
		pats[i] = "qa" + p[2:]
	}
	tr := buildStopByte16Trie(t, pats)
	var in []byte
	for len(in) < 64<<10 {
		in = append(in, 'q')
		in = append(in, bytesFill(8, 'x')...)
	}
	in = in[:64<<10]
	if !looksDense(in, tr.rootStopBytes[0]) {
		t.Fatal("test setup: false-start input must pass the density gate")
	}
	if tr.dualWorthwhile(in) {
		t.Error("dualWorthwhile=true for a dense false-start input; short chains must veto the density verdict")
	}

	// No stop byte at all: nothing sampled, low density decides.
	if tr.dualWorthwhile(bytesFill(64<<10, 'x')) {
		t.Error("dualWorthwhile=true for input without stop bytes")
	}
}

// TestDualWorthwhileGrayZoneDefersToDensity pins the gray zone:
// word-length excursions (~9-13 bytes) sit between the decisive bands,
// where the winner is workload- and machine-dependent, so the density
// calibration must decide exactly as it did before the chain sampler
// existed.
func TestDualWorthwhileGrayZoneDefersToDensity(t *testing.T) {
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	if tr.failTrans16 == nil || len(tr.rootStopBytes) != 1 {
		t.Fatal("test setup: dictionary trie must take the 16-bit single-stop-byte path")
	}
	for _, tc := range []struct {
		gap  int
		want bool
	}{
		{0, true},   // concatenated words: dense -> dual
		{16, false}, // wide gaps: sparse -> single
	} {
		var in []byte
		for i := 0; len(in) < 96<<10; i++ {
			in = append(in, patterns[i%10000]...)
			in = append(in, bytesFill(tc.gap, 'x')...)
		}
		in = in[:96<<10]
		dense := looksDense(in, tr.rootStopBytes[0])
		if dense != tc.want {
			t.Fatalf("test setup: gap=%d want looksDense=%v", tc.gap, tc.want)
		}
		if got := tr.dualWorthwhile(in); got != tc.want {
			t.Errorf("gap=%d: dualWorthwhile=%v, want the density verdict %v (gray zone must defer)", tc.gap, got, tc.want)
		}
	}
}

// TestDualWorthwhileSampleFloor pins the sampling cost floor: below
// dualChainFloor*(maxLen-1+1024) the warm-up replays cannot pay for
// themselves, so the gate must fall back to the density verdict without
// sampling.
func TestDualWorthwhileSampleFloor(t *testing.T) {
	pats := buildLongPatterns(600, 50)
	tr := buildStopByte16Trie(t, pats)
	// 5KB of concatenated 600-byte patterns: passes the dispatch size
	// guards (n > 8*maxLen fails -> actually guarded upstream), sits
	// under the sampling floor, and is sparse, so the verdict must be
	// the density one (false) with no replay.
	in := concat(pats, 5<<10)
	if got, want := len(in) < dualChainFloor*(int(tr.maxLen)+1024), true; got != want {
		t.Fatal("test setup: input must sit below the sampling floor")
	}
	if tr.dualWorthwhile(in) {
		t.Error("dualWorthwhile=true below the sampling floor for a sparse input")
	}
	// The same shape above the floor is rescued.
	big := concat(pats, 20<<10)
	if len(big) < dualChainFloor*(int(tr.maxLen)+1024) {
		t.Fatal("test setup: 20KB input must sit above the sampling floor")
	}
	if !tr.dualWorthwhile(big) {
		t.Error("dualWorthwhile=false above the sampling floor for a long-chain input")
	}
}

// TestChainSampleExactStateRecovery pins the warm-up replay: the sample
// walk must hold the real scan's state at every counted byte, so the
// verdict cannot be derailed by where a window happens to land. Window
// placements that defeated earlier window-local heuristics, swept across
// sampling phases:
//
//   - internal stop bytes: a window starting after a pattern's internal
//     'q' must not re-enter the automaton at the wrong prefix and
//     misread the rest of the occurrence;
//   - patterns wider than the 1KB window: a window wholly inside an
//     excursion has no stop byte at all and must still count as one
//     long excursion rather than no evidence.
func TestChainSampleExactStateRecovery(t *testing.T) {
	cases := []struct {
		name string
		pats []string
	}{
		{"internal-stop-plen1024", buildInternalStopPatterns(1024, 30)},
		{"wider-than-window-plen2048", buildLongPatterns(2048, 15)},
		{"mid-chain-plen512", buildLongPatterns(512, 60)},
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
			if !tr.dualWorthwhile(in) {
				t.Errorf("%s phase=%d: dualWorthwhile=false on a fully in-chain input", tc.name, ph)
			}
		}
	}
}

// TestDualWorthwhileUnrepresentativeWindows pins the per-window voting:
// a pocket of unrepresentative input must not decide the dispatch for
// the rest.
func TestDualWorthwhileUnrepresentativeWindows(t *testing.T) {
	// A 2KB false-start prefix (would exhaust a shared sampling budget
	// with dozens of depth-1 excursions) ahead of a long-chain body:
	// the windows past the prefix must outvote it.
	pats := buildLongPatterns(64, 100)
	for i, p := range pats {
		pats[i] = "qa" + p[2:]
	}
	tr := buildStopByte16Trie(t, pats)
	var in []byte
	for len(in) < 2048 {
		in = append(in, 'q')
		in = append(in, bytesFill(8, 'x')...)
	}
	in = in[:2048]
	in = append(in, concat(pats, 62<<10)...)
	if !tr.dualWorthwhile(in) {
		t.Error("dualWorthwhile=false: a noisy 2KB prefix outvoted 62KB of long chains")
	}

	// Filler pockets planted exactly on looksDense's head/mid/tail
	// windows, long chains everywhere else: the chain windows sit at
	// different offsets, so the two signals must not share blind spots.
	pats2 := buildLongPatterns(32, 200)
	tr2 := buildStopByte16Trie(t, pats2)
	n := 12 << 10
	body := concat(pats2, n)
	fill := bytesFill(1200, 'x')
	copy(body[0:], fill)
	copy(body[n/2-100:], fill)
	copy(body[n-1200:], fill)
	if looksDense(body, tr2.rootStopBytes[0]) {
		t.Fatal("test setup: filler must blind the density windows")
	}
	if !tr2.dualWorthwhile(body) {
		t.Error("dualWorthwhile=false: chain windows share looksDense's blind spots")
	}
}

// TestDifferentialLongPatternDual cross-checks Match against the naive
// reference on the long-chain input that dualWorthwhile routes to the
// dual-cursor scan, so the rescued dispatch path stays correct end to end.
func TestDifferentialLongPatternDual(t *testing.T) {
	pats := buildLongPatterns(64, 100)
	tr := buildStopByte16Trie(t, pats)
	// Sized inside the serial window: above the chain-sampling floor,
	// below Match's 2*parallelChunk parallel dispatch, so the scan is
	// guaranteed to go matchSeq -> dualWorthwhile -> dual-cursor
	// regardless of GOMAXPROCS.
	in := concat(pats, 12<<10)
	if len(in) >= 2*parallelChunk {
		t.Fatal("test setup: input must stay below the parallel dispatch threshold")
	}
	if looksDense(in, tr.rootStopBytes[0]) {
		t.Fatal("test setup: input must stay below the dense dispatch threshold")
	}
	if !tr.dualWorthwhile(in) {
		t.Fatal("test setup: input must take the long-chain dual path")
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
