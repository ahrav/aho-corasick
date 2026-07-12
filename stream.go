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

// DecodeMaxStates is the default upper bound Decode places on the number of
// automaton states it will accept from a stream. A decoded trie's memory is
// dominated by failTrans at one [256]uint32 row (1 KiB) per state, so this is
// effectively a ~4 GiB ceiling. It sits far above any realistic automaton (the
// full NSF word list in test_data is ~1.2M states ≈ 1.2 GiB), so Decode never
// rejects a real trie. Callers deserializing untrusted input on a
// memory-constrained process should call DecodeWithMaxStates with a lower bound.
const DecodeMaxStates = (4 << 30) / (256 * 4) // 4 GiB of failTrans rows

// Decode reads a Trie in gzip compressed binary format from r, accepting up to
// DecodeMaxStates states.
func Decode(r io.Reader) (*Trie, error) {
	return DecodeWithMaxStates(r, DecodeMaxStates)
}

// DecodeWithMaxStates is Decode with a caller-supplied ceiling on the number of
// automaton states. The bound caps the memory a corrupt or hostile stream can
// make Decode allocate — failTrans costs one [256]uint32 row (1 KiB) per state
// — so choose a value the process can afford. A non-positive maxStates falls
// back to DecodeMaxStates.
func DecodeWithMaxStates(r io.Reader, maxStates int) (*Trie, error) {
	dec := newDecoder(r)
	return dec.decode(maxStates)
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

	// Flatten and write failTrans. In-memory entries carry outputFlag bits
	// (see addOutputFlags); mask them off so the serialized format stays
	// plain state ids, compatible with readers that predate the flags.
	// Decode re-derives the flags.
	var row [256]uint32
	for _, arr := range trie.failTrans {
		for i, v := range arr {
			row[i] = v & stateMask
		}
		if err := binary.Write(w, binary.LittleEndian, row[:]); err != nil {
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

func (dec *decoder) decode(maxStates int) (*Trie, error) {
	if maxStates <= 0 {
		maxStates = DecodeMaxStates
	}

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
	// maxStates caps the memory a corrupt or hostile stream can make Decode
	// allocate: failTrans dominates at one [256]uint32 row (1 KiB) per state.
	// failTrans is also grown incrementally as rows are read (below), so a
	// stream that declares a huge count but carries little data cannot force a
	// large up-front allocation — the reservation tracks the bytes actually
	// delivered, bounded by maxStates.
	if failTransLen < 2 || dictLen != failTransLen || dictLinkLen != failTransLen || patternLen != failTransLen {
		return nil, fmt.Errorf("ahocorasick: corrupt trie: inconsistent table lengths (dict=%d failTrans=%d dictLink=%d pattern=%d)", dictLen, failTransLen, dictLinkLen, patternLen)
	}
	// Packed transitions reserve the high bit for outputFlag (see trie.go),
	// so state ids must fit in stateMask regardless of the caller's memory
	// budget. The default DecodeMaxStates sits far below this ceiling; the
	// check matters only for callers passing a larger custom limit.
	if failTransLen > uint64(maxStates) || failTransLen > uint64(stateMask)+1 {
		return nil, fmt.Errorf("ahocorasick: corrupt trie: %d states exceeds decode limit %d", failTransLen, maxStates)
	}

	// Allocate memory and read the actual data
	dict := make([]uint32, dictLen)
	if err := binary.Read(r, binary.LittleEndian, dict); err != nil {
		return nil, err
	}

	// Grow failTrans as rows are read rather than allocating the declared count
	// up front, so a stream that declares many states but carries few rows only
	// reserves memory proportional to the data actually delivered (it then hits
	// EOF and errors) instead of forcing a full up-front allocation.
	const initialFailTransCap = 1024 // ~1 MiB; append grows it as rows arrive
	initCap := failTransLen
	if initCap > initialFailTransCap {
		initCap = initialFailTransCap
	}
	failTrans := make([][256]uint32, 0, initCap)
	for i := uint64(0); i < failTransLen; i++ {
		failTrans = append(failTrans, [256]uint32{})
		if err := binary.Read(r, binary.LittleEndian, failTrans[i][:]); err != nil {
			return nil, err
		}
		// Transition targets come from an untrusted stream and are used as
		// indexes by addOutputFlags and the scan loops. Entries must be
		// plain state ids: in range and without flag bits (Encode strips
		// them).
		for _, v := range failTrans[i] {
			if uint64(v) >= failTransLen {
				return nil, fmt.Errorf("ahocorasick: corrupt trie: state %d transition targets state %d, want < %d states", i, v, failTransLen)
			}
		}
	}

	dictLink := make([]uint32, dictLinkLen)
	if err := binary.Read(r, binary.LittleEndian, dictLink); err != nil {
		return nil, err
	}
	// dictLink entries are chased and indexed during matching; bound them
	// the same way.
	for i, v := range dictLink {
		if uint64(v) >= failTransLen {
			return nil, fmt.Errorf("ahocorasick: corrupt trie: dictLink %d targets state %d, want < %d states", i, v, failTransLen)
		}
	}
	// A dictLink cycle (e.g. 5 -> 7 -> 5) passes the bounds check but
	// would hang the emit loops, which chase chains until nilState.
	// Verify every chain terminates; memoizing resolved states keeps the
	// pass O(states). A terminating chain visits distinct unresolved
	// states, so a path as long as the table proves a repeat.
	resolved := make([]bool, dictLinkLen)
	resolved[nilState] = true
	path := make([]uint32, 0, 64)
	for s := range dictLink {
		path = path[:0]
		for u := uint32(s); !resolved[u]; u = dictLink[u] {
			if len(path) == len(dictLink) {
				return nil, fmt.Errorf("ahocorasick: corrupt trie: dictLink chain from state %d cycles", s)
			}
			path = append(path, u)
		}
		for _, p := range path {
			resolved[p] = true
		}
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
	// Rebuild the derived acceleration tables (dictPat, failTrans16, root
	// skip); they are recomputed on decode, not stored in the wire format.
	trie.addOutputFlags()
	trie.buildRootSkip()
	trie.buildFailTrans16()
	trie.buildTransC()
	trie.setStopEntry()
	return trie, nil
}
