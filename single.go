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
// Each scan picks by the rare byte's density: rare-byte wins when
// candidates are sparse, the pair scan when the "rare" byte is locally
// common (e.g. prose, where every pattern byte is a frequent letter).
// The match path samples density up front (it scans everything anyway);
// the callback walk decides inline from bytes already scanned, so an
// early-terminating caller never pays for reads past its last match.
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

	// The checks above prove the shape (state count, outputs, one chain
	// edge per state) but not the remaining row entries. Build always
	// produces the canonical KMP automaton, but Decode accepts any
	// in-range transition table, and a noncanonical one that passes the
	// shape checks has different generic semantics than the recovered
	// pattern (e.g. a final state whose row drops back to the root
	// instead of re-entering the chain suppresses the second of two
	// overlapping occurrences). Verify every remaining entry against
	// the automaton the pattern implies, or leave the fast path off.
	//
	// Canonical rows, validated in state order so each state's fail row
	// (depth lps[s-2], a state id strictly below s) is already known
	// canonical when referenced: the root sends non-pattern bytes to
	// itself, and every deeper state copies its fail state's row except
	// at its chain byte.
	for b := range 256 {
		want := rootState
		if byte(b) == pat[0] {
			want = rootState + 1
		}
		if tr.failTrans[rootState][b]&stateMask != want {
			return
		}
	}
	for s := rootState + 1; s <= final; s++ {
		row := &tr.failTrans[s]
		failRow := &tr.failTrans[lps[s-2]+1]
		d := int(s) - 1 // pattern bytes matched entering s
		for b := range 256 {
			want := failRow[b] & stateMask
			if d < n && byte(b) == pat[d] {
				want = s + 1
			}
			if row[b]&stateMask != want {
				return
			}
		}
	}

	tr.single = pat
	tr.singleDP = tr.dictPat[final]
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

// singleVerifyCarry is singleVerify with a KMP-period carry: when the
// previous confirmed match lies d < len(p) bytes back at a multiple of
// the period (singleSkip), that match already proves the candidate's
// first len(p)-d bytes — input[pos:prev+len(p)] equals p[d:] by the
// previous match, and p[d:] equals p[:len(p)-d] because a multiple of
// the period is itself a period — so only the last d bytes need
// comparing. Without the carry, a periodic pattern over match-dense
// input (m repeated a's over n repeated a's) re-verifies all m bytes at
// each of the n overlapping occurrences, degrading the O(n) automaton
// scan this path replaces to O(n*m); with it those tail compares sum to
// O(n). prev < 0 means no previous match.
func singleVerifyCarry(input []byte, pos, prev, skip int, p []byte) bool {
	if d := pos - prev; prev >= 0 && d < len(p) && d%skip == 0 {
		known := len(p) - d
		return singleVerify(input, pos+known, p[known:])
	}
	return singleVerify(input, pos, p)
}

// singlePairDense reports whether rare-byte candidate density is high
// enough that the flat-throughput SWAR pair scan beats IndexByte
// candidate generation. Break-even (measured, Neoverse V1): rare-byte
// search costs ~20ns/KB of scan plus ~13ns per candidate; the pair scan
// ~345ns/KB flat — so rare-byte loses above ~26 candidates per KB (~2.6%
// density). Three 1KB windows (head, middle, tail) sample the density;
// below 8KB the stakes are too small to pay for sampling.
//
// Only the match path uses this up-front sample: it scans the whole
// input regardless, so touching the middle and tail windows early costs
// nothing extra. The callback walk must stay lazy (a Walk callback may
// stop at the first match — MatchFirst — so nothing may be read ahead
// of the scan); singleRareWalk instead accumulates the same density
// signal inline over the bytes already covered and switches mid-scan.
func (tr *Trie) singlePairDense(input []byte) bool {
	if len(input) < 8<<10 {
		return false
	}
	c := tr.single[tr.singleO1 : tr.singleO1+1]
	k := 0
	for _, w := range sampleWindows(input) {
		k += bytes.Count(w, c)
	}
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
	if hasPairKernel {
		tr.singleKernelMatch(input, buf)
		return
	}
	if tr.singlePairDense(input) {
		tr.singlePairMatch(input, buf)
		return
	}
	tr.singleRareMatch(input, buf)
}

// walkSingle is matchSingle for the callback API. Unlike matchSingle it
// never samples density up front: the callback may stop the walk at the
// first match (MatchFirst does), and an up-front head/middle/tail
// sample would touch distant pages of a large cold input before a match
// at offset zero could be delivered — the same rule the automaton walk
// paths follow (see walkTable). singleRareWalk measures candidate
// density inline over bytes it has already scanned and hands off to the
// pair scan when the rare byte turns out to be locally common. The
// kernel walk observes the same rule: it scans strictly forward, block
// by block, and hands dense stretches to the pair scan in bounded
// spans, taking back over when the candidate stream turns sparse again.
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
	if hasPairKernel {
		tr.singleKernelWalk(input, fn)
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
	skip := tr.singleSkip
	prev := -1 // last confirmed match start, for the period carry
	for i := 0; i+n <= len(input); {
		j := bytes.IndexByte(input[i+o1:], c1)
		if j < 0 {
			return
		}
		pos := i + j // the found byte sits at candidate offset o1
		if pos+n > len(input) {
			return
		}
		if input[pos+o2] == c2 && singleVerifyCarry(input, pos, prev, skip, p) {
			buf.raw = append(buf.raw, uint64(pos+n-1), dp)
			prev = pos
			i = pos + skip
		} else {
			i = pos + 1
		}
	}
}

// Inline switch thresholds for the callback walk (singleRareWalk): the
// same ~2.6% break-even singlePairDense encodes, but accumulated over
// the bytes the scan has already covered instead of sampled windows, so
// the walk never reads ahead of itself. Switch once the candidate count
// carries the same evidence mass as the eager sampler (78 hits) while
// still outpacing one candidate per singleSwitchSpan bytes.
const (
	singleSwitchMinCands = 78
	singleSwitchSpan     = 38 // ~1/2.6%
)

// pairPhaseSpan is the span of one SWAR phase in the kernel searchers'
// two-way handoff (singleKernelMatch): after a dense window hands over,
// the SWAR scan runs one span at a time and control returns to the
// kernel as soon as a span carries at most one candidate per 64 bytes —
// the same break-even density the handoff itself encodes.
const pairPhaseSpan = 4096

// singleRareWalk is singleRareMatch for the callback API, with the lazy
// density switch described at walkSingle: when the rare byte proves
// locally common, hand the remainder of the input to the pair scan.
func (tr *Trie) singleRareWalk(input []byte, fn WalkFn) {
	p := tr.single
	n := len(p)
	o1, o2 := tr.singleO1, tr.singleO2
	c1, c2 := p[o1], p[o2]
	dp := tr.singleDP
	skip := tr.singleSkip
	prev := -1
	cands := 0
	for i := 0; i+n <= len(input); {
		j := bytes.IndexByte(input[i+o1:], c1)
		if j < 0 {
			return
		}
		pos := i + j
		if pos+n > len(input) {
			return
		}
		cands++
		if cands >= singleSwitchMinCands && cands*singleSwitchSpan > pos {
			// Candidates are dense: the pair scan wins from here on.
			// It resumes at this (unprocessed) candidate, so nothing
			// is skipped or emitted twice.
			tr.singlePairWalkFrom(input, pos, fn)
			return
		}
		if input[pos+o2] == c2 && singleVerifyCarry(input, pos, prev, skip, p) {
			if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
				return
			}
			prev = pos
			i = pos + skip
		} else {
			i = pos + 1
		}
	}
}

// singlePairMatch: SWAR scan testing the two rarest pattern bytes at
// their offsets simultaneously, eight candidate starts per iteration.
// Requires len(tr.single) >= 2.
func (tr *Trie) singlePairMatch(input []byte, buf *matchBuf) {
	tr.singlePairMatchFrom(input, 0, buf)
}

// singlePairMatchFrom is singlePairMatch starting at position from; the
// kernel path hands over here when the candidate stream turns out dense.
func (tr *Trie) singlePairMatchFrom(input []byte, from int, buf *matchBuf) {
	tr.singlePairMatchRange(input, from, len(input)-len(tr.single)+1, buf)
}

// singlePairMatchRange scans candidate starts in [from, end) with the
// SWAR pair loop; end <= limit+1 always. When end > limit the scan is
// complete (including the scalar tail); otherwise the caller resumes at
// the returned position — candidate starts before it were handled or
// proven impossible. The second return value counts SWAR lane hits
// observed (borrow false positives included): a density signal for the
// kernel's phase policy, not an exact candidate count.
func (tr *Trie) singlePairMatchRange(input []byte, from, end int, buf *matchBuf) (int, int) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	ca := swarOnes * uint64(p[oa])
	cb := swarOnes * uint64(p[ob])
	dp := tr.singleDP
	skip := tr.singleSkip

	limit := len(input) - n // last valid start
	next := from            // next start allowed by the KMP period
	prev := -1              // last confirmed match start, for the period carry
	cands := 0
	i := from
	// Two independent 8-byte lanes per iteration: the lanes share no
	// data, so their load-xor-test chains overlap and the common no-hit
	// case takes one branch per 16 bytes. Lane 0 hits precede lane 1
	// hits, so emission order stays increasing. Each lane pair loads
	// through a 16-byte reslice of provably constant length, so the
	// compiler keeps one bounds check per slice and elides the rest;
	// binary.LittleEndian keeps the byte order portable (it compiles
	// to a plain load on little-endian targets). The intersected
	// swarZero masks can carry borrow false positives (see swarZero);
	// singleVerifyCarry, not the mask, is the correctness boundary.
	// The lane loop is bounded by the last valid start (i+15 <= limit,
	// via end <= limit+1), not by len(input): a long pattern with early
	// filter offsets would otherwise enumerate dense candidates past
	// limit only to discard them. That bound also proves the loads in
	// range — i+ob+15 <= limit+ob = len(input)-n+ob <= len(input)-1
	// since ob <= n-1. Candidate counting lives only in the hit
	// branches, so the no-hit fast path pays nothing for it.
	for ; i+16 <= end; i += 16 {
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
			cands++
			if pos < next || pos > limit {
				continue
			}
			if singleVerifyCarry(input, pos, prev, skip, p) {
				buf.raw = append(buf.raw, uint64(pos+n-1), dp)
				prev = pos
				next = pos + skip
			}
		}
		for m1 != 0 {
			pos := i + 8 + bits.TrailingZeros64(m1)>>3
			m1 &= m1 - 1
			cands++
			if pos < next || pos > limit {
				continue
			}
			if singleVerifyCarry(input, pos, prev, skip, p) {
				buf.raw = append(buf.raw, uint64(pos+n-1), dp)
				prev = pos
				next = pos + skip
			}
		}
	}
	if end <= limit {
		// Interior range: positions from i on are unprocessed; the
		// caller resumes there (next carries the period constraint).
		return max(i, next), cands
	}
	for pos := max(i, next); pos <= limit; pos++ {
		if input[pos+oa] == p[oa] && input[pos+ob] == p[ob] &&
			singleVerifyCarry(input, pos, prev, skip, p) {
			buf.raw = append(buf.raw, uint64(pos+n-1), dp)
			prev = pos
			pos += skip - 1 // the loop increment adds the 1 back
		}
	}
	return limit + 1, cands
}

// singlePairWalk is singlePairMatch for the callback API.
func (tr *Trie) singlePairWalk(input []byte, fn WalkFn) {
	tr.singlePairWalkFrom(input, 0, fn)
}

// singlePairWalkFrom is singlePairWalk starting at candidate position
// start, so singleRareWalk's mid-scan density switch and the kernel
// walk's dense-stream bailout can hand off the unscanned remainder.
// Candidates before start were already handled (or proven impossible)
// by the caller.
func (tr *Trie) singlePairWalkFrom(input []byte, start int, fn WalkFn) {
	tr.singlePairWalkRange(input, start, len(input)-len(tr.single)+1, fn)
}

// singlePairWalkRange is singlePairMatchRange for the callback API. The
// third return value reports whether the callback stopped the walk, in
// which case the caller must stop too.
func (tr *Trie) singlePairWalkRange(input []byte, from, end int, fn WalkFn) (int, int, bool) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	ca := swarOnes * uint64(p[oa])
	cb := swarOnes * uint64(p[ob])
	dp := tr.singleDP
	skip := tr.singleSkip

	limit := len(input) - n
	next := from
	prev := -1
	cands := 0
	i := from
	// Same two-lane structure (and lane-loop bound) as
	// singlePairMatchRange.
	for ; i+16 <= end; i += 16 {
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
			cands++
			if pos < next || pos > limit {
				continue
			}
			if singleVerifyCarry(input, pos, prev, skip, p) {
				if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
					return pos, cands, true
				}
				prev = pos
				next = pos + skip
			}
		}
		for m1 != 0 {
			pos := i + 8 + bits.TrailingZeros64(m1)>>3
			m1 &= m1 - 1
			cands++
			if pos < next || pos > limit {
				continue
			}
			if singleVerifyCarry(input, pos, prev, skip, p) {
				if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
					return pos, cands, true
				}
				prev = pos
				next = pos + skip
			}
		}
	}
	if end <= limit {
		return max(i, next), cands, false
	}
	for pos := max(i, next); pos <= limit; pos++ {
		if input[pos+oa] == p[oa] && input[pos+ob] == p[ob] &&
			singleVerifyCarry(input, pos, prev, skip, p) {
			if !fn(uint32(pos+n-1), uint32(dp), uint32(dp>>32)) {
				return pos, cands, true
			}
			prev = pos
			pos += skip - 1
		}
	}
	return limit + 1, cands, false
}

// swarZero returns a mask with bit 7 of each byte set for every zero
// byte of w. The mask is a superset, not exact: the subtraction borrows
// across lanes, so a 0x01 lane sitting above a zero lane (or above a
// chain of 0x01 lanes reaching one) is falsely flagged — a borrow can
// only start at a true zero (v-1 underflows only for v == 0), and lanes
// >= 0x02 either absorb it or have bit 7 masked off by &^ w. Every true
// zero lane is always flagged, so there are no false negatives, and the
// lowest set bit is always a true zero. The pair scans intersect two of
// these masks as a candidate *filter*: a stray lane costs one
// singleVerifyCarry call that exits on its first word, cheaper than
// paying for an exact per-lane mask on the no-hit fast path that
// dominates the scan.
func swarZero(w uint64) uint64 {
	return (w - swarOnes) &^ w & swarHighs
}

// singleKernelMatch finds occurrences with the vector pair kernel: one
// call scans whole 32-position blocks for candidates (both rare bytes at
// their offsets), so neither the candidate-restart cost of the rare-byte
// strategy nor the flat scalar throughput of the SWAR pair scan applies.
// Only called when hasPairKernel.
//
// Position mapping: pattern start s puts the low-offset filter byte at
// input[s+oa], so each call scans input[s+oa:] and its index j offsets
// from s. Read contract, per call: with m = limit-s+1 candidate starts
// remaining, the kernel reads at most input[s+oa : s+oa+m&^31+(ob-oa)]
// (see the guard-page test). Since m&^31 <= m, the exclusive end is at
// most s+oa+(limit-s+1)+(ob-oa) = limit+ob+1 <= len(input), because
// ob <= n-1 and limit = len(input)-n.
func (tr *Trie) singleKernelMatch(input []byte, buf *matchBuf) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	a, b := p[oa], p[ob]
	d := ob - oa
	dp := tr.singleDP
	limit := len(input) - n // last valid start
	s := 0
	candidates := 0
	anchor := 0 // window start for the density measurement
	for s <= limit {
		m := limit - s + 1 // candidate starts remaining
		j := indexPair2(input[s+oa:], m, a, b, d)
		if j < 0 {
			// Scalar tail over the last partial block.
			for t := s + m&^31; t <= limit; t++ {
				if input[t+oa] == a && input[t+ob] == b && singleVerify(input, t, p) {
					buf.raw = append(buf.raw, uint64(t+n-1), dp)
					t += tr.singleSkip - 1
				}
			}
			return
		}
		cand := s + j
		if singleVerify(input, cand, p) {
			buf.raw = append(buf.raw, uint64(cand+n-1), dp)
			s = cand + tr.singleSkip
		} else {
			s = cand + 1
		}
		// Adaptive phase policy: each kernel return costs a call
		// restart (~17ns), so a pair-dense stream (>1 candidate per
		// 64 bytes) is better served by the SWAR scan with inline
		// candidate handling. Density is measured over a tumbling
		// window — 32 returned candidates against the bytes they
		// span — and evaluated only after the kernel has returned a
		// candidate, so a candidate-free suffix is always skipped at
		// kernel speed, never handed to the scalar scan. When a
		// window proves dense the SWAR scan takes over span by span
		// and hands back as soon as one span turns sparse, so
		// neither a dense island nor a sparse tail commits the
		// remainder of the input to the wrong strategy. Bounded
		// regret per transition: at most ~64 restarts (~2 windows)
		// to detect density, one ~4KB span to detect sparsity.
		if candidates++; candidates > 32 {
			if candidates*64 > cand-anchor {
				// Dense: SWAR phases until a span proves sparse.
				for s <= limit {
					end := min(s+pairPhaseSpan, limit+1)
					var got int
					s, got = tr.singlePairMatchRange(input, s, end, buf)
					if end > limit {
						return
					}
					if got*64 <= pairPhaseSpan {
						break // sparse span: back to the kernel
					}
				}
			}
			// Restart the measurement window here.
			candidates = 0
			anchor = s
		}
	}
}

// singleKernelWalk is singleKernelMatch for the callback API.
func (tr *Trie) singleKernelWalk(input []byte, fn WalkFn) {
	p := tr.single
	n := len(p)
	oa, ob := tr.singleO1, tr.singleO2
	if oa > ob {
		oa, ob = ob, oa
	}
	a, b := p[oa], p[ob]
	d := ob - oa
	dp := tr.singleDP
	limit := len(input) - n
	s := 0
	candidates := 0
	anchor := 0
	for s <= limit {
		m := limit - s + 1
		j := indexPair2(input[s+oa:], m, a, b, d)
		if j < 0 {
			for t := s + m&^31; t <= limit; t++ {
				if input[t+oa] == a && input[t+ob] == b && singleVerify(input, t, p) {
					if !fn(uint32(t+n-1), uint32(dp), uint32(dp>>32)) {
						return
					}
					t += tr.singleSkip - 1
				}
			}
			return
		}
		cand := s + j
		if singleVerify(input, cand, p) {
			if !fn(uint32(cand+n-1), uint32(dp), uint32(dp>>32)) {
				return
			}
			s = cand + tr.singleSkip
		} else {
			s = cand + 1
		}
		// See singleKernelMatch for the adaptive phase policy.
		if candidates++; candidates > 32 {
			if candidates*64 > cand-anchor {
				for s <= limit {
					end := min(s+pairPhaseSpan, limit+1)
					var got int
					var stopped bool
					s, got, stopped = tr.singlePairWalkRange(input, s, end, fn)
					if stopped || end > limit {
						return
					}
					if got*64 <= pairPhaseSpan {
						break
					}
				}
			}
			candidates = 0
			anchor = s
		}
	}
}
