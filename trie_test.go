package ahocorasick

import (
	"bufio"
	"bytes"
	"io/ioutil"
	"os"
	"strings"
	"testing"
)

func TestReadme(t *testing.T) {
	trie := NewTrieBuilder().AddStrings([]string{"or", "amet"}).Build()
	matches := trie.MatchString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.")
	expected := []*Match{
		newMatchString(1, 0, "or"),
		newMatchString(15, 0, "or"),
		newMatchString(22, 1, "amet"),
	}

	if len(expected) != len(matches) {
		t.Errorf("expected %d matches, got %d\n", len(expected), len(matches))
	}

	for i := range matches {
		if !MatchEqual(expected[i], matches[i]) {
			t.Errorf("expected %v, got %v\n", expected[i], matches[i])
		}
	}
}

func TestTrie(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		input    string
		expected []*Match
	}{
		{
			"Wikipedia",
			[]string{"a", "ab", "bab", "bc", "bca", "c", "caa"},
			"abccab",
			[]*Match{
				newMatchString(0, 0, "a"),
				newMatchString(0, 1, "ab"),
				newMatchString(1, 3, "bc"),
				newMatchString(2, 5, "c"),
				newMatchString(3, 5, "c"),
				newMatchString(4, 0, "a"),
				newMatchString(4, 1, "ab"),
			},
		},
		{
			"Prefix",
			[]string{"Aho-Corasick", "Aho-Cora", "Aho", "A"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(0, 3, "A"),
				newMatchString(0, 2, "Aho"),
				newMatchString(0, 1, "Aho-Cora"),
				newMatchString(0, 0, "Aho-Corasick"),
			},
		},
		{
			"Suffix",
			[]string{"Aho-Corasick", "Corasick", "sick", "k"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(0, 0, "Aho-Corasick"),
				newMatchString(4, 1, "Corasick"),
				newMatchString(8, 2, "sick"),
				newMatchString(11, 3, "k"),
			},
		},
		{
			"Infix",
			[]string{"Aho-Corasick", "ho-Corasi", "o-Co", "-"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(3, 3, "-"),
				newMatchString(2, 2, "o-Co"),
				newMatchString(1, 1, "ho-Corasi"),
				newMatchString(0, 0, "Aho-Corasick"),
			},
		},
		{
			"Overlap",
			[]string{"Aho-Co", "ho-Cora", "o-Coras", "-Corasick"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(0, 0, "Aho-Co"),
				newMatchString(1, 1, "ho-Cora"),
				newMatchString(2, 2, "o-Coras"),
				newMatchString(3, 3, "-Corasick"),
			},
		},
		{
			"Adjacent",
			[]string{"Ah", "o-Co", "ras", "ick"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(0, 0, "Ah"),
				newMatchString(2, 1, "o-Co"),
				newMatchString(6, 2, "ras"),
				newMatchString(9, 3, "ick"),
			},
		},
		{
			"SingleSymbol",
			[]string{"o"},
			"Aho-Corasick",
			[]*Match{
				newMatchString(2, 0, "o"),
				newMatchString(5, 0, "o"),
			},
		},
		{
			"NoMatch",
			[]string{"Gazorpazopfield", "Knuth", "O"},
			"Aho-Corasick",
			[]*Match{},
		},
		{
			"Zeroes",
			[]string{"\x00\x00"},
			"\x00\x00Aho\x00\x00-\x00\x00Corasick\x00\x00",
			[]*Match{
				newMatchString(0, 0, "\x00\x00"),
				newMatchString(5, 0, "\x00\x00"),
				newMatchString(8, 0, "\x00\x00"),
				newMatchString(18, 0, "\x00\x00"),
			},
		},
		{
			"Alphabetsize",
			[]string{"\xff\xff"},
			"\xff\xffAho\xfe\xfe-\xff\xffCorasick\xff\xff\xff",
			[]*Match{
				newMatchString(0, 0, "\xff\xff"),
				newMatchString(8, 0, "\xff\xff"),
				newMatchString(18, 0, "\xff\xff"),
				newMatchString(19, 0, "\xff\xff"),
			},
		},
	}

	for _, c := range cases {
		tr := NewTrieBuilder().AddStrings(c.patterns).Build()
		matches := tr.MatchString(c.input)

		if len(matches) != len(c.expected) {
			t.Errorf("%s: expected %d matches, got %d", c.name, len(c.expected), len(matches))
			continue
		}

		for i := range matches {
			if !MatchEqual(matches[i], c.expected[i]) {
				t.Errorf("%s: expected %v, got %v", c.name, c.expected[i], matches[i])
			}
		}
	}
}

func TestMatchFirst(t *testing.T) {
	ibsen, err := ioutil.ReadFile("./test_data/Ibsen.txt")
	if err != nil {
		t.Error(err)
	}
	tr := NewTrieBuilder().AddString("Hedvig").Build()
	match := tr.MatchFirst(ibsen)
	expected := newMatchString(937, 0, "Hedvig")
	if !MatchEqual(expected, match) {
		t.Errorf("expected %v, got %v\n", expected, match)
	}
}

func TestHedvig(t *testing.T) {
	ibsen, err := ioutil.ReadFile("./test_data/Ibsen.txt")
	if err != nil {
		t.Error(err)
	}
	matches := NewTrieBuilder().AddString("Hedvig").Build().Match(ibsen)
	if len(matches) != 134 {
		t.Errorf("expected to find 134 Hedvig's, got %d\n", len(matches))
	}
}

func BenchmarkTrieBuild(b *testing.B) {
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		b.Error(err)
	}

	b.Run("100", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			NewTrieBuilder().AddStrings(patterns[:100]).Build()
		}
	})
	b.Run("1000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			NewTrieBuilder().AddStrings(patterns[:1000]).Build()
		}
	})
	b.Run("10000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			NewTrieBuilder().AddStrings(patterns[:10000]).Build()
		}
	})
	b.Run("100000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			NewTrieBuilder().AddStrings(patterns[:100000]).Build()
		}
	})
}

func BenchmarkMatchIbsen(b *testing.B) {
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		b.Error(err)
	}

	ibsen, err := ioutil.ReadFile("./test_data/Ibsen.txt")
	if err != nil {
		b.Error(err)
	}

	trie := NewTrieBuilder().AddStrings(patterns[:10000]).Build()

	b.Run("100", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			matches := trie.Match(ibsen[:100])
			trie.ReleaseMatches(matches)
		}
	})
	b.Run("1000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			matches := trie.Match(ibsen[:1000])
			trie.ReleaseMatches(matches)
		}
	})
	b.Run("10000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			matches := trie.Match(ibsen[:10000])
			trie.ReleaseMatches(matches)
		}
	})
	b.Run("100000", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			matches := trie.Match(ibsen[:100000])
			trie.ReleaseMatches(matches)
		}
	})
}

func readPatterns(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	patterns := make([]string, 0)

	for s.Scan() {
		patterns = append(patterns, strings.TrimSpace(s.Text()))
	}

	if err := s.Err(); err != nil {
		return nil, err
	}

	return patterns, nil
}

// TestFailTrans16Gate verifies the half-width table is built exactly when a
// 16-bit scan path can use it: any trie whose state ids fit in 15 bits. Both
// the single-stop-byte loops (matchStopByte16) and the multi-stop table loops
// (matchTable16) read it, so it is never dead weight on a small trie.
func TestFailTrans16Gate(t *testing.T) {
	// All patterns share the initial byte 'a': one root stop byte.
	single := NewTrieBuilder().AddStrings([]string{"amet", "ambit", "arc"}).Build()
	if single.failTrans16 == nil {
		t.Error("single stop byte, small trie: expected failTrans16 to be built")
	}
	if got := single.MatchString("xx amet yy"); len(got) != 1 {
		t.Errorf("single stop byte: expected 1 match, got %d", len(got))
	}

	// Distinct initial bytes: several root stop bytes; the 16-bit table
	// path (matchTable16 / walkTable16) uses the half-width table too.
	multi := NewTrieBuilder().AddStrings([]string{"or", "amet", "zebra"}).Build()
	if multi.failTrans16 == nil {
		t.Error("multiple stop bytes, small trie: expected failTrans16 to be built")
	}
	if got := multi.MatchString("zebra or amet"); len(got) != 3 {
		t.Errorf("multiple stop bytes: expected 3 matches, got %d", len(got))
	}
}

// buildBigSingleStopTrie builds a single-stop-byte trie with more than
// 2^15 states, so failTrans16 stays nil and the sequential scan runs the
// 32-bit matchStopByte loop. Every pattern starts with 'a' and continues
// over a 32-byte suffix alphabet that excludes 'a', giving
// 1 + 1 + 32 + 32^2 + 32^3 = 33826 states.
func buildBigSingleStopTrie(t *testing.T) *Trie {
	t.Helper()
	patterns := make([]string, 0, 32*32*32)
	for i := range 32 {
		for j := range 32 {
			for k := range 32 {
				patterns = append(patterns,
					string([]byte{'a', byte('A' + i), byte('A' + j), byte('A' + k)}))
			}
		}
	}
	tr := NewTrieBuilder().AddStrings(patterns).Build()
	if tr.failTrans16 != nil {
		t.Fatal("expected >2^15 states to leave failTrans16 nil")
	}
	if len(tr.rootStopBytes) != 1 {
		t.Fatalf("expected a single root stop byte, got %d", len(tr.rootStopBytes))
	}
	return tr
}

// TestParallelWorkersPolicy pins the Match dispatch policy: the worker
// count and size floors that route input to matchParallel, the sparse
// single-stop-byte inputs that stay sequential up to parallelSparseMin
// on both scan widths, and the single-CPU case that must never
// parallelize regardless of density.
func TestParallelWorkersPolicy(t *testing.T) {
	single := NewTrieBuilder().AddStrings([]string{"ab", "abc", "abca"}).Build()
	if single.failTrans16 == nil || len(single.rootStopBytes) != 1 {
		t.Fatal("expected a 16-bit single-stop-byte trie")
	}
	multi := NewTrieBuilder().AddStrings([]string{"ab", "bc", "ca"}).Build()
	if len(multi.rootStopBytes) != 0 {
		t.Fatal("expected a table-path trie (no root stop byte set)")
	}
	big := buildBigSingleStopTrie(t)

	// A table-path trie with a 9000-byte pattern: overlap*4 is 35996,
	// so at 256KiB the overlap shrink caps the workers at
	// 262144/35996 = 7 where the plain caps would allow 8.
	long := NewTrieBuilder().AddStrings([]string{
		strings.Repeat("x", 9000), "ab", "cd",
	}).Build()
	if len(long.rootStopBytes) == 1 {
		t.Fatal("expected the long-pattern trie to skip the density gate")
	}
	if long.maxLen != 9000 {
		t.Fatalf("expected maxLen 9000, got %d", long.maxLen)
	}

	// Stop byte 'a' at 1/16 bytes (~6%) sits under the ~10% density
	// threshold; at 1/8 (~12.5%) it sits over.
	sparse := func(size int) []byte {
		return bytes.Repeat([]byte("axxxxxxxxxxxxxxx"), size/16)
	}
	dense := func(size int) []byte {
		return bytes.Repeat([]byte("abcaxxxx"), size/8)
	}

	cases := []struct {
		name     string
		tr       *Trie
		input    []byte
		maxProcs int
		want     int
	}{
		{"below parallelMin stays sequential", single, dense(16 << 10), 8, 0},
		{"single CPU stays sequential", single, dense(40 << 10), 1, 0},
		{"sparse below parallelSparseMin stays sequential", single, sparse(64 << 10), 8, 0},
		{"sparse past parallelSparseMin parallelizes", single, sparse(160 << 10), 8, 8},
		{"dense parallelizes from parallelMin", single, dense(40 << 10), 8, 5},
		{"table path parallelizes from parallelMin", multi, sparse(40 << 10), 8, 5},
		{"workers capped at 8", single, dense(160 << 10), 16, 8},
		{"workers capped by GOMAXPROCS", single, dense(40 << 10), 2, 2},
		{"256KiB holds the base cap", single, dense(256 << 10), 64, 8},
		{"extended cap ramps at parallelChunkWide per worker", single, dense(512 << 10), 64, 16},
		{"extended cap reaches 32 at 1MiB", single, dense(1 << 20), 64, 32},
		{"extended cap stays below 32 just under 1MiB", single, dense((1 << 20) - 8), 64, 31},
		{"extended cap saturates at 32", single, dense(2 << 20), 64, 32},
		{"extended cap bounded by GOMAXPROCS", single, dense(2 << 20), 12, 12},
		{"long patterns shrink the cap to clear the overlap guard", long, sparse(256 << 10), 64, 7},
		{"overlap shrink below 2 workers stays sequential", long, sparse(64 << 10), 64, 0},
		{"32-bit sparse below parallelSparseMin stays sequential", big, sparse(64 << 10), 8, 0},
		{"32-bit sparse past parallelSparseMin parallelizes", big, sparse(160 << 10), 8, 8},
		{"32-bit dense parallelizes from parallelMin", big, dense(40 << 10), 8, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.parallelWorkers(tc.input, tc.maxProcs); got != tc.want {
				t.Errorf("parallelWorkers(len=%d, maxProcs=%d) = %d, want %d",
					len(tc.input), tc.maxProcs, got, tc.want)
			}
		})
	}
}
