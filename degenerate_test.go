package ahocorasick

import (
	"bytes"
	"testing"
)

// Degenerate tries: zero patterns, a single empty pattern, and one
// single-byte pattern. These hit buildTransC's minimum-stride path and
// the pattern-less dispatch.
func TestDegenerateTries(t *testing.T) {
	t.Run("no_patterns", func(t *testing.T) {
		tr := NewTrieBuilder().Build()
		if got := tr.Match([]byte("anything at all \x00\xff")); got != nil {
			t.Fatalf("no-pattern trie matched: %v", got)
		}
		if m := tr.MatchFirst([]byte("xyz")); m != nil {
			t.Fatalf("no-pattern MatchFirst: %v", m)
		}
		var b bytes.Buffer
		if err := Encode(&b, tr); err != nil {
			t.Fatal(err)
		}
		dec, err := Decode(&b)
		if err != nil {
			t.Fatal(err)
		}
		if got := dec.Match([]byte("anything")); got != nil {
			t.Fatalf("decoded no-pattern trie matched: %v", got)
		}
	})
	t.Run("empty_pattern", func(t *testing.T) {
		tr := NewTrieBuilder().AddString("").Build()
		if got := tr.Match([]byte("abc")); got != nil {
			t.Fatalf("empty-pattern trie matched: %v", got)
		}
	})
	t.Run("one_byte_pattern", func(t *testing.T) {
		tr := NewTrieBuilder().AddString("q").Build()
		got := tr.Match([]byte("qq q"))
		if len(got) != 3 {
			t.Fatalf("want 3 matches, got %v", got)
		}
		tr.ReleaseMatches(got)
	})
	t.Run("all_256_first_bytes", func(t *testing.T) {
		pats := make([]string, 256)
		for i := range pats {
			pats[i] = string([]byte{byte(i), 'z'})
		}
		tr := NewTrieBuilder().AddStrings(pats).Build()
		in := []byte("azbz\x00z\xffz")
		want := naiveMatch(pats, in)
		got := tr.Match(in)
		if len(got) != len(want) {
			t.Fatalf("want %d matches, got %d", len(want), len(got))
		}
		for i, m := range got {
			if m.Pos() != want[i][0] || m.Pattern() != want[i][1] {
				t.Fatalf("match %d: got (%d,%d), want (%d,%d)", i, m.Pos(), m.Pattern(), want[i][0], want[i][1])
			}
		}
		tr.ReleaseMatches(got)
	})
}
