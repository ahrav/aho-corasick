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
	// The dominant allocation is failTrans: one [256]uint32 row (1 KiB) per
	// state, read straight into place below (no transient flat copy), so the
	// state count is also the peak allocation. Bound it by an explicit byte
	// budget so the guard matches the real allocation size and a
	// corrupt-but-consistent length cannot OOM the process before any
	// transition data is read. The budget is far above any realistic automaton
	// (the full NSF word list in test_data is ~1.2M states ≈ 1.2 GiB of rows),
	// so it never rejects a real trie.
	const (
		failTransRowBytes = 256 * 4 // one [256]uint32 transition row
		maxDecodeBytes    = 4 << 30 // 4 GiB budget for failTrans rows
		maxDecodeStates   = maxDecodeBytes / failTransRowBytes
	)
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

	// Read failTrans one row at a time straight into place. Reading the whole
	// table into a temporary flat slice first would double the peak allocation
	// (another maxDecodeBytes), so decode row by row instead.
	failTrans := make([][256]uint32, failTransLen)
	for i := range failTrans {
		if err := binary.Read(r, binary.LittleEndian, failTrans[i][:]); err != nil {
			return nil, err
		}
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
	trie.buildRootSkip()
	return trie, nil
}
