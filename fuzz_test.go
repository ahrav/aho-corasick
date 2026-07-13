package ahocorasick

import (
	"bytes"
	"testing"
)

// triplesFromMatches converts a Match result slice into the (start,
// pattern id, length) triples the naive reference emits, preserving
// order.
func triplesFromMatches(ms []*Match) [][3]uint32 {
	out := make([][3]uint32, len(ms))
	for i, m := range ms {
		out[i] = [3]uint32{m.Pos(), m.Pattern(), uint32(len(m.Match()))}
	}
	return out
}

// triplesFromWalk collects Walk's callback stream into the same triple
// form. Walk reports (end, length, pattern); the reference keys on start.
func (tr *Trie) triplesFromWalk(input []byte) [][3]uint32 {
	var out [][3]uint32
	tr.Walk(input, func(end, n, pattern uint32) bool {
		out = append(out, [3]uint32{end - n + 1, pattern, n})
		return true
	})
	return out
}

// diffTriples reports the first index at which got and want differ, or -1
// when they are identical.
func diffTriples(got, want [][3]uint32) int {
	if len(got) != len(want) {
		if len(got) < len(want) {
			return len(got)
		}
		return len(want)
	}
	for i := range got {
		if got[i] != want[i] {
			return i
		}
	}
	return -1
}

// Pattern-set bounds shared by patternSetFromRaw and encodeSeed. Small
// enough that Build() stays sub-millisecond so the fuzzer runs thousands
// of execs/sec and spends its budget exploring input boundaries, not
// rebuilding maximal automata; still large enough to exercise dictLink
// chains, overlapping suffixes, and both the single- and multi-stop-byte
// root layouts.
const (
	maxPatterns = 16
	maxPatLen   = 12
)

// patternSetFromRaw decodes fuzz bytes into a valid pattern set with the
// same shape the builder and naive reference both require: non-empty,
// deduplicated (a repeated pattern string collapses to one trie state
// with a single pattern id, which the naive reference cannot mirror), and
// bounded so a single fuzz iteration stays cheap. Patterns are
// length-prefixed so any byte value — including NUL and 0xff — can appear
// inside one, which the "Zeroes" and "Alphabetsize" fixtures show matters.
func patternSetFromRaw(raw []byte) []string {
	var patterns []string
	seen := make(map[string]bool)
	for i := 0; i < len(raw) && len(patterns) < maxPatterns; {
		l := int(raw[i])%maxPatLen + 1
		i++
		if i+l > len(raw) {
			l = len(raw) - i
		}
		if l <= 0 {
			break
		}
		p := string(raw[i : i+l])
		i += l
		if !seen[p] {
			seen[p] = true
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// encodeSeed is the inverse of patternSetFromRaw for seed construction: it
// emits a length-prefixed byte stream that decodes back to patterns.
// Every pattern must be 1..maxPatLen bytes: the (len-1) prefix byte
// round-trips through patternSetFromRaw's (raw[i]%maxPatLen)+1 only in
// that range, so longer seeds would silently decode into a different
// pattern stream and miss their intended coverage.
func encodeSeed(patterns ...string) []byte {
	var raw []byte
	for _, p := range patterns {
		if len(p) < 1 || len(p) > maxPatLen {
			panic("encodeSeed: seed pattern length outside 1..maxPatLen: " + p)
		}
		raw = append(raw, byte(len(p)-1)) // (len-1)%maxPatLen+1 == len for len ≤ maxPatLen
		raw = append(raw, p...)
	}
	return raw
}

// fillWithMatches builds a size-byte input of filler bytes with needle
// copied in at a fixed stride, so a seed reliably produces matches while
// leaving long filler runs that engage the root-skip fast paths. filler
// must differ from every byte that leaves the root state for the skip to
// actually skip.
func fillWithMatches(size int, filler byte, needle string) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = filler
	}
	for pos := 0; pos+len(needle) <= size; pos += 137 {
		copy(b[pos:], needle)
	}
	return b
}

// FuzzMatch is the differential core: for an arbitrary pattern set and
// input, Match, Walk, and MatchFirst must all agree with the naive
// reference (naiveMatch, in differential_test.go). The seed corpus is
// chosen to drive every reachable scan specialization in matchSeq/Match:
//
//   - >1 root stop byte  → matchTable16 / walkTable16
//   - 1 root stop byte   → matchStopByte16 / walkStopByte16
//   - 1 stop byte, large → matchDualStopByte16 + scanRange16
//   - ≥ 32 KiB, table or stop-byte-dense → matchParallel (either family)
//
// The uint32 loops (matchStopByte, matchTable, walkStopByte, walkTable)
// need >failTrans16MaxStates states and are not reachable at fuzzing
// scale; they are covered only structurally. The
// Encode→Decode round-trip is a separate target (FuzzEncodeDecode) so the
// gzip cost does not throttle exploration of the scan paths here.
func FuzzMatch(f *testing.F) {
	// >1 stop byte, small — table path (Wikipedia fixture).
	f.Add(encodeSeed("a", "ab", "bab", "bc", "bca", "c", "caa"), []byte("abccab"))
	// Patterns carrying NUL / 0xff bytes.
	f.Add(encodeSeed("\x00\x00", "\x00a"), []byte("\x00\x00a\x00\x00"))
	f.Add(encodeSeed("\xff\xff"), []byte("\xff\xff\xfe\xff\xff\xff"))
	// 1 stop byte, small — stop-byte16 / walkStopByte path.
	f.Add(encodeSeed("ab", "abc", "abca"), []byte("xxabcaxxabxx"))
	// 1 stop byte, large — dual-cursor path (≥1024, maxLen*4 < len/2).
	f.Add(encodeSeed("ab", "abc", "abca"), fillWithMatches(4096, 'x', "abca"))
	// 1 stop byte, ≥32 KiB but stop-byte-sparse (~1.5%) — pins the
	// sparse branch of the parallel dispatch keeping the scan
	// sequential below parallelSparseMin.
	f.Add(encodeSeed("ab", "abc", "abca"), fillWithMatches(40000, 'x', "abca"))
	// 1 stop byte, ≥32 KiB and stop-byte-dense (25%) — parallel path
	// over dual-cursor chunks.
	f.Add(encodeSeed("ab", "abc", "abca"), bytes.Repeat([]byte("abcaxxxx"), 5000))
	// >1 stop byte, ≥32 KiB — parallel path over table chunks.
	f.Add(encodeSeed("ab", "bc", "ca"), fillWithMatches(40000, 'z', "abca"))
	// Degenerate inputs.
	f.Add(encodeSeed("a"), []byte(""))
	f.Add(encodeSeed("aaaa"), bytes.Repeat([]byte("a"), 2048))
	// Dense matches at parallel/dual scale: an all-'a' input makes a
	// match end at *every* position, so a match necessarily lands exactly
	// on the dual midpoint and every parallel chunk boundary — the
	// boundaries the drop/rebase and lane-B emit conditions turn on.
	f.Add(encodeSeed("a", "aa", "aaa"), bytes.Repeat([]byte("a"), 40000))
	f.Add(encodeSeed("ab", "b", "bab"), bytes.Repeat([]byte("ab"), 20000))

	f.Fuzz(func(t *testing.T, raw, input []byte) {
		patterns := patternSetFromRaw(raw)
		if len(patterns) == 0 {
			return
		}
		// Bound scan cost so no single exec can stall the fuzzer: the
		// mutator readily grows input to megabytes, and the reference is
		// O(len·maxLen). 64 KiB still reaches the parallel path for
		// table and stop-byte-dense inputs (up to 8 workers over 8 KiB
		// chunks) and every dual-cursor boundary; the sparse single-stop
		// branch past parallelSparseMin is pinned by
		// TestParallelWorkersPolicy instead.
		const maxScan = 1 << 16
		if len(input) > maxScan {
			input = input[:maxScan]
		}

		trie := NewTrieBuilder().AddStrings(patterns).Build()
		want := naiveMatch(patterns, input)

		// Match must equal the reference exactly (pos, pattern id, len).
		got := trie.Match(input)
		if i := diffTriples(triplesFromMatches(got), want); i != -1 {
			t.Fatalf("Match mismatch at %d\npatterns=%q\ninput=%q\ngot =%v\nwant=%v",
				i, patterns, input, triplesFromMatches(got), want)
		}
		trie.ReleaseMatches(got)

		// Walk (walkStopByte / walkTable) must produce the same stream.
		if i := diffTriples(trie.triplesFromWalk(input), want); i != -1 {
			t.Fatalf("Walk mismatch at %d\npatterns=%q\ninput=%q\ngot =%v\nwant=%v",
				i, patterns, input, trie.triplesFromWalk(input), want)
		}

		// MatchFirst must return the reference's first triple, or nil.
		first := trie.MatchFirst(input)
		switch {
		case len(want) == 0:
			if first != nil {
				t.Fatalf("MatchFirst returned %v, want nil\npatterns=%q\ninput=%q", first, patterns, input)
			}
		case first == nil:
			t.Fatalf("MatchFirst returned nil, want %v\npatterns=%q\ninput=%q", want[0], patterns, input)
		default:
			got0 := [3]uint32{first.Pos(), first.Pattern(), uint32(len(first.Match()))}
			if got0 != want[0] {
				t.Fatalf("MatchFirst = %v, want %v\npatterns=%q\ninput=%q", got0, want[0], patterns, input)
			}
		}
	})
}

// FuzzEncodeDecode checks that a Trie survives the gzip binary round-trip:
// Decode(Encode(t)) must produce an automaton that matches the naive
// reference, since Decode re-derives dictPat, rootStop, failTrans16, and
// stopEntry16 rather than storing them — a bug there escapes the in-memory
// scan paths FuzzMatch covers. Kept separate so the gzip cost does not
// slow scan-path fuzzing; input is bounded small since round-trip
// correctness depends on the automaton, not the input length.
func FuzzEncodeDecode(f *testing.F) {
	f.Add(encodeSeed("or", "amet"), []byte("Lorem ipsum dolor sit amet"))
	f.Add(encodeSeed("a", "ab", "bab", "bc", "bca", "c", "caa"), []byte("abccab"))
	f.Add(encodeSeed("ab", "abc", "abca"), []byte("xxabcaxxabxx"))
	f.Add(encodeSeed("\x00\x00", "\x00a"), []byte("\x00\x00a\x00\x00"))

	f.Fuzz(func(t *testing.T, raw, input []byte) {
		patterns := patternSetFromRaw(raw)
		if len(patterns) == 0 {
			return
		}
		if len(input) > 4096 {
			input = input[:4096]
		}

		trie := NewTrieBuilder().AddStrings(patterns).Build()
		var enc bytes.Buffer
		if err := Encode(&enc, trie); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		decoded, err := Decode(&enc)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}

		want := naiveMatch(patterns, input)
		got := decoded.Match(input)
		if i := diffTriples(triplesFromMatches(got), want); i != -1 {
			t.Fatalf("decoded Match mismatch at %d\npatterns=%q\ninput=%q\ngot =%v\nwant=%v",
				i, patterns, input, triplesFromMatches(got), want)
		}
		decoded.ReleaseMatches(got)
	})
}
