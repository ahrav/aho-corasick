package ahocorasick

import (
	"math/rand"
	"testing"
)

// TestDPEquivalence checks the DP-built tables against the reference
// definitions: per state, dict/pattern/dictLink must equal the builder
// state's values (dictLink translated through the BFS numbering); per
// (state, byte), failTrans states must equal the fail-chain walk
// (computeFailTransition), output flags must equal the target's
// dict/dictLink emit condition, and failTrans16 must mirror failTrans.
func TestDPEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		numPat := 1 + rng.Intn(30)
		alpha := 2 + rng.Intn(8)
		var pats []string
		for i := 0; i < numPat; i++ {
			l := 1 + rng.Intn(10)
			b := make([]byte, l)
			for j := range b {
				b[j] = byte('a' + rng.Intn(alpha))
			}
			pats = append(pats, string(b))
		}
		tb := NewTrieBuilder().AddStrings(pats)
		trie := tb.Build()

		// Recompute the BFS numbering the same way Build does.
		numStates := len(tb.states)
		newID := make([]uint32, numStates)
		order := make([]uint32, 2, numStates)
		order[0], order[1] = 0, rootState
		newID[rootState] = 1
		for qi := 1; qi < len(order); qi++ {
			for s := tb.states[order[qi]].firstChild; s != 0; s = tb.states[s].nextSib {
				newID[s] = uint32(len(order))
				order = append(order, s)
			}
		}

		emits := make([]bool, len(trie.dict))
		for s := range emits {
			emits[s] = trie.dict[s] != 0 || trie.dictLink[s] != nilState
		}
		for i, sid := range order {
			if i == 0 {
				continue
			}
			s := &tb.states[sid]
			if trie.dict[i] != s.dict || trie.pattern[i] != s.pattern {
				t.Fatalf("trial %d state %d: dict/pattern got (%d,%d) want (%d,%d)",
					trial, i, trie.dict[i], trie.pattern[i], s.dict, s.pattern)
			}
			wantDictLink := nilState
			if s.dictLink != 0 {
				wantDictLink = newID[s.dictLink]
			}
			if trie.dictLink[i] != wantDictLink {
				t.Fatalf("trial %d state %d: dictLink got %d want %d",
					trial, i, trie.dictLink[i], wantDictLink)
			}
			for b := 0; b < 256; b++ {
				v := trie.failTrans[i][b]
				want := newID[tb.computeFailTransition(sid, byte(b))]
				if got := v & stateMask; got != want {
					t.Fatalf("trial %d state %d byte %d: got %d want %d", trial, i, b, got, want)
				}
				if got, want := v&outputFlag != 0, emits[v&stateMask]; got != want {
					t.Fatalf("trial %d state %d byte %d: flag %v want %v", trial, i, b, got, want)
				}
				if trie.failTrans16 != nil {
					w := trie.failTrans16[i<<8+b]
					if uint32(w&(1<<15-1)) != v&stateMask || (w&(1<<15) != 0) != (v&outputFlag != 0) {
						t.Fatalf("trial %d state %d byte %d: failTrans16 %#x disagrees with failTrans %#x", trial, i, b, w, v)
					}
				}
			}
		}
	}
}

// buildBoundaryTrie builds a trie with exactly numStates states out of
// two-byte patterns. Patterns [hi, lo] for consecutive integers produce
// 2 bookkeeping states (state 0 and the root) + one state per distinct
// first byte + one state per pattern, giving exact control of the count.
func buildBoundaryTrie(t *testing.T, numStates int) *Trie {
	t.Helper()
	// Grow pattern count until the state arithmetic lands exactly:
	// states = 2 + first-byte groups + patterns.
	patterns := 0
	groups := 0
	for 2+groups+patterns < numStates {
		if patterns == groups*256 { // start a new group
			groups++
		}
		patterns++
	}
	pats := make([][]byte, 0, patterns)
	for i := 0; i < patterns; i++ {
		pats = append(pats, []byte{byte(i >> 8), byte(i)})
	}
	tr := NewTrieBuilder().AddPatterns(pats).Build()
	if got := len(tr.failTrans); got != numStates {
		t.Fatalf("fixture built %d states, want %d", got, numStates)
	}
	return tr
}

// TestFailTrans16Cutoff pins the half-width table's size gate at the DP
// builder's 2^15-state boundary: exactly 2^15 states must build the
// table (and pack the highest 15-bit state id and the bit-15 output flag
// without truncation), while one state more must leave it nil.
func TestFailTrans16Cutoff(t *testing.T) {
	if testing.Short() {
		t.Skip("boundary fixtures allocate ~40MB of table")
	}

	t.Run("at limit", func(t *testing.T) {
		tr := buildBoundaryTrie(t, failTrans16MaxStates)
		if tr.failTrans16 == nil {
			t.Fatalf("failTrans16 nil at exactly failTrans16MaxStates states, want built")
		}
		// The highest BFS id is a pattern-terminal (emitting) state:
		// its packed entry must carry all 15 state bits and the flag.
		// If failTrans16MaxStates is ever raised past the 15-bit packing
		// capacity, this catches the truncation at the new boundary.
		maxState := uint32(failTrans16MaxStates - 1)
		var found bool
		for s := 0; s < len(tr.failTrans) && !found; s++ {
			for b := 0; b < 256; b++ {
				v := tr.failTrans[s][b]
				if v&stateMask != maxState {
					continue
				}
				found = true
				if v&outputFlag == 0 {
					t.Fatalf("state %#x reached from (%d,%#x) lost its output flag in failTrans", maxState, s, b)
				}
				w := tr.failTrans16[s<<8+b]
				if uint32(w&(1<<15-1)) != maxState {
					t.Fatalf("failTrans16 truncated max state: got %#x want %#x", w&(1<<15-1), maxState)
				}
				if w&(1<<15) == 0 {
					t.Fatalf("failTrans16 dropped the output flag on max state entry %#x", w)
				}
				break
			}
		}
		if !found {
			t.Fatalf("no transition targets max state %#x; fixture invalid", maxState)
		}
	})

	t.Run("one past limit", func(t *testing.T) {
		tr := buildBoundaryTrie(t, failTrans16MaxStates+1)
		if tr.failTrans16 != nil {
			t.Fatalf("failTrans16 built for %d states, want nil above the limit", failTrans16MaxStates+1)
		}
	})
}
