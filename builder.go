package ahocorasick

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
)

// state represents a node in the Aho-Corasick trie during construction.
// Each state maintains links for pattern matching and failure transitions.
type state struct {
	parent   *state          // Parent state in the trie
	failLink *state          // Failure link for the Aho-Corasick algorithm
	dictLink *state          // Dictionary link to next matching pattern
	trans    map[byte]*state // Transitions to child states
	id       uint32          // Unique identifier for this state
	dict     uint32          // Length of pattern ending at this state (0 if none)
	pattern  uint32          // Pattern number for matches at this state
	value    byte            // Character value on incoming transition
}

// TrieBuilder constructs an Aho-Corasick string matching automaton.
// It builds the trie structure incrementally and computes failure/dictionary
// links before producing the final optimized Trie.
type TrieBuilder struct {
	states      []*state // All states in the trie
	root        *state   // Root state of the trie
	numPatterns uint32   // Number of patterns added
}

// NewTrieBuilder creates and initializes a new TrieBuilder.
// It creates two initial states - state 0 (unused) and state 1 (root).
// State 0 exists to maintain consistency with the paper's state numbering.
func NewTrieBuilder() *TrieBuilder {
	tb := &TrieBuilder{
		states:      make([]*state, 0),
		root:        nil,
		numPatterns: 0,
	}
	tb.addState(0, nil) // State 0 (unused)
	tb.addState(0, nil) // State 1 (root)
	tb.root = tb.states[1]
	return tb
}

// addState creates a new state in the trie with the given byte value
// and parent state. Returns the newly created state.
func (tb *TrieBuilder) addState(value byte, parent *state) *state {
	s := &state{
		id:       uint32(len(tb.states)),
		value:    value,
		parent:   parent,
		trans:    make(map[byte]*state),
		dict:     0,
		failLink: nil,
		dictLink: nil,
		pattern:  0,
	}
	tb.states = append(tb.states, s)
	return s
}

// AddPattern adds a byte pattern to the Trie under construction.
// It creates new states as needed while following/creating the path
// for the pattern in the trie. The final state is marked with the
// pattern length and assigned a unique pattern number.
func (tb *TrieBuilder) AddPattern(pattern []byte) *TrieBuilder {
	s := tb.root
	var t *state
	var ok bool

	// Follow/create the path for this pattern.
	for _, c := range pattern {
		if t, ok = s.trans[c]; !ok {
			t = tb.addState(c, s)
			s.trans[c] = t
		}
		s = t
	}

	// Mark the final state with pattern info.
	s.dict = uint32(len(pattern))
	s.pattern = tb.numPatterns
	tb.numPatterns++

	return tb
}

// AddPatterns adds multiple byte patterns to the Trie.
func (tb *TrieBuilder) AddPatterns(patterns [][]byte) *TrieBuilder {
	for _, pattern := range patterns {
		tb.AddPattern(pattern)
	}
	return tb
}

// AddString adds a string pattern to the Trie under construction.
func (tb *TrieBuilder) AddString(pattern string) *TrieBuilder {
	return tb.AddPattern([]byte(pattern))
}

// AddStrings add multiple strings to the Trie.
func (tb *TrieBuilder) AddStrings(patterns []string) *TrieBuilder {
	for _, pattern := range patterns {
		tb.AddString(pattern)
	}
	return tb
}

// LoadPatterns loads byte patterns from a file. Expects one pattern per line in hexadecimal form.
// Empty lines are skipped. Returns error if file cannot be opened or if hex decoding fails.
func (tb *TrieBuilder) LoadPatterns(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)

	for s.Scan() {
		str := strings.TrimSpace(s.Text())
		if len(str) != 0 {
			pattern, err := hex.DecodeString(str)
			if err != nil {
				return err
			}
			tb.AddPattern(pattern)
		}
	}

	return s.Err()
}

// LoadStrings loads string patterns from a file. Expects one pattern per line.
// Empty lines are skipped. Returns error if file cannot be opened.
func (tb *TrieBuilder) LoadStrings(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)

	for s.Scan() {
		str := strings.TrimSpace(s.Text())
		if len(str) != 0 {
			tb.AddString(str)
		}
	}

	return s.Err()
}

// Build constructs the final Trie structure.
// This involves:
//  1. Computing failure and dictionary links.
//  2. Renumbering states in BFS order so frequently visited (shallow)
//     states are packed together for cache and TLB locality.
//  3. Converting the state graph into array-based representation.
//  4. Pre-computing all possible transitions.
//  5. Setting up object pools for match results.
func (tb *TrieBuilder) Build() *Trie {
	// Compute failure and dictionary links needed for the Aho-Corasick algorithm.
	tb.computeFailLinks()
	tb.computeDictLinks()

	numStates := len(tb.states)

	// Packed transitions reserve the high bit for outputFlag (see trie.go),
	// leaving 31 bits for state ids. Refuse to build a trie whose ids would
	// collide with the flag. Unreachable in practice: the builder needs
	// hundreds of bytes per state, so >2^31 states means hundreds of GB.
	if uint64(numStates) > uint64(stateMask)+1 {
		panic("ahocorasick: too many states to build trie (max 2^31)")
	}

	// Renumber states breadth-first. The automaton spends nearly all
	// its time in shallow states; giving them adjacent ids packs their
	// transition rows into a small contiguous prefix of failTrans.
	newID := make([]uint32, numStates)
	order := make([]*state, 0, numStates)
	order = append(order, tb.states[0], tb.root)
	newID[tb.root.id] = 1
	for qi := 1; qi < len(order); qi++ {
		for _, t := range order[qi].trans {
			newID[t.id] = uint32(len(order))
			order = append(order, t)
		}
	}

	// Initialize the array-based trie structure.
	trie := &Trie{
		failTrans: make([][256]uint32, numStates),
		dictLink:  make([]uint32, numStates),
		dict:      make([]uint32, numStates),
		pattern:   make([]uint32, numStates),
	}

	// Set up object pool for match buffer reuse.
	trie.bufPool = newBufPool()

	// Convert the state graph into arrays using the BFS numbering.
	for i, s := range order {
		trie.dict[i] = s.dict
		trie.pattern[i] = s.pattern
		if s.dictLink != nil {
			trie.dictLink[i] = newID[s.dictLink.id]
		}
		// Pre-compute all possible byte transitions for this state.
		for b := range 256 {
			c := byte(b)
			trie.failTrans[i][c] = newID[tb.computeFailTransition(s, c)]
		}
	}

	trie.addOutputFlags()
	trie.buildRootSkip()
	trie.setStopEntry()

	return trie
}

// computeFailTransition determines the next state for a given state and input byte.
// It follows failure links until it finds a valid transition or reaches the root.
// This is used to pre-compute all possible transitions during Build().
func (tb *TrieBuilder) computeFailTransition(s *state, c byte) uint32 {
	for t := s; t != nil; t = t.failLink {
		if next, exists := t.trans[c]; exists {
			return next.id
		}
	}
	return tb.root.id
}

// computeFailLinks builds the failure links for the Aho-Corasick algorithm.
// It performs a breadth-first traversal of the trie, setting each state's
// failure link to the longest proper suffix that is also a prefix of some pattern.
func (tb *TrieBuilder) computeFailLinks() {
	queue := []*state{tb.root}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]

		for _, t := range s.trans {
			queue = append(queue, t)
			// Follow failure links until we find a state that has a transition
			// on the current character, or reach the root.
			fail := s.failLink
			for fail != nil && fail.trans[t.value] == nil {
				fail = fail.failLink
			}
			if fail != nil {
				t.failLink = fail.trans[t.value]
			} else {
				t.failLink = tb.root
			}
		}
	}
}

// computeDictLinks builds dictionary links that connect states representing
// overlapping patterns. This allows finding all matching patterns that end
// at the current position in a single traversal.
func (tb *TrieBuilder) computeDictLinks() {
	for _, s := range tb.states {
		if s == tb.root {
			continue
		}
		// Follow failure links until we find a state that represents
		// the end of some pattern.
		for fail := s.failLink; fail != nil; fail = fail.failLink {
			if fail.dict > 0 {
				s.dictLink = fail
				break
			}
		}
	}
}
