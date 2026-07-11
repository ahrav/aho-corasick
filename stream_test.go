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
