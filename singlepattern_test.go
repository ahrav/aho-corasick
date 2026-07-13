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
