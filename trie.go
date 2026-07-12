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

	// skipBytes holds all stop bytes when there are two to four of
	// them: long root gaps then escape from the byte-table skip to one
	// windowed bytes.IndexByte pass per value, trading k vectorized
	// scans for a table walk. Empty otherwise.
	skipBytes []byte

	// maxLen is the longest pattern length. Parallel scans start each
	// chunk maxLen-1 bytes early so boundary-spanning matches are found.
	maxLen uint32

	// failTrans16 is a half-width copy of failTrans used by the match
	// loops when every state id fits in 15 bits (bit 15 carries the
	// output flag). Rows are 512B instead of 1KB, halving the cache
	// footprint of the table the serial dependency chain loads from.
	// Built only when the single-stop-byte 16-bit matcher can use it
	// (see buildFailTrans16); nil otherwise.
	failTrans16 []uint16

	// stopEntry16 is failTrans16[root][stopByte] when there is a single
	// stop byte: the root always transitions to the same depth-1 state
	// on it, so scan loops substitute this constant for the table load
	// on every root re-entry, cutting a serial L1 access.
	stopEntry16 uint16

	// failTransC is a byte-class-compressed copy of failTrans, built for
	// automata too large for failTrans16 when few enough distinct byte
	// values appear in the patterns. Bytes that appear in no pattern all
	// behave identically (every state moves to the root), so one class
	// column stands in for all of them; each pattern byte gets its own
	// class. Rows shrink from 1KB to classStride*4 bytes, so the table
	// the serial dependency chain loads from drops from hundreds of MB
	// toward the L2/L3 sizes that dominate its latency. classOf maps an
	// input byte to its class; classShift is log2 of the row stride.
	failTransC []uint32
	classOf    [256]uint8
	classShift uint32

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
	raw2  []uint64 // second lane of the dual-cursor scan
	ptrs  []*Match
	arena []Match
}

// reset prepares the buffer for reuse, keeping all allocated capacity.
func (b *matchBuf) reset() {
	b.raw = b.raw[:0]
	b.raw2 = b.raw2[:0]
	b.ptrs = b.ptrs[:0]
}

// materialize builds the arena of Match values and the pointer slice
// from the recorded raw pairs.
// sizeArena prepares arena and ptrs for exactly n Match values, keeping
// capacity and clearing any stale tail (stale Match values would keep
// the previous input alive via their match slices while the buffer sits
// idle in the pool).
func (b *matchBuf) sizeArena(n int) {
	if cap(b.arena) < n {
		b.arena = make([]Match, n)
	} else {
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
}

// materializeSegment expands raw pairs into b.arena/b.ptrs starting at
// index off, returning the index after the last entry written. The
// arena must already be sized; segments written by different goroutines
// are disjoint, so parallel calls are safe.
func (b *matchBuf) materializeSegment(input []byte, raw []uint64, off int) int {
	for k := 0; k < len(raw)/2; k++ {
		end := raw[2*k]
		dp := raw[2*k+1]
		ln := uint32(dp)
		pos := uint32(end) - ln + 1
		m := &b.arena[off+k]
		m.pos = pos
		m.pattern = uint32(dp >> 32)
		m.match = input[pos : uint32(end)+1]
		m.buf = nil
		b.ptrs[off+k] = m
	}
	return off + len(raw)/2
}

// materialize builds the arena of Match values and the pointer slice
// from the recorded raw pairs.
func (b *matchBuf) materialize(input []byte) {
	b.sizeArena(len(b.raw) / 2)
	b.materializeSegment(input, b.raw, 0)
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
}

// buildFailTrans16 builds the half-width transition table whenever every
// state id fits in 15 bits. Both the single-stop-byte loops
// (matchStopByte16, walkStopByte16) and the multi-stop table loops
// (matchTable16, walkTable16) read it, so any trie small enough to have
// one uses it on every scan; the 512B/state it costs on such tries buys
// the halved cache footprint of the serial transition chain. Must run
// after addOutputFlags (reads the flag bits).
func (tr *Trie) buildFailTrans16() {
	tr.failTrans16 = nil
	if len(tr.failTrans) > 1<<15 {
		return
	}
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

// buildClassTable builds the byte-class-compressed transition table for
// automata that cannot use failTrans16. Only worthwhile when the class
// row is at most half the full row: beyond that the compressed table
// stops fitting meaningfully better in cache. Idempotent; must run after
// addOutputFlags (flags ride along in the copied entries).
//
// A byte is live iff some state moves on it — equivalently, iff it
// appears in some pattern; every dead byte sends every state to the
// plain root and shares class 0. The builder passes the live set it
// already knows; the decoder derives it with derivedLiveBytes.
func (tr *Trie) buildClassTable(live *[256]bool) {
	tr.failTransC = nil
	if tr.failTrans16 != nil {
		return
	}

	numClasses := 1
	for b := range 256 {
		if live[b] {
			tr.classOf[b] = uint8(numClasses)
			numClasses++
		} else {
			tr.classOf[b] = 0
		}
	}

	stride := 1
	shift := uint32(0)
	for stride < numClasses {
		stride <<= 1
		shift++
	}
	if stride > 128 {
		return
	}

	tr.classShift = shift
	// Only live columns need copying: dead columns are all class 0,
	// pre-set to the root. Iterating the live list keeps this pass
	// O(states x liveBytes) instead of O(states x 256).
	liveList := make([]int, 0, numClasses-1)
	for b := range 256 {
		if live[b] {
			liveList = append(liveList, b)
		}
	}
	tr.failTransC = make([]uint32, len(tr.failTrans)*stride)
	for s := range tr.failTrans {
		row := &tr.failTrans[s]
		crow := tr.failTransC[s<<shift : s<<shift+stride]
		crow[0] = rootState
		for _, b := range liveList {
			crow[tr.classOf[b]] = row[b]
		}
	}
}

// derivedLiveBytes recovers the live-byte set from a populated failTrans
// (for decoded tries, where the builder's pattern knowledge is gone): a
// byte is live iff any state's entry on it differs from a plain root
// transition. Costs a full table scan; call only when buildClassTable
// will actually build (failTrans16 == nil).
func (tr *Trie) derivedLiveBytes() *[256]bool {
	var live [256]bool
	for s := range tr.failTrans {
		row := &tr.failTrans[s]
		for b := range 256 {
			if row[b] != rootState {
				live[b] = true
			}
		}
	}
	return &live
}

// setStopEntry caches the root transition on the single stop byte.
// Must run after both buildRootSkip and buildFailTrans16.
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
	tr.skipBytes = nil
	var stops []byte
	for b := range 256 {
		if tr.failTrans[rootState][b]&stateMask != rootState {
			tr.rootStop[b] = 1
			stops = append(stops, byte(b))
		} else {
			tr.rootStop[b] = 0
		}
	}
	// With a single stop byte, the scan loops skip root self-loops with
	// an inline SWAR word scan plus the vectorized bytes.IndexByte (see
	// walkStopByte). With two to four, the table skip escapes to
	// windowed per-value IndexByte passes on long gaps (skipRootTable).
	// Beyond that the k passes stop paying for themselves.
	switch {
	case len(stops) == 1:
		tr.rootStopBytes = stops
	case len(stops) <= 4:
		tr.skipBytes = stops
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
// rootStop lookup table. When the stop set is small (skipBytes
// non-empty), gaps that outlast the first 128 table-scanned bytes escape
// to windowed per-value bytes.IndexByte passes: k vectorized scans beat
// the byte-table walk severalfold, and windowing bounds the work a rare
// value's scan can waste when another stop byte hits early.
func (tr *Trie) skipRootTable(input []byte, i int) int {
	rootStop := &tr.rootStop
	inputLen := len(input)
	if tr.skipBytes != nil {
		lim := i + 128
		for i+8 <= inputLen {
			// See the comment on the main loop below.
			if rootStop[input[i]]|rootStop[input[i+1]]|
				rootStop[input[i+2]]|rootStop[input[i+3]]|
				rootStop[input[i+4]]|rootStop[input[i+5]]|
				rootStop[input[i+6]]|rootStop[input[i+7]] != 0 {
				m := uint64(rootStop[input[i]]) |
					uint64(rootStop[input[i+1]])<<8 |
					uint64(rootStop[input[i+2]])<<16 |
					uint64(rootStop[input[i+3]])<<24 |
					uint64(rootStop[input[i+4]])<<32 |
					uint64(rootStop[input[i+5]])<<40 |
					uint64(rootStop[input[i+6]])<<48 |
					uint64(rootStop[input[i+7]])<<56
				return i + bits.TrailingZeros64(m)>>3
			}
			i += 8
			if i >= lim {
				return tr.skipRootIndex(input, i)
			}
		}
		for i < inputLen && rootStop[input[i]] == 0 {
			i++
		}
		return i
	}
	// The eight 0/1 lookups are OR-ed together as the cheap reject, so
	// the common all-skippable case costs a single branch per eight
	// input bytes with no extra shift work. Only when the group holds a
	// stop byte are the lookups re-shifted into disjoint bytes of one
	// word, where TrailingZeros64 pinpoints the stop byte — replacing
	// the up-to-8-iteration scalar re-scan (dependent loads plus a
	// mispredict-prone branch each) with a branchless locate. Pure-skip
	// input never pays the shift tree; dense-exit input pays it once
	// per hit instead of the scalar tail.
	for i+8 <= inputLen {
		if rootStop[input[i]]|rootStop[input[i+1]]|
			rootStop[input[i+2]]|rootStop[input[i+3]]|
			rootStop[input[i+4]]|rootStop[input[i+5]]|
			rootStop[input[i+6]]|rootStop[input[i+7]] != 0 {
			m := uint64(rootStop[input[i]]) |
				uint64(rootStop[input[i+1]])<<8 |
				uint64(rootStop[input[i+2]])<<16 |
				uint64(rootStop[input[i+3]])<<24 |
				uint64(rootStop[input[i+4]])<<32 |
				uint64(rootStop[input[i+5]])<<40 |
				uint64(rootStop[input[i+6]])<<48 |
				uint64(rootStop[input[i+7]])<<56
			return i + bits.TrailingZeros64(m)>>3
		}
		i += 8
	}
	for i < inputLen && rootStop[input[i]] == 0 {
		i++
	}
	return i
}

// skipRootIndex finds the next stop byte at or after i with one
// bytes.IndexByte pass per stop value over successive 2KB windows.
func (tr *Trie) skipRootIndex(input []byte, i int) int {
	const window = 2048
	inputLen := len(input)
	for i < inputLen {
		end := min(i+window, inputLen)
		w := input[i:end]
		best := -1
		for _, c := range tr.skipBytes {
			if j := bytes.IndexByte(w, c); j >= 0 {
				if best < 0 || j < best {
					best = j
				}
				w = w[:j+1] // later values only matter if earlier
			}
		}
		if best >= 0 {
			return i + best
		}
		i = end
	}
	return inputLen
}

// Walk calls this function on any match, giving the end position, length of the matched bytes,
// and the pattern number.
type WalkFn func(end, n, pattern uint32) bool

// Walk runs the algorithm on a given output, calling the supplied callback function on every
// match. The algorithm will terminate if the callback function returns false.
func (tr *Trie) Walk(input []byte, fn WalkFn) {
	if tr.failTrans16 != nil {
		if len(tr.rootStopBytes) == 1 {
			tr.walkStopByte16(input, fn)
		} else {
			tr.walkTable16(input, fn)
		}
		return
	}
	if len(tr.rootStopBytes) == 1 {
		tr.walkStopByte(input, fn)
		return
	}
	tr.walkTable(input, fn)
}

// walkStopByte16 is walkStopByte on the half-width failTrans16 table,
// with the same root-transition constant (stopEntry16) and raw pointer
// loads as matchStopByte16. See matchStopByte for the offset shifts.
func (tr *Trie) walkStopByte16(input []byte, fn WalkFn) {
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
				i = nextStop(input, i, c, cc)
				if i == inputLen {
					return
				}
			}
			v = stopE
		} else {
			v = uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
		}
		s = v &^ (1 << 15)
		if v&(1<<15) != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				dp := *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3))
				if !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
					return
				}
			}
		}
	}
}

// walkTable16 is walkTable on the half-width failTrans16 table with raw
// pointer loads. See matchStopByte for the offset shifts.
func (tr *Trie) walkTable16(input []byte, fn WalkFn) {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	s := rootState

	inputLen := len(input)

	for i := 0; i < inputLen; i++ {
		if s == rootState && tr.rootStop[input[i]] == 0 {
			i = tr.skipRootTable(input, i)
			if i == inputLen {
				return
			}
		}

		v := uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
		s = v &^ (1 << 15)
		if v&(1<<15) != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				dp := *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3))
				if !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
					return
				}
			}
		}
	}
}

// walkStopByte is Walk specialized for tries whose root leaves on a
// single byte value: the root skip is an inlined SWAR word scan with a
// vectorized bytes.IndexByte fallback for long gaps.
func (tr *Trie) walkStopByte(input []byte, fn WalkFn) {
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

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

		v := *(*uint32)(unsafe.Add(ftBase, uintptr(s)<<10+uintptr(input[i])<<2))
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				dp := *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3))
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
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	s := rootState

	inputLen := len(input)

	for i := 0; i < inputLen; i++ {
		if s == rootState && tr.rootStop[input[i]] == 0 {
			// Fast path: while at the root, skip bytes that self-loop.
			// The root state never produces a match, so no dict checks
			// are needed until we leave it. The guard keeps the
			// non-inlinable call off root visits already sitting on a
			// stop byte (nothing to skip) — with it, the skip wins or
			// ties at every stop-byte density (see the density-sweep
			// benchmarks), so no adaptive gate is needed.
			i = tr.skipRootTable(input, i)
			if i == inputLen {
				return
			}
		}

		v := *(*uint32)(unsafe.Add(ftBase, uintptr(s)<<10+uintptr(input[i])<<2))
		s = v & stateMask
		if v&outputFlag != 0 {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 && !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				dp := *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3))
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
	if len(input) >= 32<<10 {
		// The single-stop-byte automaton scans sparse input at ~2GB/s
		// serially, so goroutine startup, the redundant overlap
		// re-scans, and the merge fan-in only pay for themselves near
		// 128KB; on stop-byte-dense input its serial throughput drops
		// several-fold and parallelism pays from 32KB, like the slower
		// table-path automata.
		pmin := 32 << 10
		if tr.failTrans16 != nil && len(tr.rootStopBytes) == 1 &&
			!looksDense(input, tr.rootStopBytes[0]) {
			pmin = 128 << 10
		}
		if len(input) >= pmin {
			// The worker cap bounds wakeup and merge fan-in: 8 below
			// 256KB, where each worker gets only a few KB of a fast
			// scan, 32 above (scan throughput saturates near 32
			// workers, measured on 48-core Zen 4).
			maxP := 8
			if len(input) >= 256<<10 {
				maxP = 32
			}
			if p := min(runtime.GOMAXPROCS(0), len(input)/parallelChunk, maxP); p > 1 {
				return tr.matchParallel(input, p)
			}
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

// dualThreshold is the minimum input size for the dual-cursor scan.
const dualThreshold = 1024

// dualDenseThreshold is the minimum sampled stop-byte density for the
// dual-cursor scan, in stop bytes per 4096 input bytes (~10%).
// Measured on Zen 4: natural text over a single-stop-byte pattern set
// sits near 3-4% (short excursions, single-cursor inline skip wins by
// 10-15%), while concatenated dictionary words sit at 11-15% (long
// dependent-load excursions, dual-cursor wins by 25-45% on small
// inputs). Word-plus-filler mixtures up to ~9% still favor the single
// cursor.
const dualDenseThreshold = 410

// looksDense samples up to three 1KB windows of input (head, middle,
// tail) and reports whether the stop-byte density is high enough for the
// dual-cursor scan to pay. Costs three vectorized bytes.Count calls,
// noise against a scan that touches every byte.
func looksDense(input []byte, c byte) bool {
	n := len(input)
	if n <= 4096 {
		return bytes.Count(input, []byte{c})*4096 >= dualDenseThreshold*n
	}
	k := bytes.Count(input[:1024], []byte{c})
	mid := n / 2
	k += bytes.Count(input[mid:mid+1024], []byte{c})
	k += bytes.Count(input[n-1024:], []byte{c})
	return k*4 >= dualDenseThreshold*3
}

// dualTableMin is the minimum input size for the dual-cursor table
// scans: below it the rootDense sampling cost is a meaningful fraction
// of the whole scan.
const dualTableMin = 4096

// rootDenseThreshold is the minimum number of root-leaving bytes in the
// 768 sampled by rootDense (~28%) for the dual-cursor table scan.
// Measured on Zen 4: natural text over the multi-stop word automata
// samples at 15-20% (single-cursor wins by ~5%), concatenated dictionary
// words at ~38% (dual wins by 35-43%).
const rootDenseThreshold = 215

// rootDense samples three 256-byte windows of input (head, middle, tail)
// and reports whether enough bytes leave the root state for the
// dual-cursor table scan to pay. Windows, not strided points, so
// periodic inputs do not alias against the sample pattern.
func (tr *Trie) rootDense(input []byte) bool {
	rootStop := &tr.rootStop
	n := len(input)
	k := 0
	for _, b := range input[:256] {
		k += int(rootStop[b])
	}
	// A dense input with a near-empty head window is pathological;
	// bail before sampling the other windows. Misclassification only
	// picks the single-cursor loop, never wrong results.
	if k < 8 {
		return false
	}
	mid, tail := n/2-128, n-256
	for _, w := range [2]int{mid, tail} {
		for _, b := range input[w : w+256] {
			k += int(rootStop[b])
		}
	}
	return k >= rootDenseThreshold
}

// matchSeq scans input sequentially into buf.
func (tr *Trie) matchSeq(input []byte, buf *matchBuf) {
	if tr.failTrans16 != nil && len(tr.rootStopBytes) == 1 {
		// Dual-scan only when the maxLen-1 bytes lane B re-scans are a
		// small fraction of a half, and the input is dense enough in
		// stop bytes that overlapping two transition chains beats the
		// single-cursor loop's inline root skip.
		if len(input) >= dualThreshold && int(tr.maxLen)*4 < len(input)/2 &&
			looksDense(input, tr.rootStopBytes[0]) {
			tr.matchDualStopByte16(input, buf)
			return
		}
		tr.matchStopByte16(input, buf)
		return
	}
	if len(tr.rootStopBytes) == 1 {
		tr.matchStopByte(input, buf)
	} else if tr.failTrans16 != nil {
		if len(input) >= dualTableMin && int(tr.maxLen)*4 < len(input)/2 && tr.rootDense(input) {
			tr.matchDualTable16(input, buf)
		} else {
			tr.matchTable16(input, buf)
		}
	} else {
		if len(input) >= dualTableMin && int(tr.maxLen)*4 < len(input)/2 && tr.rootDense(input) {
			if tr.failTransC != nil {
				tr.matchDualTableC(input, buf)
			} else {
				tr.matchDualTable32(input, buf)
			}
		} else {
			tr.matchTable(input, buf)
		}
	}
}

// scanRangeTableC is scanRangeTable32 on the class-compressed table.
func (tr *Trie) scanRangeTableC(input []byte, i, to int, s uint32, minEmit int, raw []uint64) []uint64 {
	ftBase := unsafe.Pointer(&tr.failTransC[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])
	classOf := &tr.classOf
	shift := tr.classShift

	for ; i < to; i++ {
		if s == rootState {
			if tr.rootStop[input[i]] == 0 {
				i = tr.skipRootTable(input, i)
			}
			if i >= to {
				return raw
			}
		}

		v := *(*uint32)(unsafe.Add(ftBase, (uintptr(s)<<shift+uintptr(classOf[input[i]]))<<2))
		s = v & stateMask
		if v&outputFlag != 0 && i >= minEmit {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				raw = append(raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				raw = append(raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
	return raw
}

// matchDualTableC is matchDualTable32 on the class-compressed table.
func (tr *Trie) matchDualTableC(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTransC[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])
	classOf := &tr.classOf
	shift := tr.classShift

	inputLen := len(input)
	mid := inputLen / 2
	startB := max(mid-int(tr.maxLen)+1, 0)

	sA, sB := rootState, rootState
	iA, iB := 0, startB
	rawA, rawB := buf.raw, buf.raw2

	for iA < mid && iB < inputLen {
		if sA == rootState && tr.rootStop[input[iA]] == 0 {
			iA = tr.skipRootTable(input, iA)
		} else {
			v := *(*uint32)(unsafe.Add(ftBase, (uintptr(sA)<<shift+uintptr(classOf[input[iA]]))<<2))
			sA = v & stateMask
			if v&outputFlag != 0 {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sA)<<3)); uint32(dp) != 0 {
					rawA = append(rawA, uint64(iA), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sA)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawA = append(rawA, uint64(iA), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iA++
		}

		if sB == rootState && tr.rootStop[input[iB]] == 0 {
			iB = tr.skipRootTable(input, iB)
		} else {
			v := *(*uint32)(unsafe.Add(ftBase, (uintptr(sB)<<shift+uintptr(classOf[input[iB]]))<<2))
			sB = v & stateMask
			if v&outputFlag != 0 && iB >= mid {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sB)<<3)); uint32(dp) != 0 {
					rawB = append(rawB, uint64(iB), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sB)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawB = append(rawB, uint64(iB), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iB++
		}
	}

	rawA = tr.scanRangeTableC(input, iA, mid, sA, 0, rawA)
	rawB = tr.scanRangeTableC(input, iB, inputLen, sB, mid, rawB)

	buf.raw = append(rawA, rawB...)
	buf.raw2 = rawB[:0]
}

// scanRangeTable16 runs the multi-stop-byte automaton over input[i:to),
// starting in state s, appending matches that end at or after minEmit to
// raw, which is returned. Positions are absolute into input. Loads use
// raw pointer arithmetic; see matchStopByte.
func (tr *Trie) scanRangeTable16(input []byte, i, to int, s uint32, minEmit int, raw []uint64) []uint64 {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	for ; i < to; i++ {
		if s == rootState {
			if tr.rootStop[input[i]] == 0 {
				i = tr.skipRootTable(input, i)
			}
			if i >= to {
				return raw
			}
		}

		v := uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
		s = v &^ (1 << 15)
		if v&(1<<15) != 0 && i >= minEmit {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				raw = append(raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				raw = append(raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
	return raw
}

// scanRangeTable32 is scanRangeTable16 on the full-width failTrans table.
func (tr *Trie) scanRangeTable32(input []byte, i, to int, s uint32, minEmit int, raw []uint64) []uint64 {
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	for ; i < to; i++ {
		if s == rootState {
			if tr.rootStop[input[i]] == 0 {
				i = tr.skipRootTable(input, i)
			}
			if i >= to {
				return raw
			}
		}

		v := *(*uint32)(unsafe.Add(ftBase, uintptr(s)<<10+uintptr(input[i])<<2))
		s = v & stateMask
		if v&outputFlag != 0 && i >= minEmit {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				raw = append(raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				raw = append(raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
	return raw
}

// matchDualTable16 is the multi-stop-byte analog of matchDualStopByte16:
// two independent cursors interleave over the two halves of input so
// their serial transition-load chains overlap. Lane B starts maxLen-1
// bytes before the midpoint and emits only matches ending at or after it
// (see matchParallel for why that overlap suffices), so rawA followed by
// rawB is exactly a sequential scan's output. Root gaps are skipped with
// the rootStop table since several byte values leave the root.
func (tr *Trie) matchDualTable16(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	inputLen := len(input)
	mid := inputLen / 2
	startB := max(mid-int(tr.maxLen)+1, 0)

	sA, sB := rootState, rootState
	iA, iB := 0, startB
	rawA, rawB := buf.raw, buf.raw2

	for iA < mid && iB < inputLen {
		// Lane A: one step — a whole root gap skip, or one transition.
		if sA == rootState && tr.rootStop[input[iA]] == 0 {
			iA = tr.skipRootTable(input, iA)
		} else {
			v := uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(sA)<<9+uintptr(input[iA])<<1)))
			sA = v &^ (1 << 15)
			if v&(1<<15) != 0 {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sA)<<3)); uint32(dp) != 0 {
					rawA = append(rawA, uint64(iA), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sA)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawA = append(rawA, uint64(iA), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iA++
		}

		// Lane B: same step shape.
		if sB == rootState && tr.rootStop[input[iB]] == 0 {
			iB = tr.skipRootTable(input, iB)
		} else {
			v := uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(sB)<<9+uintptr(input[iB])<<1)))
			sB = v &^ (1 << 15)
			if v&(1<<15) != 0 && iB >= mid {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sB)<<3)); uint32(dp) != 0 {
					rawB = append(rawB, uint64(iB), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sB)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawB = append(rawB, uint64(iB), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iB++
		}
	}

	// Finish whichever lane has input left.
	rawA = tr.scanRangeTable16(input, iA, mid, sA, 0, rawA)
	rawB = tr.scanRangeTable16(input, iB, inputLen, sB, mid, rawB)

	// Concatenate: lane A ends < mid, lane B ends >= mid, so order is
	// exactly the sequential scan's.
	buf.raw = append(rawA, rawB...)
	buf.raw2 = rawB[:0]
}

// matchDualTable32 is matchDualTable16 on the full-width failTrans table
// (automata with too many states for 15-bit ids).
func (tr *Trie) matchDualTable32(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	inputLen := len(input)
	mid := inputLen / 2
	startB := max(mid-int(tr.maxLen)+1, 0)

	sA, sB := rootState, rootState
	iA, iB := 0, startB
	rawA, rawB := buf.raw, buf.raw2

	for iA < mid && iB < inputLen {
		if sA == rootState && tr.rootStop[input[iA]] == 0 {
			iA = tr.skipRootTable(input, iA)
		} else {
			v := *(*uint32)(unsafe.Add(ftBase, uintptr(sA)<<10+uintptr(input[iA])<<2))
			sA = v & stateMask
			if v&outputFlag != 0 {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sA)<<3)); uint32(dp) != 0 {
					rawA = append(rawA, uint64(iA), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sA)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawA = append(rawA, uint64(iA), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iA++
		}

		if sB == rootState && tr.rootStop[input[iB]] == 0 {
			iB = tr.skipRootTable(input, iB)
		} else {
			v := *(*uint32)(unsafe.Add(ftBase, uintptr(sB)<<10+uintptr(input[iB])<<2))
			sB = v & stateMask
			if v&outputFlag != 0 && iB >= mid {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sB)<<3)); uint32(dp) != 0 {
					rawB = append(rawB, uint64(iB), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sB)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawB = append(rawB, uint64(iB), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iB++
		}
	}

	rawA = tr.scanRangeTable32(input, iA, mid, sA, 0, rawA)
	rawB = tr.scanRangeTable32(input, iB, inputLen, sB, mid, rawB)

	buf.raw = append(rawA, rawB...)
	buf.raw2 = rawB[:0]
}

// scanRange16 runs the stop-byte automaton over input[i:to), starting in
// state s, appending matches that end at or after minEmit to raw, which
// is returned. Positions are absolute into input; the SWAR skip may read
// (but not emit) past to. Loads use raw pointer arithmetic; see
// matchStopByte.
func (tr *Trie) scanRange16(input []byte, i, to int, s uint32, minEmit int, raw []uint64) []uint64 {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	c := tr.rootStopBytes[0]
	cc := uint64(c) * swarOnes
	stopE := uint32(tr.stopEntry16)

	inputLen := len(input)
	for ; i < to; i++ {
		if s == rootState && input[i] != c {
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
			if i >= to {
				return raw
			}
		}

		// At the root the cursor is on the stop byte (the skip above
		// guarantees input[i] == c), so the transition is the
		// precomputed constant; see matchStopByte16.
		v := stopE
		if s != rootState {
			v = uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
		}
		s = v &^ (1 << 15)
		if v&(1<<15) != 0 && i >= minEmit {
			if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(s)<<3)); uint32(dp) != 0 {
				raw = append(raw, uint64(i), dp)
			}
			for u := *(*uint32)(unsafe.Add(dlBase, uintptr(s)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
				raw = append(raw, uint64(i), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
			}
		}
	}
	return raw
}

// nextStop returns the position of the first occurrence of c at or after
// i, or len(input) if there is none. Short gaps resolve in a few SWAR
// words; long gaps fall through to the vectorized bytes.IndexByte, whose
// setup cost would dominate typical short gaps. cc must be c replicated
// into all eight lanes (uint64(c) * swarOnes).
func nextStop(input []byte, i int, c byte, cc uint64) int {
	inputLen := len(input)
	for k := 0; ; k++ {
		if i+8 > inputLen {
			for i < inputLen && input[i] != c {
				i++
			}
			return i
		}
		w := binary.LittleEndian.Uint64(input[i:]) ^ cc
		if m := (w - swarOnes) & ^w & swarHighs; m != 0 {
			return i + bits.TrailingZeros64(m)>>3
		}
		i += 8
		if k == 3 {
			j := bytes.IndexByte(input[i:], c)
			if j < 0 {
				return inputLen
			}
			return i + j
		}
	}
}

// matchDualStopByte16 scans the two halves of input with two independent
// automaton cursors interleaved in one loop. The state-transition load
// chain is the serial bottleneck of a single scan; two chains overlap
// their L1 latencies. Lane B starts maxLen-1 bytes before the midpoint
// (see matchParallel for why that overlap suffices) and emits only
// matches ending at or after the midpoint, so rawA followed by rawB is
// exactly a sequential scan's output. Loads use raw pointer arithmetic;
// see matchStopByte.
func (tr *Trie) matchDualStopByte16(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	c := tr.rootStopBytes[0]
	cc := uint64(c) * swarOnes

	inputLen := len(input)
	mid := inputLen / 2
	startB := max(mid-int(tr.maxLen)+1, 0)

	stopE := uint32(tr.stopEntry16)

	sA, sB := rootState, rootState
	iA, iB := 0, startB
	rawA, rawB := buf.raw, buf.raw2

	for iA < mid && iB < inputLen {
		// Lane A: one step — a whole root gap skip, or one transition.
		if sA == rootState && input[iA] != c {
			iA = nextStop(input, iA, c, cc)
		} else {
			v := stopE
			if sA != rootState {
				v = uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(sA)<<9+uintptr(input[iA])<<1)))
			}
			sA = v &^ (1 << 15)
			if v&(1<<15) != 0 {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sA)<<3)); uint32(dp) != 0 {
					rawA = append(rawA, uint64(iA), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sA)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawA = append(rawA, uint64(iA), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iA++
		}

		// Lane B: same step shape.
		if sB == rootState && input[iB] != c {
			iB = nextStop(input, iB, c, cc)
		} else {
			v := stopE
			if sB != rootState {
				v = uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(sB)<<9+uintptr(input[iB])<<1)))
			}
			sB = v &^ (1 << 15)
			if v&(1<<15) != 0 && iB >= mid {
				if dp := *(*uint64)(unsafe.Add(dpBase, uintptr(sB)<<3)); uint32(dp) != 0 {
					rawB = append(rawB, uint64(iB), dp)
				}
				for u := *(*uint32)(unsafe.Add(dlBase, uintptr(sB)<<2)); u != nilState; u = *(*uint32)(unsafe.Add(dlBase, uintptr(u)<<2)) {
					rawB = append(rawB, uint64(iB), *(*uint64)(unsafe.Add(dpBase, uintptr(u)<<3)))
				}
			}
			iB++
		}
	}

	// Finish whichever lane has input left.
	rawA = tr.scanRange16(input, iA, mid, sA, 0, rawA)
	rawB = tr.scanRange16(input, iB, inputLen, sB, mid, rawB)

	// Concatenate: lane A ends < mid, lane B ends >= mid, so order is
	// exactly the sequential scan's.
	buf.raw = append(rawA, rawB...)
	buf.raw2 = rawB[:0]
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
			for idx := drop; idx < len(raw); idx += 2 {
				raw[idx] += uint64(scanStart)
			}
			if drop > 0 {
				// Shift the kept entries down instead of reslicing
				// forward: assigning raw[drop:] back would move the
				// pooled slice's base, permanently leaking the dropped
				// prefix's capacity from the buffer's future lives.
				buf.raw = raw[:copy(raw, raw[drop:])]
			}
		}(start, end, buf)
	}

	var main *matchBuf
	if p > 0 {
		main = tr.bufPool.Get().(*matchBuf)
		main.reset()
		tr.matchSeq(input[:min(chunk, len(input))], main)
		wg.Wait()

		// Materialize straight from the per-worker buffers instead of
		// first copying every raw pair into one slice: the copy and the
		// single-threaded expansion were the serial tail that capped
		// the parallel speedup.
		total := len(main.raw) / 2
		for k := 1; k < p; k++ {
			total += len(bufs[k].raw) / 2
		}
		if total == 0 {
			for k := 1; k < p; k++ {
				tr.putWorkerBuf(bufs[k])
			}
			tr.bufPool.Put(main)
			return nil
		}
		main.sizeArena(total)

		// Small results are not worth another round of goroutine
		// wakeups; expand serially but still without the merge copy.
		const parallelMaterialize = 4096
		if total >= parallelMaterialize {
			var mg sync.WaitGroup
			off := 0
			for k := 0; k < p; k++ {
				raw := main.raw
				if k > 0 {
					raw = bufs[k].raw
				}
				mg.Add(1)
				go func(raw []uint64, off int) {
					defer mg.Done()
					main.materializeSegment(input, raw, off)
				}(raw, off)
				off += len(raw) / 2
			}
			mg.Wait()
		} else {
			off := main.materializeSegment(input, main.raw, 0)
			for k := 1; k < p; k++ {
				off = main.materializeSegment(input, bufs[k].raw, off)
			}
		}
		for k := 1; k < p; k++ {
			tr.putWorkerBuf(bufs[k])
		}
		main.ptrs[0].buf = main
		return main.ptrs
	}

	main = tr.bufPool.Get().(*matchBuf)
	main.reset()
	tr.matchSeq(input, main)

	if len(main.raw) == 0 {
		tr.bufPool.Put(main)
		return nil
	}
	main.materialize(input)
	main.ptrs[0].buf = main
	return main.ptrs
}

// putWorkerBuf returns a worker's scratch buffer to the pool. Worker
// buffers never materialize, so an arena filled in a previous life of
// this pooled buffer may still hold Match.match slices into an old
// input; clear it so the buffer doesn't retain that input while idle in
// the pool. (sizeArena keeps the arena tail beyond len zeroed, so
// clearing len and truncating leaves no stale entries.)
func (tr *Trie) putWorkerBuf(wb *matchBuf) {
	wb.raw = wb.raw[:0]
	clear(wb.arena)
	wb.arena = wb.arena[:0]
	tr.bufPool.Put(wb)
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

	for i := 0; i < inputLen; i++ {
		if s == rootState && tr.rootStop[input[i]] == 0 {
			i = tr.skipRootTable(input, i)
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

// matchTable16 is matchTable on the half-width failTrans16 table: same
// rootStop-table skip, but the transition loads touch 512B rows instead
// of 1KB, halving the cache footprint of the serial dependency chain.
// See matchStopByte for why the loads use raw pointer arithmetic.
func (tr *Trie) matchTable16(input []byte, buf *matchBuf) {
	ftBase := unsafe.Pointer(&tr.failTrans16[0])
	dpBase := unsafe.Pointer(&tr.dictPat[0])
	dlBase := unsafe.Pointer(&tr.dictLink[0])

	s := rootState

	inputLen := len(input)

	for i := 0; i < inputLen; i++ {
		if s == rootState && tr.rootStop[input[i]] == 0 {
			i = tr.skipRootTable(input, i)
			if i == inputLen {
				return
			}
		}

		v := uint32(*(*uint16)(unsafe.Add(ftBase, uintptr(s)<<9+uintptr(input[i])<<1)))
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
