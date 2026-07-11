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
