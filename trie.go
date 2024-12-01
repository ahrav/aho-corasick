package ahocorasick

import (
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

	matchPool       sync.Pool // Pool for match slices
	matchStructPool sync.Pool // Pool for Match structs

	// trans    [][256]int64
	// failLink []int64
}

// Walk calls this function on any match, giving the end position, length of the matched bytes,
// and the pattern number.
type WalkFn func(end, n, pattern uint32) bool

// Walk runs the algorithm on a given output, calling the supplied callback function on every
// match. The algorithm will terminate if the callback function returns false.
func (tr *Trie) Walk(input []byte, fn WalkFn) {
	// Local references to frequently accessed slices
	failTrans := tr.failTrans
	dict := tr.dict
	pattern := tr.pattern
	dictLink := tr.dictLink

	s := rootState

	inputLen := len(input)
	for i := range inputLen {
		s = failTrans[s][input[i]]

		if dict[s] != 0 || dictLink[s] != nilState {
			// Primary match check
			if dict[s] != 0 && !fn(uint32(i), dict[s], pattern[s]) {
				return
			}

			// Dictionary link traversal
			for u := dictLink[s]; u != nilState; u = dictLink[u] {
				if !fn(uint32(i), dict[u], pattern[u]) {
					return
				}
			}
		}
	}
}

// Match runs the Aho-Corasick string-search algorithm on a byte input.
func (tr *Trie) Match(input []byte) []*Match {
	matches := tr.matchPool.Get().([]*Match)
	matches = matches[:0] // Reset length but keep capacity

	tr.Walk(input, func(end, n, pattern uint32) bool {
		pos := end - n + 1
		m := tr.matchStructPool.Get().(*Match)
		m.pos = pos
		m.pattern = pattern
		m.match = input[pos : pos+n]
		matches = append(matches, m)
		return true
	})

	return matches
}

// MatchFirst is the same as Match, but returns after first successful match.
func (tr *Trie) MatchFirst(input []byte) *Match {
	matches := tr.matchPool.Get().([]*Match)
	matches = matches[:0] // Reset length but keep capacity

	tr.Walk(input, func(end, n, pattern uint32) bool {
		pos := end - n + 1
		m := tr.matchStructPool.Get().(*Match)
		m.pos = pos
		m.pattern = pattern
		m.match = input[pos : pos+n]
		matches = append(matches, m)
		return false
	})

	if len(matches) == 0 {
		return nil
	}

	return matches[0]
}

// MatchString runs the Aho-Corasick string-search algorithm on a string input.
func (tr *Trie) MatchString(input string) []*Match {
	return tr.Match([]byte(input))
}

// MatchFirstString is the same as MatchString, but returns after first successful match.
func (tr *Trie) MatchFirstString(input string) *Match {
	return tr.MatchFirst([]byte(input))
}

// New method to return slice to pool
func (tr *Trie) ReleaseMatches(matches []*Match) {
	for _, m := range matches {
		tr.matchStructPool.Put(m)
	}
	tr.matchPool.Put(matches)
}
