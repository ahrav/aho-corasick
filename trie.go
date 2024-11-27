package ahocorasick

const (
	rootState int64 = 1
	nilState  int64 = 0
)

// Trie represents a trie of patterns with extra links as per the Aho-Corasick algorithm.
type Trie struct {
	failTrans [][256]int64

	dict     []int64
	pattern  []int64
	dictLink []int64

	trans    [][256]int64
	failLink []int64
}

// Walk calls this function on any match, giving the end position, length of the matched bytes,
// and the pattern number.
type WalkFn func(end, n, pattern int64) bool

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
			if dict[s] != 0 && !fn(int64(i), dict[s], pattern[s]) {
				return
			}

			// Dictionary link traversal
			for u := dictLink[s]; u != nilState; u = dictLink[u] {
				if !fn(int64(i), dict[u], pattern[u]) {
					return
				}
			}
		}
	}
}

// Match runs the Aho-Corasick string-search algorithm on a byte input.
func (tr *Trie) Match(input []byte) []*Match {
	matches := make([]*Match, 0, len(input)>>5) // heuristic to reduce memory allocation
	tr.Walk(input, func(end, n, pattern int64) bool {
		pos := end - n + 1
		matches = append(matches, newMatch(pos, pattern, input[pos:pos+n]))
		return true
	})
	return matches
}

// MatchFirst is the same as Match, but returns after first successful match.
func (tr *Trie) MatchFirst(input []byte) *Match {
	var match *Match
	tr.Walk(input, func(end, n, pattern int64) bool {
		pos := end - n + 1
		match = &Match{pos: pos, match: input[pos : pos+n]}
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
