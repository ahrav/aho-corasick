package ahocorasick

// Tests for the single-pattern fast path (buildSinglePattern, matchSingle,
// walkSingle): detection of the one-pattern trie shape, byte-for-byte
// agreement with the generic automaton paths and the naive reference, KMP
// period handling on periodic patterns, Walk termination, and survival of
// the Encode/Decode roundtrip.

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

func TestSinglePatternDetection(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     string // "" => single must be nil
	}{
		{"one word", []string{"Hedvig"}, "Hedvig"},
		{"one byte", []string{"x"}, "x"},
		{"duplicate adds collapse", []string{"abc", "abc"}, "abc"},
		{"periodic", []string{"abab"}, "abab"},
		{"self-overlap run", []string{"aa"}, "aa"},
		{"utf8 bytes", []string{"æøå"}, "æøå"},
		{"two patterns", []string{"ab", "cd"}, ""},
		{"prefix pair", []string{"ab", "abc"}, ""},
		{"suffix pair", []string{"bc", "abc"}, ""},
		{"long", []string{strings.Repeat("abz", 40)}, strings.Repeat("abz", 40)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTrieBuilder().AddStrings(tc.patterns).Build()
			if tc.want == "" {
				if tr.single != nil {
					t.Fatalf("single = %q, want nil", tr.single)
				}
				return
			}
			if string(tr.single) != tc.want {
				t.Fatalf("single = %q, want %q", tr.single, tc.want)
			}
			if got, want := uint32(tr.singleDP), uint32(len(tc.want)); got != want {
				t.Fatalf("singleDP length = %d, want %d", got, want)
			}
		})
	}
}

func TestSinglePatternSkipIsKMPPeriod(t *testing.T) {
	cases := []struct {
		pattern string
		skip    int
	}{
		{"a", 1},
		{"aa", 1},
		{"ab", 2},
		{"aba", 2},
		{"abab", 2},
		{"aabaa", 3},
		{"Hedvig", 6},
	}
	for _, tc := range cases {
		tr := NewTrieBuilder().AddString(tc.pattern).Build()
		if tr.singleSkip != tc.skip {
			t.Errorf("%q: singleSkip = %d, want %d", tc.pattern, tr.singleSkip, tc.skip)
		}
	}
}

// singleInputs exercises: no match, match at 0, match at end, overlapping
// periodic matches, inputs shorter than the pattern, and empty input.
func singleInputs(pattern string) [][]byte {
	rep := bytes.Repeat([]byte(pattern), 5)
	return [][]byte{
		nil,
		[]byte(""),
		[]byte(pattern),
		[]byte(pattern[:len(pattern)-1]),
		[]byte(pattern + " tail"),
		[]byte("head " + pattern),
		[]byte("no occurrences here at all"),
		rep,
		bytes.Repeat(append([]byte(pattern), ' '), 3),
		append(bytes.Repeat([]byte{pattern[0]}, 20), pattern...),
	}
}

func TestSinglePatternMatchesReference(t *testing.T) {
	for _, pattern := range []string{"a", "aa", "ab", "aba", "abab", "aabaa", "Hedvig", "æøå"} {
		t.Run(pattern, func(t *testing.T) {
			tr := NewTrieBuilder().AddString(pattern).Build()
			if tr.single == nil {
				t.Fatal("single not detected")
			}
			for _, input := range singleInputs(pattern) {
				want := naiveMatch([]string{pattern}, input)

				ms := tr.Match(input)
				if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
					t.Fatalf("Match(%q) diverges from reference at %d", input, d)
				}
				tr.ReleaseMatches(ms)

				if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
					t.Fatalf("Walk(%q) diverges from reference at %d", input, d)
				}

				// The generic automaton path must agree too: disable
				// the fast path and rerun (white-box).
				save := tr.single
				tr.single = nil
				ms = tr.Match(input)
				if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
					t.Fatalf("generic Match(%q) diverges from reference at %d", input, d)
				}
				tr.ReleaseMatches(ms)
				tr.single = save
			}
		})
	}
}

func TestSinglePatternMatchFirst(t *testing.T) {
	tr := NewTrieBuilder().AddString("needle").Build()
	input := []byte("hay needle hay needle")
	m := tr.MatchFirst(input)
	if m == nil || m.Pos() != 4 || m.MatchString() != "needle" {
		t.Fatalf("MatchFirst = %v, want needle at 4", m)
	}
	if m := tr.MatchFirst([]byte("no hit")); m != nil {
		t.Fatalf("MatchFirst on miss = %v, want nil", m)
	}
}

func TestSinglePatternWalkStops(t *testing.T) {
	tr := NewTrieBuilder().AddString("aa").Build()
	calls := 0
	tr.Walk([]byte("aaaa"), func(end, n, pattern uint32) bool {
		calls++
		return false
	})
	if calls != 1 {
		t.Fatalf("Walk called callback %d times after false, want 1", calls)
	}
}

func TestSinglePatternDecodeRoundtrip(t *testing.T) {
	tr := NewTrieBuilder().AddString("abab").Build()
	var blob bytes.Buffer
	if err := Encode(&blob, tr); err != nil {
		t.Fatal(err)
	}
	dec, err := Decode(&blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(dec.single) != "abab" {
		t.Fatalf("decoded single = %q, want %q", dec.single, "abab")
	}
	if dec.singleSkip != tr.singleSkip {
		t.Fatalf("decoded singleSkip = %d, want %d", dec.singleSkip, tr.singleSkip)
	}
	input := []byte("xxababab yy abab")
	want := naiveMatch([]string{"abab"}, input)
	ms := dec.Match(input)
	if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
		t.Fatalf("decoded Match diverges from reference at %d", d)
	}
	dec.ReleaseMatches(ms)
}

// rawTriples converts matchBuf raw pairs to reference triples.
func rawTriples(raw []uint64) [][3]uint32 {
	var out [][3]uint32
	for k := 0; k+1 < len(raw); k += 2 {
		end, dp := raw[k], raw[k+1]
		l := uint32(dp)
		out = append(out, [3]uint32{uint32(end) - l + 1, uint32(dp >> 32), l})
	}
	return out
}

// TestSingleStrategiesDifferential cross-checks both single-pattern
// search strategies (rare-byte IndexByte and SWAR pair) directly against
// the naive reference, on inputs dense and large enough to exercise the
// pair scan's word loop, its KMP-period suppression, and its scalar tail.
func TestSingleStrategiesDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	patterns := []string{"ab", "aa", "aba", "abab", "aabaa", "xyzzy", "Hedvig"}
	alphabets := []string{"ab", "abcxyz", "abcdefghijklmnopqrstuvwxyz "}
	sizes := []int{0, 1, 7, 63, 100, 1000, 8192, 9001, 32768}

	for _, pat := range patterns {
		tr := NewTrieBuilder().AddString(pat).Build()
		if tr.single == nil {
			t.Fatalf("%q: single not detected", pat)
		}
		for _, alpha := range alphabets {
			for _, size := range sizes {
				input := make([]byte, size)
				for i := range input {
					input[i] = alpha[rng.Intn(len(alpha))]
				}
				// Plant some real occurrences.
				for k := 0; k < size/64; k++ {
					pos := rng.Intn(size)
					copy(input[pos:], pat)
				}
				want := naiveMatch([]string{pat}, input)

				var bufA, bufB matchBuf
				tr.singleRareMatch(input, &bufA)
				if d := diffTriples(rawTriples(bufA.raw), want); d != -1 {
					t.Fatalf("%q/%s/%d: rare-byte diverges at %d", pat, alpha, size, d)
				}
				tr.singlePairMatch(input, &bufB)
				if d := diffTriples(rawTriples(bufB.raw), want); d != -1 {
					t.Fatalf("%q/%s/%d: pair scan diverges at %d", pat, alpha, size, d)
				}
			}
		}
	}
}

// TestSingleWalkStrategiesStop verifies early termination in both
// strategy walk variants on dense input.
func TestSingleWalkStrategiesStop(t *testing.T) {
	tr := NewTrieBuilder().AddString("ab").Build()
	input := bytes.Repeat([]byte("ab"), 8192) // 16KB, dense
	for name, walk := range map[string]func([]byte, WalkFn){
		"rare": tr.singleRareWalk,
		"pair": tr.singlePairWalk,
	} {
		calls := 0
		walk(input, func(end, n, pattern uint32) bool {
			calls++
			return calls < 3
		})
		if calls != 3 {
			t.Errorf("%s: %d calls after stop at 3", name, calls)
		}
	}
}

// TestSingleLargeInputs exercises the large-input single-pattern paths
// end-to-end (on arm64 the vector kernel and its adaptive handover to
// the SWAR scan; elsewhere the sampled strategies) against the naive
// reference: sparse prose-like input, pair-dense periodic input that
// forces the mid-stream switch, and a hit landing in the scalar tail
// after the last full block.
func TestSingleLargeInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))
	for _, pattern := range []string{"ab", "aba", "Hedvig", "imorges"} {
		tr := NewTrieBuilder().AddString(pattern).Build()

		// Sparse: 64KB filler with occasional plants.
		sparse := make([]byte, 64<<10)
		for i := range sparse {
			sparse[i] = byte(' ' + rng.Intn(90))
		}
		for k := 0; k < 40; k++ {
			copy(sparse[rng.Intn(len(sparse)-len(pattern)):], pattern)
		}
		// Tail hit: plant in the final partial block.
		copy(sparse[len(sparse)-len(pattern)-3:], pattern)

		// Dense: the pattern repeated back-to-back (maximum overlap and
		// candidate density; trips the kernel's adaptive bailout).
		dense := bytes.Repeat([]byte(pattern), (32<<10)/len(pattern))

		for name, input := range map[string][]byte{"sparse": sparse, "dense": dense} {
			want := naiveMatch([]string{pattern}, input)
			ms := tr.Match(input)
			if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
				t.Fatalf("%q/%s: Match diverges from reference at %d", pattern, name, d)
			}
			tr.ReleaseMatches(ms)
			if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
				t.Fatalf("%q/%s: Walk diverges from reference at %d", pattern, name, d)
			}
		}
	}
}

// TestSingleLongDistanceFilter drives the kernel paths end-to-end with a
// pattern whose two selected filter bytes sit more than one 32-byte block
// apart, so the kernel's second-byte load crosses into a later block than
// the first. The direct differential and guard-page tests already cover
// large d values on synthetic buffers; what is unique here is the full
// pipeline — the builder's offset selection, candidate-position mapping,
// and the scalar-tail handoff — driven through Trie-derived offsets and
// checked against the reference (the read contract itself is proved by
// the guard-page test, which this heap-backed test cannot).
func TestSingleLongDistanceFilter(t *testing.T) {
	// '7' (digit, rank 80) and 'Q' (uppercase, rank 85) are the two
	// rarest bytes; every middle byte is a common lowercase letter.
	pattern := "Q" + strings.Repeat("eta", 13) + "7"
	tr := NewTrieBuilder().AddString(pattern).Build()
	if tr.single == nil {
		t.Fatal("single not detected")
	}
	if d := absInt(tr.singleO1 - tr.singleO2); d <= 32 {
		t.Fatalf("filter distance %d, want > 32; offsets %d,%d",
			d, tr.singleO1, tr.singleO2)
	}

	input := bytes.Repeat([]byte("loremipsu"), 1000) // 9KB, no 'Q'/'7'
	// Full-block hits, including adjacent occurrences.
	copy(input[100:], pattern)
	copy(input[100+len(pattern):], pattern)
	copy(input[4096:], pattern)
	// Scalar-tail hit: the last valid start, past the final full block.
	copy(input[len(input)-len(pattern):], pattern)

	want := naiveMatch([]string{pattern}, input)
	if len(want) != 4 {
		t.Fatalf("reference found %d occurrences, want 4", len(want))
	}
	ms := tr.Match(input)
	if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
		t.Fatalf("Match diverges from reference at %d", d)
	}
	tr.ReleaseMatches(ms)
	if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
		t.Fatalf("Walk diverges from reference at %d", d)
	}
}

// TestSinglePatternRejectsNoncanonicalTable corrupts individual
// transition entries of canonical single-pattern tables and asserts the
// detector turns the fast path off: the shape checks (state count, one
// output, one chain edge per state) can all pass while a non-chain
// entry still disagrees with the KMP automaton the pattern implies, and
// scanning with the recovered pattern would then diverge from the
// generic paths. Decode accepts any in-range table, so such tables are
// reachable from corrupt or hostile streams.
func TestSinglePatternRejectsNoncanonicalTable(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		state   uint32 // state whose row to corrupt
		b       byte   // row entry to corrupt
		to      uint32 // new target (plain state id)
	}{
		// The final state must re-enter the chain on the pattern byte
		// (overlapping occurrence); dropping to the root makes the
		// generic path emit once on "aa" where the recovered-pattern
		// path emits twice.
		{"final drops to root", "a", 2, 'a', rootState},
		// A root non-pattern byte must self-loop, not jump into the
		// chain.
		{"root leaves on foreign byte", "ab", rootState, 'z', 3},
		// A mid-chain non-chain byte must copy the fail state's row,
		// not self-loop.
		{"mid-chain wrong fallback", "abab", 3, 'z', 3},
		// A periodic pattern's chain state must fall back into the
		// chain on its period byte, not to the root.
		{"periodic fallback to root", "abab", 4, 'a', rootState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTrieBuilder().AddString(tc.pattern).Build()
			if tr.single == nil {
				t.Fatal("canonical table not detected as single")
			}
			if old := tr.failTrans[tc.state][tc.b] & stateMask; old == tc.to {
				t.Fatalf("corruption is a no-op: entry already %d", tc.to)
			}
			tr.failTrans[tc.state][tc.b] = tc.to
			tr.addOutputFlags() // re-derive flags for the new target
			tr.buildSinglePattern()
			if tr.single != nil {
				t.Fatalf("noncanonical table still detected: single = %q", tr.single)
			}
		})
	}
}

// TestSinglePatternPeriodicCarry cross-checks the KMP-period carry
// (singleVerifyCarry) in all searchers against the naive reference on
// inputs built to stress it: long runs of overlapping occurrences
// (where the carry verifies only the tail bytes per match), runs broken
// by a corrupted byte at every alignment near a match boundary (where a
// carry-qualified candidate must still fail), and occurrences at
// non-period distances (where the carry must not apply).
func TestSinglePatternPeriodicCarry(t *testing.T) {
	patterns := []string{
		"aa",                     // period 1, byte-loop verify
		strings.Repeat("a", 10),  // period 1, word-loop verify
		strings.Repeat("ab", 6),  // period 2
		"aabaa",                  // period 3, bordered
		strings.Repeat("abz", 8), // period 3, longer
		"aaaaaaab",               // period n (no overlap savings)
	}
	for _, pat := range patterns {
		tr := NewTrieBuilder().AddString(pat).Build()
		if tr.single == nil {
			t.Fatalf("%q: single not detected", pat)
		}
		var inputs [][]byte
		// Pure periodic runs: maximal overlap, matches every period.
		inputs = append(inputs,
			bytes.Repeat([]byte(pat), 40),
			bytes.Repeat([]byte(pat[:tr.singleSkip]), 40*len(pat)/tr.singleSkip),
		)
		// Runs broken at every offset around the second occurrence: the
		// carry proves the prefix, so the corrupted byte must be caught
		// by the tail compare.
		base := bytes.Repeat([]byte(pat), 40)
		for off := len(pat); off < 3*len(pat) && off < len(base); off++ {
			in := append([]byte(nil), base...)
			in[off] ^= 0xFF
			inputs = append(inputs, in)
		}
		// Occurrences at a non-period gap: carry must not apply.
		gap := append([]byte(pat), 'q', 'w')
		gap = append(gap, pat...)
		inputs = append(inputs, gap)

		for k, input := range inputs {
			want := naiveMatch([]string{pat}, input)
			var bufA, bufB matchBuf
			tr.singleRareMatch(input, &bufA)
			if d := diffTriples(rawTriples(bufA.raw), want); d != -1 {
				t.Fatalf("%q input %d: rare-byte diverges at %d", pat, k, d)
			}
			tr.singlePairMatch(input, &bufB)
			if d := diffTriples(rawTriples(bufB.raw), want); d != -1 {
				t.Fatalf("%q input %d: pair scan diverges at %d", pat, k, d)
			}
			if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
				t.Fatalf("%q input %d: walk diverges at %d", pat, k, d)
			}
		}
	}
}

// TestSingleWalkDensitySwitch drives walkSingle over inputs dense
// enough to trip the inline rare-to-pair switch and verifies the
// handoff loses and duplicates nothing across the boundary.
func TestSingleWalkDensitySwitch(t *testing.T) {
	for _, pat := range []string{"ab", "aa", "abab", "aabaa"} {
		tr := NewTrieBuilder().AddString(pat).Build()
		if tr.single == nil {
			t.Fatalf("%q: single not detected", pat)
		}
		denseTail := bytes.Repeat([]byte("qw"), 4096)
		denseTail = append(denseTail, bytes.Repeat([]byte(pat), 4096/len(pat))...)
		inputs := [][]byte{
			bytes.Repeat([]byte(pat), 8192/len(pat)), // dense from byte 0
			bytes.Repeat([]byte(pat[:1]), 16384),     // rare byte everywhere
			denseTail,                                // sparse head, dense tail
		}
		for k, input := range inputs {
			want := naiveMatch([]string{pat}, input)
			if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
				t.Fatalf("%q input %d: walk diverges at %d", pat, k, d)
			}
		}
	}
}

// TestSinglePhaseTransitions stresses the kernel searchers' two-way
// dense/sparse handoff (on arm64; elsewhere the sampled strategies)
// with inputs that alternate phases: dense islands committing to the
// SWAR scan must hand back to the kernel on the sparse stretches that
// follow, dense suffixes must still trip the switch, and a dense island
// followed by a candidate-free remainder must not strand the scan on
// the slow path. Every shape is checked against the naive reference
// through both Match and Walk.
func TestSinglePhaseTransitions(t *testing.T) {
	for _, pat := range []string{"ab", "abab", "aabaa", "Hedvig"} {
		tr := NewTrieBuilder().AddString(pat).Build()
		if tr.single == nil {
			t.Fatalf("%q: single not detected", pat)
		}
		sparse := bytes.Repeat([]byte("qw"), 16384) // 32KB, no candidates
		island := bytes.Repeat([]byte(pat), 2048/len(pat))

		// Island then long sparse tail (with one late plant).
		islandTail := append(append([]byte{}, island...), sparse...)
		copy(islandTail[len(islandTail)-len(pat)-7:], pat)
		// Sparse head, island, sparse tail: both transitions.
		sandwich := append(append(append([]byte{}, sparse...), island...), sparse...)
		// Alternating islands and gaps: repeated transitions.
		var alternating []byte
		for range 6 {
			alternating = append(alternating, island...)
			alternating = append(alternating, sparse[:4096]...)
		}
		// Island then candidate-free remainder: the kernel must keep
		// the suffix (no SWAR strand) and still match the reference.
		islandEmpty := append(append([]byte{}, island...), sparse[:16384]...)

		for k, input := range [][]byte{islandTail, sandwich, alternating, islandEmpty} {
			want := naiveMatch([]string{pat}, input)
			ms := tr.Match(input)
			if d := diffTriples(triplesFromMatches(ms), want); d != -1 {
				t.Fatalf("%q input %d: Match diverges at %d", pat, k, d)
			}
			tr.ReleaseMatches(ms)
			if d := diffTriples(tr.triplesFromWalk(input), want); d != -1 {
				t.Fatalf("%q input %d: Walk diverges at %d", pat, k, d)
			}
		}
	}
}
