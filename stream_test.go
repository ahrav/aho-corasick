package ahocorasick

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

func testTrie(trie *Trie) error {
	matches := trie.MatchString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.")
	expected := []*Match{
		newMatchString(1, 0, "or"),
		newMatchString(15, 0, "or"),
		newMatchString(22, 1, "amet"),
	}

	if len(expected) != len(matches) {
		return fmt.Errorf("expected %d matches, got %d\n", len(expected), len(matches))
	}

	for i := range matches {
		if !MatchEqual(expected[i], matches[i]) {
			return fmt.Errorf("expected %v, got %v\n", expected[i], matches[i])
		}
	}

	return nil
}

func TestEncodingAndDecoding(t *testing.T) {
	trie := NewTrieBuilder().AddStrings([]string{"or", "amet"}).Build()

	if err := testTrie(trie); err != nil {
		t.Error(err)
	}

	var buf bytes.Buffer

	if err := Encode(&buf, trie); err != nil {
		t.Error(err)
	}

	decodedTrie, err := Decode(&buf)
	if err != nil {
		t.Error(err)
	}

	if err := testTrie(decodedTrie); err != nil {
		t.Error(err)
	}
}

func TestReadAndWriteTrie(t *testing.T) {
	patterns, err := readPatterns("test_data/NSF-ordlisten.cleaned.uniq.txt")
	if err != nil {
		t.Fatal(err)
	}

	trie := NewTrieBuilder().AddStrings(patterns[:10000]).Build()

	f, err := os.Create("test.trie")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove("test.trie")

	if err := Encode(f, trie); err != nil {
		t.Fatal(err)
	}

	f.Seek(0, 0)

	decodedTrie, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}

	matches := decodedTrie.MatchString("abasien")

	if len(matches) != 3 {
		t.Errorf("expected 3 matches, got %d", len(matches))
	}
}

// encodeRaw writes a trie stream from raw tables, bypassing Encode's
// flag masking, so tests can construct corrupt payloads.
func encodeRaw(t *testing.T, dict []uint32, failTrans [][256]uint32, dictLink, pattern []uint32) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	lens := []uint64{uint64(len(dict)), uint64(len(failTrans)), uint64(len(dictLink)), uint64(len(pattern))}
	for _, n := range lens {
		if err := binary.Write(w, binary.LittleEndian, n); err != nil {
			t.Fatal(err)
		}
	}
	if err := binary.Write(w, binary.LittleEndian, dict); err != nil {
		t.Fatal(err)
	}
	for _, arr := range failTrans {
		if err := binary.Write(w, binary.LittleEndian, arr[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := binary.Write(w, binary.LittleEndian, dictLink); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(w, binary.LittleEndian, pattern); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// gzipTrieHeader builds a gzip stream with the four length prefixes Decode
// reads first, optionally followed by raw payload, for exercising Decode's
// untrusted-input guards without a full valid trie.
func gzipTrieHeader(t *testing.T, dictLen, failTransLen, dictLinkLen, patternLen uint64, payload []byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, n := range []uint64{dictLen, failTransLen, dictLinkLen, patternLen} {
		if err := binary.Write(w, binary.LittleEndian, n); err != nil {
			t.Fatal(err)
		}
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// TestEncodeStripsOutputFlags verifies the serialized format stays plain
// state ids: the in-memory outputFlag bits must never reach the stream.
func TestEncodeStripsOutputFlags(t *testing.T) {
	trie := NewTrieBuilder().AddStrings([]string{"or", "amet"}).Build()

	var buf bytes.Buffer
	if err := Encode(&buf, trie); err != nil {
		t.Fatal(err)
	}

	r, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var lens [4]uint64
	for i := range lens {
		if err := binary.Read(r, binary.LittleEndian, &lens[i]); err != nil {
			t.Fatal(err)
		}
	}
	dict := make([]uint32, lens[0])
	if err := binary.Read(r, binary.LittleEndian, dict); err != nil {
		t.Fatal(err)
	}
	flat := make([]uint32, lens[1]*256)
	if err := binary.Read(r, binary.LittleEndian, flat); err != nil {
		t.Fatal(err)
	}
	for i, v := range flat {
		if v&outputFlag != 0 {
			t.Fatalf("serialized transition %d carries outputFlag: %#x", i, v)
		}
	}
}

func TestDecodeRejectsOutOfRangeTransition(t *testing.T) {
	failTrans := make([][256]uint32, 2)
	for b := range 256 {
		failTrans[1][b] = rootState
	}
	failTrans[1][0] = 2 // out of range: valid states are 0 and 1

	_, err := Decode(encodeRaw(t, make([]uint32, 2), failTrans, make([]uint32, 2), make([]uint32, 2)))
	if err == nil {
		t.Fatal("expected error for out-of-range transition target")
	}
}

func TestDecodeRejectsFlaggedTransition(t *testing.T) {
	failTrans := make([][256]uint32, 2)
	for b := range 256 {
		failTrans[1][b] = rootState
	}
	failTrans[1][0] = outputFlag | rootState // stray flag bit

	_, err := Decode(encodeRaw(t, make([]uint32, 2), failTrans, make([]uint32, 2), make([]uint32, 2)))
	if err == nil {
		t.Fatal("expected error for transition carrying a flag bit")
	}
}

func TestDecodeRejectsOutOfRangeDictLink(t *testing.T) {
	failTrans := make([][256]uint32, 2)
	for b := range 256 {
		failTrans[1][b] = rootState
	}

	_, err := Decode(encodeRaw(t, make([]uint32, 2), failTrans, []uint32{0, 7}, make([]uint32, 2)))
	if err == nil {
		t.Fatal("expected error for out-of-range dictLink target")
	}
}

func TestDecodeRejectsCyclicDictLink(t *testing.T) {
	for name, dictLink := range map[string][]uint32{
		"self-loop": {0, 0, 2, 0},
		"two-cycle": {0, 0, 3, 2},
	} {
		failTrans := make([][256]uint32, 4)
		for s := range failTrans {
			for b := range 256 {
				failTrans[s][b] = rootState
			}
		}

		_, err := Decode(encodeRaw(t, make([]uint32, 4), failTrans, dictLink, make([]uint32, 4)))
		if err == nil {
			t.Fatalf("%s: expected error for cyclic dictLink chain", name)
		}
	}
}

// TestDecodeRejectsOversizedStateCount checks that a stream declaring more
// states than the limit is rejected up front, before any table allocation.
func TestDecodeRejectsOversizedStateCount(t *testing.T) {
	huge := uint64(DecodeMaxStates) + 1
	buf := gzipTrieHeader(t, huge, huge, huge, huge, nil)
	if _, err := Decode(buf); err == nil {
		t.Fatal("expected error for state count above DecodeMaxStates, got nil")
	}
}

// TestDecodeWithMaxStatesEnforcesCallerLimit checks the caller-supplied bound:
// a real trie is rejected under a tight limit but decodes under the default.
func TestDecodeWithMaxStatesEnforcesCallerLimit(t *testing.T) {
	trie := NewTrieBuilder().AddStrings([]string{"or", "amet"}).Build()

	var tight bytes.Buffer
	if err := Encode(&tight, trie); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWithMaxStates(&tight, 1); err == nil {
		t.Fatal("expected error when trie exceeds caller maxStates=1, got nil")
	}

	var ok bytes.Buffer
	if err := Encode(&ok, trie); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWithMaxStates(&ok, DecodeMaxStates); err != nil {
		t.Fatalf("expected default-limit decode to succeed, got %v", err)
	}
}

// TestDecodeTruncatedBeforeFailTrans exercises the incremental failTrans path:
// a stream that declares many states (within the limit) but is truncated after
// the dict rows must return an error, never panic, without reserving the full
// declared failTrans up front.
func TestDecodeTruncatedBeforeFailTrans(t *testing.T) {
	const n = 100000                 // within DecodeMaxStates
	dictPayload := make([]byte, n*4) // n zero uint32s for dict; failTrans absent
	buf := gzipTrieHeader(t, n, n, n, n, dictPayload)
	if _, err := Decode(buf); err == nil {
		t.Fatal("expected error for stream truncated before failTrans, got nil")
	}
}

// TestEncodeDeterministic verifies that building the same pattern set twice
// yields byte-identical serialized tries. BFS renumbering in Build visits
// children in byte order rather than Go map order, so state ids — and thus
// the encoded dict/dictLink/failTrans arrays — must not vary between runs.
// Reproducible artifacts matter for checksums and cache keys.
func TestEncodeDeterministic(t *testing.T) {
	// Enough branching (shared prefixes, many distinct first bytes) that
	// randomized map iteration would almost surely produce different ids.
	patterns := []string{
		"or", "orb", "orbit", "amet", "ambit", "gravel", "grave",
		"zebra", "zeal", "quark", "quartz", "night", "nickel",
		"he", "she", "his", "hers", "hi", "them", "then", "there",
	}

	var first bytes.Buffer
	if err := Encode(&first, NewTrieBuilder().AddStrings(patterns).Build()); err != nil {
		t.Fatal(err)
	}
	for run := 1; run < 5; run++ {
		var again bytes.Buffer
		if err := Encode(&again, NewTrieBuilder().AddStrings(patterns).Build()); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), again.Bytes()) {
			t.Fatalf("run %d: encoded trie differs from first build of the same patterns", run)
		}
	}
}
