package ahocorasick

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
)

// Encode writes a Trie to w in gzip compressed binary format.
func Encode(w io.Writer, trie *Trie) error {
	enc := newEncoder(w)
	return enc.encode(trie)
}

// Decode reads a Trie in gzip compressed binary format from r.
func Decode(r io.Reader) (*Trie, error) {
	dec := newDecoder(r)
	return dec.decode()
}

type encoder struct {
	w io.Writer
}

func newEncoder(w io.Writer) *encoder {
	return &encoder{
		w,
	}
}

func (enc *encoder) encode(trie *Trie) error {
	w := gzip.NewWriter(enc.w)
	defer w.Close()

	// Write the lengths of all arrays first
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.dict))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.failTrans))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.dictLink))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.pattern))); err != nil {
		return err
	}

	// Write the actual data
	if err := binary.Write(w, binary.LittleEndian, trie.dict); err != nil {
		return err
	}

	// Flatten and write failTrans
	for _, arr := range trie.failTrans {
		if err := binary.Write(w, binary.LittleEndian, arr[:]); err != nil {
			return err
		}
	}

	if err := binary.Write(w, binary.LittleEndian, trie.dictLink); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, trie.pattern); err != nil {
		return err
	}

	return nil
}

type decoder struct {
	r io.Reader
}

func newDecoder(r io.Reader) *decoder {
	return &decoder{
		r,
	}
}

func (dec *decoder) decode() (*Trie, error) {
	r, err := gzip.NewReader(dec.r)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var dictLen, failTransLen, dictLinkLen, patternLen uint64

	// Read the lengths of all arrays
	if err := binary.Read(r, binary.LittleEndian, &dictLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &failTransLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &dictLinkLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &patternLen); err != nil {
		return nil, err
	}

	// Decode operates on untrusted input. A well-formed trie has one row per
	// state across all four arrays and at least the unused state 0 plus the
	// root, so buildRootSkip can index failTrans[rootState]. Reject anything
	// else with an error rather than panicking on a truncated or corrupt stream.
	//
	// maxDecodeStates also bounds the up-front allocation: failTrans costs 1KB
	// per state, so an internally consistent but huge count would otherwise
	// reach make(...) and panic on slice-allocation limits before any data is
	// read. The bound is far above any realistic automaton (the full NSF word
	// list in test_data is ~1.2M states), so it never rejects a real trie.
	const maxDecodeStates = 1 << 24
	if failTransLen < 2 || dictLen != failTransLen || dictLinkLen != failTransLen || patternLen != failTransLen {
		return nil, fmt.Errorf("ahocorasick: corrupt trie: inconsistent table lengths (dict=%d failTrans=%d dictLink=%d pattern=%d)", dictLen, failTransLen, dictLinkLen, patternLen)
	}
	if failTransLen > maxDecodeStates {
		return nil, fmt.Errorf("ahocorasick: corrupt trie: %d states exceeds decode limit %d", failTransLen, maxDecodeStates)
	}

	// Allocate memory and read the actual data
	dict := make([]uint32, dictLen)
	if err := binary.Read(r, binary.LittleEndian, dict); err != nil {
		return nil, err
	}

	// Read and reshape failTrans
	failTrans := make([][256]uint32, failTransLen)
	flatFailTrans := make([]uint32, failTransLen*256)
	if err := binary.Read(r, binary.LittleEndian, flatFailTrans); err != nil {
		return nil, err
	}
	for i := range failTrans {
		copy(failTrans[i][:], flatFailTrans[i*256:(i+1)*256])
	}

	dictLink := make([]uint32, dictLinkLen)
	if err := binary.Read(r, binary.LittleEndian, dictLink); err != nil {
		return nil, err
	}

	pattern := make([]uint32, patternLen)
	if err := binary.Read(r, binary.LittleEndian, pattern); err != nil {
		return nil, err
	}

	trie := &Trie{
		failTrans: failTrans,
		dictLink:  dictLink,
		dict:      dict,
		pattern:   pattern,
		bufPool:   newBufPool(),
	}
	trie.addOutputFlags()
	trie.buildRootSkip()
	return trie, nil
}
