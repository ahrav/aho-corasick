package ahocorasick

import (
	"math/rand"
	"testing"
)

// TestDPEquivalence checks the DP-built tables against the reference
// definitions for every (state, byte): failTrans states must equal the
// fail-chain walk (computeFailTransition), output flags must equal the
// target's dict/dictLink emit condition, and failTrans16 must mirror
// failTrans.
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
