package ahocorasick

import (
	"compress/gzip"
	"encoding/binary"
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
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.trans))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.failTrans))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint64(len(trie.failLink))); err != nil {
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
	if err := binary.Write(w, binary.LittleEndian, trie.trans); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, trie.failTrans); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, trie.failLink); err != nil {
		return err
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

	var dictLen, transLen, failTransLen, failLinkLen, dictLinkLen, patternLen uint64

	// Read the lengths of all arrays
	if err := binary.Read(r, binary.LittleEndian, &dictLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &transLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &failTransLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &failLinkLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &dictLinkLen); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &patternLen); err != nil {
		return nil, err
	}

	// Allocate memory and read the actual data
	dict := make([]int64, dictLen)
	if err := binary.Read(r, binary.LittleEndian, dict); err != nil {
		return nil, err
	}

	trans := make([][256]int64, transLen)
	if err := binary.Read(r, binary.LittleEndian, trans); err != nil {
		return nil, err
	}

	failTrans := make([][256]int64, failTransLen)
	if err := binary.Read(r, binary.LittleEndian, failTrans); err != nil {
		return nil, err
	}

	failLink := make([]int64, failLinkLen)
	if err := binary.Read(r, binary.LittleEndian, failLink); err != nil {
		return nil, err
	}

	dictLink := make([]int64, dictLinkLen)
	if err := binary.Read(r, binary.LittleEndian, dictLink); err != nil {
		return nil, err
	}

	pattern := make([]int64, patternLen)
	if err := binary.Read(r, binary.LittleEndian, pattern); err != nil {
		return nil, err
	}

	return &Trie{
		trans:     trans,
		failTrans: failTrans,
		failLink:  failLink,
		dictLink:  dictLink,
		dict:      dict,
		pattern:   pattern,
	}, nil
}
