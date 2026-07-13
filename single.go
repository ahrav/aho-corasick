package ahocorasick

// Single-pattern fast path. A one-pattern trie needs no automaton: the
// scan entry points dispatch to the substring searchers here, built on
// the two fastest primitives available in pure Go on this hot path:
//
//   - rare-byte search (sparse inputs): vectorized bytes.IndexByte over
//     the pattern's rarest byte generates candidate starts; a one-byte
//     probe of the second-rarest byte rejects almost all false
//     candidates before the full comparison. Runs near memory bandwidth
//     between candidates but pays a call-and-restart cost per candidate.
//
//   - SWAR pair search (dense inputs): a word scan tests the two rarest
//     bytes at their pattern offsets simultaneously, eight candidate
//     starts per iteration, with no per-candidate restart cost.
//     Throughput is flat (~3GB/s) regardless of candidate density.
//
// Each scan samples the rare byte's density and picks: rare-byte wins
// when candidates are sparse, the pair scan when the "rare" byte is
// locally common (e.g. prose, where every pattern byte is a frequent
// letter).
//
// bytes.Index is deliberately not used: on arm64 it generates candidates
// from the first byte only, so a common-first-byte needle over prose
// degrades it to ~2GB/s.

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

// byteRank estimates how common each byte value is in text-like corpora
// (higher = more common). Only the relative order matters: the searchers
// pick the pattern's two lowest-ranked bytes as candidate filters. A
// coarse heuristic in the spirit of the frequency tables behind
// memchr/memmem prefilters.
var byteRank = func() (r [256]uint8) {
	for i := range r {
		r[i] = 50 // binary/control bytes: rare
	}
	for b := 0x80; b <= 0xBF; b++ {
		r[b] = 100 // UTF-8 continuation bytes
	}
	for b := 0xC0; b <= 0xDF; b++ {
		r[b] = 95 // UTF-8 lead bytes (Latin scripts)
	}
	for b := '0'; b <= '9'; b++ {
		r[b] = 80
	}
	for b := 'A'; b <= 'Z'; b++ {
		r[b] = 85
	}
	for _, c := range []byte(`.,;:'"-()`) {
		r[c] = 90
	}
	for _, c := range []byte(" \n\r\t") {
		r[c] = 250
	}
	// Descending frequency ladder for lowercase letters.
	for i, c := range []byte("etaoinsrhldcumfgpwybvkxjqz") {
		r[c] = 245 - uint8(i)*5
	}
	return
}()

// buildSinglePattern detects the one-pattern trie shape and extracts its
// pattern from the transition table, enabling the fast scan paths
// (matchSingle, walkSingle). Derived like the other acceleration tables:
// it reads only failTrans/dict/dictLink/dictPat, so Build and Decode both
// reach the same result without any wire-format change.
//
// A single pattern of length n compiles to exactly n+2 states (nil, root,
// one chain state per byte) numbered sequentially by the BFS renumbering,
// with one emitting state (the chain end) and no dictLink entries. The
// pattern bytes are recovered by walking the chain: only the true child
// edge of state s can reach state s+1, because every other entry in the
// row was copied from a strictly shallower fail state whose targets are
// all shallower than s+1.
//
// Must run after buildDictPat (reads maxLen and dictPat).
func (tr *Trie) buildSinglePattern() {
	tr.single = nil
	n := int(tr.maxLen)
	if n < 1 || len(tr.failTrans) != n+2 {
		return
	}
	final := uint32(n) + 1
	for s := range tr.dict {
		if tr.dict[s] != 0 && uint32(s) != final {
			return
		}
		if tr.dictLink[s] != nilState {
			return
		}
	}
	if tr.dict[final] != uint32(n) {
		return
	}
	pat := make([]byte, 0, n)
	for s := rootState; s <= uint32(n); s++ {
		row := &tr.failTrans[s]
		found := -1
		for b := range 256 {
			if row[b]&stateMask == s+1 {
				if found >= 0 {
					return
				}
				found = b
			}
		}
		if found < 0 {
			return
		}
		pat = append(pat, byte(found))
	}
	tr.single = pat
	tr.singleDP = tr.dictPat[final]

	// KMP prefix function: the minimal distance between overlapping
	// occurrence starts is the period n - lps[n-1]. Advancing by it
	// after each hit finds every overlapping occurrence while keeping
	// the rescan linear on periodic patterns.
	lps := make([]int, n)
	for i, k := 1, 0; i < n; i++ {
		for k > 0 && pat[i] != pat[k] {
			k = lps[k-1]
		}
		if pat[i] == pat[k] {
			k++
		}
		lps[i] = k
	}
	tr.singleSkip = n - lps[n-1]

	// Candidate filter offsets: the two lowest-ranked (rarest) pattern
	// bytes at distinct offsets. Rank ties prefer the offset farther
	// from the first pick — adjacent text bytes correlate (digraphs),
	// so separation buys discrimination.
	o1 := 0
	for k := 1; k < n; k++ {
		if byteRank[pat[k]] < byteRank[pat[o1]] {
			o1 = k
		}
	}
	o2 := o1
	for k := range n {
		if k == o1 {
			continue
		}
		if o2 == o1 || byteRank[pat[k]] < byteRank[pat[o2]] ||
			(byteRank[pat[k]] == byteRank[pat[o2]] && absInt(k-o1) > absInt(o2-o1)) {
			o2 = k
		}
	}
	tr.singleO1, tr.singleO2 = o1, o2
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// singleVerify reports whether the pattern p occurs at input[pos:]. The
// caller guarantees pos+len(p) <= len(input). Word-wide compares (with an
// overlapped tail load) avoid bytes.Equal's call overhead on the
// candidate-verification path.
func singleVerify(input []byte, pos int, p []byte) bool {
	n := len(p)
	if n < 8 {
		for k := 0; k < n; k++ {
			if input[pos+k] != p[k] {
				return false
			}
		}
		return true
	}
	k := 0
	for ; k+8 <= n; k += 8 {
		if binary.LittleEndian.Uint64(input[pos+k:]) != binary.LittleEndian.Uint64(p[k:]) {
			return false
		}
	}
	// Overlapped final word re-compares up to 7 already-verified bytes.
	return k == n ||
		binary.LittleEndian.Uint64(input[pos+n-8:]) == binary.LittleEndian.Uint64(p[n-8:])
}

// singlePairDense reports whether rare-byte candidate density is high
// enough that the flat-throughput SWAR pair scan beats IndexByte
// candidate generation. Break-even (measured, Neoverse V1): rare-byte
// search costs ~20ns/KB of scan plus ~13ns per candidate; the pair scan
// ~345ns/KB flat — so rare-byte loses above ~26 candidates per KB (~2.6%
// density). Three 1KB windows (head, middle, tail) sample the density;
// below 8KB the stakes are too small to pay for sampling.
func (tr *Trie) singlePairDense(input []byte) bool {
	if len(input) < 8<<10 {
		return false
	}
	c := tr.single[tr.singleO1 : tr.singleO1+1]
	n := len(input)
	k := bytes.Count(input[:1024], c)
	k += bytes.Count(input[n/2:n/2+1024], c)
	k += bytes.Count(input[n-1024:], c)
	return k >= 78 // ~2.6% of the 3KB sampled
}

// matchSingle appends every (overlapping) occurrence of the one-pattern
// trie's pattern. Emission order (by end position) matches the automaton
// paths.
func (tr *Trie) matchSingle(input []byte, buf *matchBuf) {
	if len(tr.single) == 1 {
		c := tr.single[0]
		dp := tr.singleDP
		for i := 0; ; i++ {
			j := bytes.IndexByte(input[i:], c)
			if j < 0 {
				return
			}
			i += j
			buf.raw = append(buf.raw, uint64(i), dp)
		}
	}
	if tr.singlePairDense(input) {
		tr.singlePairMatch(input, buf)
		return
	}
	tr.singleRareMatch(input, buf)
}

// walkSingle is matchSingle for the callback API.
func (tr *Trie) walkSingle(input []byte, fn WalkFn) {
	if len(tr.single) == 1 {
		c := tr.single[0]
		dp := tr.singleDP
		for i := 0; ; i++ {
			j := bytes.IndexByte(input[i:], c)
			if j < 0 {
				return
			}
			i += j
			if !fn(uint32(i), uint32(dp), uint32(dp>>32)) {
				return
			}
		}
	}
	if tr.singlePairDense(input) {
		tr.singlePairWalk(input, fn)
		return
	}
	tr.singleRareWalk(input, fn)
}

// singleRareMatch: IndexByte over the rarest pattern byte generates
// candidates; a second-byte probe rejects false ones before the full
// comparison. Requires len(tr.single) >= 2.
func (tr *Trie) singleRareMatch(input []byte, buf *matchBuf) {
	p := tr.single
	n := len(p)
	o1, o2 := tr.singleO1, tr.singleO2
	c1, c2 := p[o1], p[o2]
	dp := tr.singleDP
	for i := 0; i+n <= len(input); {
		j := bytes.IndexByte(input[i+o1:], c1)
		if j < 0 {
			return
		}
		pos := i + j // the found byte sits at candidate offset o1
		if pos+n > len(input) {
			return
		}
		if input[pos+o2] == c2 && singleVerify(input, pos, p) {
			buf.raw = append(buf.raw, uint64(pos+n-1), dp)
			i = pos + tr.singleSkip
		} else {
			i = pos + 1
		}
	}
}

// singleRareWalk is singleRareMatch for the callback API.
func (tr *Trie) singleRareWalk(input []byte, fn WalkFn) {
	p := tr.single
	n := len(p)
	o1, o2 := tr.singleO1, tr.singleO2
	c1, c2 := p[o1], p[o2]
	dp := tr.singleDP
	for i := 0; i+n <= len(input); {
		j := bytes.IndexByte(input[i+o1:], c1)
		if j < 0 {
			return
		}
		pos := i + j
		if pos+n > len(input) {
			return
		}
		if input[pos+o2] == c2 && singleVerify(input, pos, p) {
			if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
				return
			}
			i = pos + tr.singleSkip
		} else {
			i = pos + 1
		}
	}
}

// singlePairMatch: SWAR scan testing the two rarest pattern bytes at
// their offsets simultaneously, eight candidate starts per iteration.
// Requires len(tr.single) >= 2.
func (tr *Trie) singlePairMatch(input []byte, buf *matchBuf) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	ca := swarOnes * uint64(p[oa])
	cb := swarOnes * uint64(p[ob])
	dp := tr.singleDP

	limit := len(input) - n // last valid start
	next := 0               // next start allowed by the KMP period
	i := 0
	// Two independent 8-byte lanes per iteration: the lanes share no
	// data, so their load-xor-test chains overlap and the common no-hit
	// case takes one branch per 16 bytes. Lane 0 hits precede lane 1
	// hits, so emission order stays increasing. Each lane pair loads
	// through a 16-byte reslice of provably constant length, so the
	// compiler keeps one bounds check per slice and elides the rest;
	// binary.LittleEndian keeps the byte order portable (it compiles
	// to a plain load on little-endian targets).
	for ; i+ob+16 <= len(input); i += 16 {
		wA := input[i+oa : i+oa+16]
		wB := input[i+ob : i+ob+16]
		wa0 := binary.LittleEndian.Uint64(wA)
		wb0 := binary.LittleEndian.Uint64(wB)
		wa1 := binary.LittleEndian.Uint64(wA[8:])
		wb1 := binary.LittleEndian.Uint64(wB[8:])
		m0 := swarZero(wa0^ca) & swarZero(wb0^cb)
		m1 := swarZero(wa1^ca) & swarZero(wb1^cb)
		if m0|m1 == 0 {
			continue
		}
		for m0 != 0 {
			pos := i + bits.TrailingZeros64(m0)>>3
			m0 &= m0 - 1
			if pos < next || pos > limit {
				continue
			}
			if singleVerify(input, pos, p) {
				buf.raw = append(buf.raw, uint64(pos+n-1), dp)
				next = pos + tr.singleSkip
			}
		}
		for m1 != 0 {
			pos := i + 8 + bits.TrailingZeros64(m1)>>3
			m1 &= m1 - 1
			if pos < next || pos > limit {
				continue
			}
			if singleVerify(input, pos, p) {
				buf.raw = append(buf.raw, uint64(pos+n-1), dp)
				next = pos + tr.singleSkip
			}
		}
	}
	for pos := max(i, next); pos <= limit; pos++ {
		if input[pos+oa] == p[oa] && input[pos+ob] == p[ob] && singleVerify(input, pos, p) {
			buf.raw = append(buf.raw, uint64(pos+n-1), dp)
			pos += tr.singleSkip - 1 // the loop increment adds the 1 back
		}
	}
}

// singlePairWalk is singlePairMatch for the callback API.
func (tr *Trie) singlePairWalk(input []byte, fn WalkFn) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	ca := swarOnes * uint64(p[oa])
	cb := swarOnes * uint64(p[ob])
	dp := tr.singleDP

	limit := len(input) - n
	next := 0
	i := 0
	// Same two-lane structure as singlePairMatch.
	for ; i+ob+16 <= len(input); i += 16 {
		wA := input[i+oa : i+oa+16]
		wB := input[i+ob : i+ob+16]
		wa0 := binary.LittleEndian.Uint64(wA)
		wb0 := binary.LittleEndian.Uint64(wB)
		wa1 := binary.LittleEndian.Uint64(wA[8:])
		wb1 := binary.LittleEndian.Uint64(wB[8:])
		m0 := swarZero(wa0^ca) & swarZero(wb0^cb)
		m1 := swarZero(wa1^ca) & swarZero(wb1^cb)
		if m0|m1 == 0 {
			continue
		}
		for m0 != 0 {
			pos := i + bits.TrailingZeros64(m0)>>3
			m0 &= m0 - 1
			if pos < next || pos > limit {
				continue
			}
			if singleVerify(input, pos, p) {
				if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
					return
				}
				next = pos + tr.singleSkip
			}
		}
		for m1 != 0 {
			pos := i + 8 + bits.TrailingZeros64(m1)>>3
			m1 &= m1 - 1
			if pos < next || pos > limit {
				continue
			}
			if singleVerify(input, pos, p) {
				if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
					return
				}
				next = pos + tr.singleSkip
			}
		}
	}
	for pos := max(i, next); pos <= limit; pos++ {
		if input[pos+oa] == p[oa] && input[pos+ob] == p[ob] && singleVerify(input, pos, p) {
			if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
				return
			}
			pos += tr.singleSkip - 1
		}
	}
}

// swarZero returns a mask with bit 7 of each byte set iff that byte of w
// is zero.
func swarZero(w uint64) uint64 {
	return (w - swarOnes) &^ w & swarHighs
}
