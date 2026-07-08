package ahocorasick

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"runtime"
	"sync"
	"unsafe"
)

const (
	rootState uint32 = 1
	nilState  uint32 = 0

	// outputFlag is set on a failTrans entry when the target state emits
	// at least one match (dict or dictLink). It rides along with the
	// transition load, so the common no-match case needs no extra memory
	// accesses. stateMask recovers the state id.
	outputFlag uint32 = 1 << 31
	stateMask  uint32 = outputFlag - 1
)

// Trie represents a trie of patterns with extra links as per the Aho-Corasick algorithm.
type Trie struct {
	failTrans [][256]uint32

	dict     []uint32
	pattern  []uint32
	dictLink []uint32

	// dictPat[s] packs pattern[s] (high 32 bits) and dict[s] (low 32
	// bits) so the emit path fetches both with a single load from one
	// cache line.
	dictPat []uint64

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

	// maxLen is the longest pattern length. Parallel scans start each
	// chunk maxLen-1 bytes early so boundary-spanning matches are found.
	maxLen uint32

	// failTrans16 is a half-width copy of failTrans used by the match
	// loops when every state id fits in 15 bits (bit 15 carries the
	// output flag). Rows are 512B instead of 1KB, halving the cache
	// footprint of the table the serial dependency chain loads from.
	failTrans16 []uint16

	// stopEntry16 is failTrans16[root][stopByte] when there is a single
	// stop byte: the root always transitions to the same depth-1 state
	// on it, so scan loops substitute this constant for the table load
	// on every root re-entry, cutting a serial L1 access.
	stopEntry16 uint16

	bufPool sync.Pool // Pool of *matchBuf
}

// matchBuf holds the per-call scratch for Match, recycled through a
// pool: the returned buffer is acquired with one Get and released with
// one Put via ReleaseMatches. The parallel path additionally borrows and
// returns one pooled buffer per worker. During the scan, matches are
// recorded as raw integer pairs (end position, packed dictPat); appends
// of plain integers carry no pointers, so they stay off the GC
// write-barrier path. The Match structs and the returned pointer slice
// are materialized in one pass afterwards, when the final count is
// known, so the arena never reallocates under live pointers.
type matchBuf struct {
	raw   []uint64 // pairs: end position, dictPat
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

// addOutputFlags sets outputFlag on every transition whose target state
// emits at least one match, and builds the packed dictPat array.
// Idempotent; must be called after failTrans, dict, and dictLink are
// fully populated.
func (tr *Trie) addOutputFlags() {
	var emits = make([]bool, len(tr.dict))
	for s := range emits {
		emits[s] = tr.dict[s] != 0 || tr.dictLink[s] != nilState
	}
	for s := range tr.failTrans {
		for b := range 256 {
			if emits[tr.failTrans[s][b]&stateMask] {
				tr.failTrans[s][b] |= outputFlag
			}
		}
	}

	tr.dictPat = make([]uint64, len(tr.dict))
	tr.maxLen = 0
	for s := range tr.dict {
		tr.dictPat[s] = uint64(tr.pattern[s])<<32 | uint64(tr.dict[s])
		if tr.dict[s] > tr.maxLen {
			tr.maxLen = tr.dict[s]
		}
	}

	tr.failTrans16 = nil
	if len(tr.failTrans) <= 1<<15 {
		tr.failTrans16 = make([]uint16, len(tr.failTrans)*256)
		for s := range tr.failTrans {
			for b := range 256 {
				v := tr.failTrans[s][b]
				w := uint16(v & stateMask)
				if v&outputFlag != 0 {
					w |= 1 << 15
				}
				tr.failTrans16[s<<8+b] = w
			}
		}
	}
}

// setStopEntry caches the root transition on the single stop byte.
// Must run after both buildRootSkip and the failTrans16 build.
func (tr *Trie) setStopEntry() {
	tr.stopEntry16 = 0
	if tr.failTrans16 != nil && len(tr.rootStopBytes) == 1 {
		tr.stopEntry16 = tr.failTrans16[int(rootState)<<8+int(tr.rootStopBytes[0])]
	}
}

// buildRootSkip derives the root self-loop byte set from failTrans.
// Must be called after failTrans is fully populated.
func (tr *Trie) buildRootSkip() {
	tr.rootStopBytes = nil
	for b := range 256 {
		if tr.failTrans[rootState][b]&stateMask != rootState {
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

// rootSkipSampleLen bounds how many root-state bytes walkTable samples,
// inline, before it commits to whether the root self-loop skip pays off at
// the input's stop-byte density.
const rootSkipSampleLen = 4096

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
	dictPat := tr.dictPat
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

		v := failTrans[s][input[i]]
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := dictPat[s]; uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := dictLink[s]; u != nilState; u = dictLink[u] {
				dp := dictPat[u]
				if !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
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
	dictPat := tr.dictPat
	dictLink := tr.dictLink

	s := rootState

	inputLen := len(input)

	// Root self-loop skip, gated on stop-byte density measured INLINE so an
	// early-terminating caller (Walk whose callback returns false, as
	// MatchFirst does) never pays an up-front prefix scan. Begin optimistic;
	// over the first rootSkipSampleLen root-state bytes, accumulate how many
	// leave the root (stop bytes). If that density reaches the measured
	// break-even (~1/16), where the skip machinery costs about as much as the
	// plain loop, disable it for the remainder. The sampled bytes are ones the
	// skip reads anyway, so this adds no extra reads and nothing before the
	// first match.
	skip := true
	sample := rootSkipSampleLen
	var rootBytes, stopBytes int

	for i := 0; i < inputLen; i++ {
		if skip && s == rootState {
			// Fast path: while at the root, skip bytes that self-loop.
			// The root state never produces a match, so no dict checks
			// are needed until we leave it.
			j := tr.skipRootTable(input, i)
			if sample > 0 && j < inputLen {
				// j-i self-looping bytes, then one stop byte at j.
				rootBytes += j - i + 1
				stopBytes++
				sample -= j - i + 1
				if sample <= 0 && stopBytes*16 >= rootBytes {
					skip = false
				}
			}
			i = j
			if i == inputLen {
				return
			}
		}

		v := failTrans[s][input[i]]
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := dictPat[s]; uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := dictLink[s]; u != nilState; u = dictLink[u] {
				dp := dictPat[u]
				if !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
					return
				}
			}
		}
	}
}

// parallelChunk is the minimum bytes of input per worker goroutine;
// below it, goroutine startup outweighs the scan work.
const parallelChunk = 8 << 10

// Match runs the Aho-Corasick string-search algorithm on a byte input.
func (tr *Trie) Match(input []byte) []*Match {
	if len(input) >= 2*parallelChunk {
		// More workers mainly adds wakeup and steal latency; cap at 8 to
		// keep each chunk large enough to amortize goroutine startup.
		if p := min(runtime.GOMAXPROCS(0), len(input)/parallelChunk, 8); p > 1 {
			return tr.matchParallel(input, p)
		}
	}

	buf := tr.bufPool.Get().(*matchBuf)
	buf.reset()

	tr.matchSeq(input, buf)

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

// matchSeq scans input sequentially into buf.
func (tr *Trie) matchSeq(input []byte, buf *matchBuf) {
	if tr.failTrans16 != nil && len(tr.rootStopBytes) == 1 {
		tr.matchStopByte16(input, buf)
		return
	}
	if len(tr.rootStopBytes) == 1 {
		tr.matchStopByte(input, buf)
	} else {
		tr.matchTable(input, buf)
	}
}

// matchStopByte16 is matchStopByte on the half-width failTrans16 table.
// See matchStopByte for why the loads use raw pointer arithmetic and for
// the offset shifts.
func (tr *Trie) matchStopByte16(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	c := tr.rootStopBytes[0]
	cc := uint64(c) * swarOnes
	stopE := uint32(tr.stopEntry16)

	s := rootState

	inputLen := len(input)
	for i := 0; i < inputLen; i++ {
		var v uint32
		if s == rootState {
			if input[i] != c {
				// See walkStopByte for the skip strategy.
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
			// At the root the cursor is on the stop byte, so the
			// transition is the precomputed constant: no table load
			// on the serial chain.
			v = stopE
		} else {
			v = uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
		}
		s = v &^ (1 << 15)
		if v&(1<<15) != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				buf.raw = append(buf.raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				buf.raw = append(buf.raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
}

// matchParallel splits input across p goroutines. Each worker scans its
// chunk plus the preceding maxLen-1 bytes. A match ending at or after
// its chunk start is at most maxLen bytes long, so it begins within that
// overlap; scanning the overlap from the root therefore reaches a state
// that reports every such match exactly as a sequential scan would.
// Matches ending inside the overlap belong to the previous chunk and are
// dropped, so the concatenated per-chunk results equal a sequential
// scan's output.
func (tr *Trie) matchParallel(input []byte, p int) []*Match {
	overlap := int(tr.maxLen) - 1
	chunk := (len(input) + p - 1) / p
	if overlap*4 > chunk {
		// Overlap rescanning would dominate; not worth parallelizing.
		p = 0
	}

	bufs := make([]*matchBuf, p)
	var wg sync.WaitGroup
	for k := 1; k < p; k++ {
		start := k * chunk
		end := min(start+chunk, len(input))
		buf := tr.bufPool.Get().(*matchBuf)
		buf.reset()
		bufs[k] = buf
		wg.Add(1)
		go func(start, end int, buf *matchBuf) {
			defer wg.Done()
			scanStart := max(start-overlap, 0)
			tr.matchSeq(input[scanStart:end], buf)
			// Rebase positions to the full input and drop matches that
			// end in the overlap (owned by the previous chunk). Entries
			// are ordered by end position, so they form a prefix.
			raw := buf.raw
			drop := 0
			for drop < len(raw) && int(raw[drop])+scanStart < start {
				drop += 2
			}
			raw = raw[drop:]
			for idx := 0; idx < len(raw); idx += 2 {
				raw[idx] += uint64(scanStart)
			}
			buf.raw = raw
		}(start, end, buf)
	}

	var main *matchBuf
	if p > 0 {
		main = tr.bufPool.Get().(*matchBuf)
		main.reset()
		tr.matchSeq(input[:min(chunk, len(input))], main)
		wg.Wait()
		for k := 1; k < p; k++ {
			main.raw = append(main.raw, bufs[k].raw...)
			bufs[k].raw = bufs[k].raw[:0]
			tr.bufPool.Put(bufs[k])
		}
	} else {
		main = tr.bufPool.Get().(*matchBuf)
		main.reset()
		tr.matchSeq(input, main)
	}

	if len(main.raw) == 0 {
		tr.bufPool.Put(main)
		return nil
	}
	main.materialize(input)
	main.ptrs[0].buf = main
	return main.ptrs
}

// matchStopByte is the Match specialization of walkStopByte: matches are
// appended straight to buf, avoiding an indirect callback call per match.
//
// The transition and emit loads use raw pointer arithmetic: state ids
// come from masking a table entry, so the compiler cannot prove them in
// bounds and would otherwise emit a bounds check on the critical
// dependency chain. All entries are valid state ids by construction
// (builder and decoder populate every slot), so the accesses are safe.
// The offsets are byte offsets into each array: <<10 selects a 1KB
// failTrans row (256 uint32) and <<2 the byte column within it; <<3
// indexes a dictPat (uint64) and <<2 a dictLink (uint32). The failTrans16
// path halves these to <<9 for the 512B row and <<1 for the uint16
// column.
func (tr *Trie) matchStopByte(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	c := tr.rootStopBytes[0]
	cc := uint64(c) * swarOnes

	s := rootState

	inputLen := len(input)
	for i := 0; i < inputLen; i++ {
		if s == rootState && input[i] != c {
			// See walkStopByte for the skip strategy.
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

		v := *(*uint32)(unsafe.Add(ftBase, uintptr(s)<<10+uintptr(input[i])<<2))
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				buf.raw = append(buf.raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				buf.raw = append(buf.raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
}

// matchTable is the Match specialization of walkTable. See matchStopByte
// for why the loads use raw pointer arithmetic.
func (tr *Trie) matchTable(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	s := rootState

	inputLen := len(input)

	// Root self-loop skip gated on stop-byte density measured inline; see
	// walkTable. Sample the first rootSkipSampleLen root-state bytes, and
	// disable the skip for the remainder once density reaches ~1/16.
	skip := true
	sample := rootSkipSampleLen
	var rootBytes, stopBytes int

	for i := 0; i < inputLen; i++ {
		if skip && s == rootState {
			j := tr.skipRootTable(input, i)
			if sample > 0 && j < inputLen {
				// j-i self-looping bytes, then one stop byte at j.
				rootBytes += j - i + 1
				stopBytes++
				sample -= j - i + 1
				if sample <= 0 && stopBytes*16 >= rootBytes {
					skip = false
				}
			}
			i = j
			if i == inputLen {
				return
			}
		}

		v := *(*uint32)(unsafe.Add(ftBase, uintptr(s)<<10+uintptr(input[i])<<2))
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				buf.raw = append(buf.raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				buf.raw = append(buf.raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
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
