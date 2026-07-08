package ahocorasick

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"sync"
)

const (
	rootState uint32 = 1
	nilState  uint32 = 0
)

// Trie represents a trie of patterns with extra links as per the Aho-Corasick algorithm.
type Trie struct {
	failTrans [][256]uint32

	dict     []uint32
	pattern  []uint32
	dictLink []uint32

	// rootStop[b] is 1 if byte b moves the automaton out of the root
	// state, 0 if it self-loops. Runs of zero bytes can be skipped
	// wholesale while at the root, since the root never produces a
	// match. Using 0/1 bytes (rather than bools) lets the skip loop
	// OR eight lookups together and take one branch per eight bytes.
	rootStop [256]byte
	// rootStopBytes holds the single stop byte when the root leaves its
	// self-loop on exactly one byte value; the scan loops then skip root
	// self-loops with SWAR plus the vectorized bytes.IndexByte instead of
	// the rootStop table. Empty for zero or several stop bytes.
	rootStopBytes []byte

	bufPool sync.Pool // Pool of *matchBuf
}

// matchBuf holds the per-call scratch for Match, recycled through a
// pool: the returned buffer is acquired with one Get and released with
// one Put via ReleaseMatches. During the scan, matches are recorded as
// raw integer pairs (end position, and pattern id packed with match
// length); appends of plain integers carry no pointers, so they stay off
// the GC write-barrier path. The Match structs and the returned pointer
// slice are materialized in one pass afterwards, when the final count is
// known, so the arena never reallocates under live pointers.
type matchBuf struct {
	raw   []uint64 // pairs: end position, packed pattern+length
	ptrs  []*Match
	arena []Match
}

// reset prepares the buffer for reuse, keeping all allocated capacity.
func (b *matchBuf) reset() {
	b.raw = b.raw[:0]
	b.ptrs = b.ptrs[:0]
}

// materialize builds the arena of Match values and the pointer slice
// from the recorded raw pairs.
func (b *matchBuf) materialize(input []byte) {
	n := len(b.raw) / 2
	if cap(b.arena) < n {
		b.arena = make([]Match, n)
	} else {
		// Clear the dropped tail before reslicing down: stale Match values
		// in arena[n:] would otherwise keep the previous input alive via
		// their match slices while the buffer sits idle in the pool.
		old := b.arena
		b.arena = b.arena[:n]
		if len(old) > n {
			clear(old[n:])
		}
	}
	if cap(b.ptrs) < n {
		b.ptrs = make([]*Match, n)
	} else {
		b.ptrs = b.ptrs[:n]
	}
	for k := 0; k < n; k++ {
		end := b.raw[2*k]
		dp := b.raw[2*k+1]
		ln := uint32(dp)
		pos := uint32(end) - ln + 1
		m := &b.arena[k]
		m.pos = pos
		m.pattern = uint32(dp >> 32)
		m.match = input[pos : uint32(end)+1]
		m.buf = nil
		b.ptrs[k] = m
	}
}

func newBufPool() sync.Pool {
	return sync.Pool{
		New: func() any { return new(matchBuf) },
	}
}

// buildRootSkip derives the root self-loop byte set from failTrans.
// Must be called after failTrans is fully populated.
func (tr *Trie) buildRootSkip() {
	tr.rootStopBytes = nil
	for b := range 256 {
		if tr.failTrans[rootState][b] != rootState {
			tr.rootStop[b] = 1
			tr.rootStopBytes = append(tr.rootStopBytes, byte(b))
		} else {
			tr.rootStop[b] = 0
		}
	}
	// With a single stop byte, the scan loops skip root self-loops with
	// an inline SWAR word scan plus the vectorized bytes.IndexByte (see
	// walkStopByte). Several stop bytes would need one IndexByte pass per
	// value, so fall back to the rootStop table.
	if len(tr.rootStopBytes) > 1 {
		tr.rootStopBytes = nil
	}
}

// swar constants for the zero-byte test: bit 7 of each byte of
// (w-ones) & ^w & highs is set iff that byte of w is zero.
const (
	swarOnes  uint64 = 0x0101010101010101
	swarHighs uint64 = swarOnes << 7
)

// skipRootTable returns the position of the first byte at or after i
// that leaves the root state, or len(input) if there is none, using the
// rootStop lookup table.
func (tr *Trie) skipRootTable(input []byte, i int) int {
	rootStop := &tr.rootStop
	inputLen := len(input)
	// Eight lookups are OR-ed together so the common all-skippable
	// case costs a single branch per eight input bytes.
	for i+8 <= inputLen {
		m := rootStop[input[i]] | rootStop[input[i+1]] |
			rootStop[input[i+2]] | rootStop[input[i+3]] |
			rootStop[input[i+4]] | rootStop[input[i+5]] |
			rootStop[input[i+6]] | rootStop[input[i+7]]
		if m != 0 {
			break
		}
		i += 8
	}
	for i < inputLen && rootStop[input[i]] == 0 {
		i++
	}
	return i
}

// Walk calls this function on any match, giving the end position, length of the matched bytes,
// and the pattern number.
type WalkFn func(end, n, pattern uint32) bool

// Walk runs the algorithm on a given output, calling the supplied callback function on every
// match. The algorithm will terminate if the callback function returns false.
func (tr *Trie) Walk(input []byte, fn WalkFn) {
	if len(tr.rootStopBytes) == 1 {
		tr.walkStopByte(input, fn)
		return
	}
	tr.walkTable(input, fn)
}

// walkStopByte is Walk specialized for tries whose root leaves on a
// single byte value: the root skip is an inlined SWAR word scan with a
// vectorized bytes.IndexByte fallback for long gaps.
func (tr *Trie) walkStopByte(input []byte, fn WalkFn) {
	failTrans := tr.failTrans
	dict := tr.dict
	pattern := tr.pattern
	dictLink := tr.dictLink

	c := tr.rootStopBytes[0]
	cc := uint64(c) * swarOnes

	s := rootState

	inputLen := len(input)
	for i := 0; i < inputLen; i++ {
		if s == rootState && input[i] != c {
			// Skip to the next stop byte. Typical gaps are short, so
			// scan a few words with SWAR first; a bytes.IndexByte call
			// per gap would be dominated by its setup cost.
		skip:
			for k := 0; ; k++ {
				if i+8 > inputLen {
					for i < inputLen && input[i] != c {
						i++
					}
					break
				}
				w := binary.LittleEndian.Uint64(input[i:]) ^ cc
				if m := (w - swarOnes) & ^w & swarHighs; m != 0 {
					i += bits.TrailingZeros64(m) >> 3
					break
				}
				i += 8
				if k == 3 {
					j := bytes.IndexByte(input[i:], c)
					if j < 0 {
						i = inputLen
					} else {
						i += j
					}
					break skip
				}
			}
			if i == inputLen {
				return
			}
		}

		s = failTrans[s][input[i]]
		ds := dict[s]
		dl := dictLink[s]
		if ds != 0 || dl != nilState {
			if ds != 0 && !fn(uint32(i), ds, pattern[s]) {
				return
			}
			for u := dl; u != nilState; u = dictLink[u] {
				if !fn(uint32(i), dict[u], pattern[u]) {
					return
				}
			}
		}
	}
}

// walkTable is Walk for tries with several root stop bytes, using the
// rootStop table to skip root self-loops.
func (tr *Trie) walkTable(input []byte, fn WalkFn) {
	failTrans := tr.failTrans
	dict := tr.dict
	pattern := tr.pattern
	dictLink := tr.dictLink

	s := rootState

	inputLen := len(input)
	for i := 0; i < inputLen; i++ {
		if s == rootState {
			// Fast path: while at the root, skip bytes that self-loop.
			// The root state never produces a match, so no dict checks
			// are needed until we leave it.
			i = tr.skipRootTable(input, i)
			if i == inputLen {
				return
			}
		}

		s = failTrans[s][input[i]]
		ds := dict[s]
		dl := dictLink[s]
		if ds != 0 || dl != nilState {
			if ds != 0 && !fn(uint32(i), ds, pattern[s]) {
				return
			}
			for u := dl; u != nilState; u = dictLink[u] {
				if !fn(uint32(i), dict[u], pattern[u]) {
					return
				}
			}
		}
	}
}

// Match runs the Aho-Corasick string-search algorithm on a byte input.
func (tr *Trie) Match(input []byte) []*Match {
	buf := tr.bufPool.Get().(*matchBuf)
	buf.reset()

	// Record each match as a raw (end position, packed pattern+length)
	// pair. Integer appends carry no pointers, so the scan never touches
	// the GC write-barrier path.
	tr.Walk(input, func(end, n, pattern uint32) bool {
		buf.raw = append(buf.raw, uint64(end), uint64(pattern)<<32|uint64(n))
		return true
	})

	if len(buf.raw) == 0 {
		tr.bufPool.Put(buf)
		return nil
	}

	buf.materialize(input)

	// Stash the buffer handle in the first match so ReleaseMatches can
	// recycle the whole buffer in one pool operation. A retained match
	// therefore keeps the whole batch's scratch (raw, ptrs, arena) alive
	// until ReleaseMatches or until the result is dropped and GC'd;
	// callers that hold one match long-term should copy its fields.
	buf.ptrs[0].buf = buf
	return buf.ptrs
}

// MatchFirst is the same as Match, but returns after first successful match.
func (tr *Trie) MatchFirst(input []byte) *Match {
	var match *Match

	tr.Walk(input, func(end, n, pattern uint32) bool {
		pos := end - n + 1
		match = &Match{pos: pos, pattern: pattern, match: input[pos : pos+n]}
		return false
	})

	return match
}

// MatchString runs the Aho-Corasick string-search algorithm on a string input.
func (tr *Trie) MatchString(input string) []*Match {
	return tr.Match([]byte(input))
}

// MatchFirstString is the same as MatchString, but returns after first successful match.
func (tr *Trie) MatchFirstString(input string) *Match {
	return tr.MatchFirst([]byte(input))
}

// ReleaseMatches returns the scratch buffer backing a Match result to the
// Trie's pool for reuse. Releasing is optional: a result that is simply
// dropped is reclaimed by the GC.
//
// Pass the exact slice returned by Match, at most once. After the call
// the slice and every Match in it are invalid — the buffer may be handed
// to a later Match and overwritten, so reading it or releasing it again
// can corrupt an unrelated result. The pool handle is anchored to the
// batch's first element, so a nil/empty slice, a slice not obtained from
// Match, or a tail sub-slice such as result[1:] is a no-op; a sub-slice
// that includes the original first element (e.g. result[:k]) releases the
// whole underlying buffer.
func (tr *Trie) ReleaseMatches(matches []*Match) {
	if len(matches) == 0 {
		return
	}
	buf := matches[0].buf
	if buf == nil {
		return
	}
	matches[0].buf = nil
	tr.bufPool.Put(buf)
}
