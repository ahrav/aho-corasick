package ahocorasick

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestTransCEquivalence cross-checks matchTableC against matchTable
// directly: same trie, same input, transC force-disabled for the
// reference run. This is a white-box differential that isolates the
// compressed table from the naive-reference tests.
func TestTransCEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 40; trial++ {
		numPat := 2 + rng.Intn(24)
		alpha := []byte("abcdefgh\x00\xff")
		seen := map[string]bool{}
		var pats []string
		for len(pats) < numPat {
			l := 1 + rng.Intn(7)
			b := make([]byte, l)
			for i := range b {
				b[i] = alpha[rng.Intn(len(alpha))]
			}
			if s := string(b); !seen[s] {
				seen[s] = true
				pats = append(pats, s)
			}
		}
		tr := NewTrieBuilder().AddStrings(pats).Build()
		if tr.transC == nil {
			continue // single-stop-byte trie; nothing to compare
		}

		size := []int{16, 300, 5000, 20000}[rng.Intn(4)]
		input := make([]byte, size)
		for i := range input {
			if rng.Intn(3) == 0 {
				input[i] = byte('p' + rng.Intn(8)) // outside alphabet
			} else {
				input[i] = alpha[rng.Intn(len(alpha))]
			}
		}

		var bufC, bufT matchBuf
		tr.matchTableC(input, &bufC)
		tr.matchTable(input, &bufT)
		if !equalU64(bufC.raw, bufT.raw) {
			t.Fatalf("trial %d: transC diverges from matchTable\npats=%q\ninput=%q\nC=%v\nT=%v",
				trial, pats, input, bufC.raw, bufT.raw)
		}
	}
}

func equalU64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTransCDecodeRebuild verifies a decoded trie rebuilds transC and
// matches identically to the original on a multi-stop-byte pattern set.
func TestTransCDecodeRebuild(t *testing.T) {
	pats := []string{"ab", "bc", "ca", "abc", "\x00x", "\xffy"}
	tr := NewTrieBuilder().AddStrings(pats).Build()
	if tr.transC == nil {
		t.Fatal("expected transC on multi-stop trie")
	}
	var buf bytes.Buffer
	if err := Encode(&buf, tr); err != nil {
		t.Fatal(err)
	}
	dec, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if dec.transC == nil {
		t.Fatal("decoded trie did not rebuild transC")
	}
	input := []byte("zabcaz\x00x\xffy zz bc")
	want := naiveMatch(pats, input)
	got := dec.Match(input)
	if len(got) != len(want) {
		t.Fatalf("decoded: want %d matches, got %d", len(want), len(got))
	}
	for i, m := range got {
		if m.Pos() != want[i][0] || m.Pattern() != want[i][1] || uint32(len(m.Match())) != want[i][2] {
			t.Fatalf("decoded match %d: got (%d,%d,%d), want %v", i, m.Pos(), m.Pattern(), len(m.Match()), want[i])
		}
	}
	dec.ReleaseMatches(got)
}
