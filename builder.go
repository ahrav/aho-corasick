package ahocorasick

import (
	"bufio"
	"encoding/hex"
	"os"
	"sort"
	"strings"
)

// state represents a node in the Aho-Corasick trie during construction.
// Each state maintains links for pattern matching and failure transitions.
// Child transitions are kept in a slice sorted by byte value: pattern
// tries have tiny fan-out on average, so sorted-slice lookup beats a map
// while keeping iteration order — and therefore state numbering and the
// serialized form — deterministic.
type state struct {
	parent   *state   // Parent state in the trie
	failLink *state   // Failure link for the Aho-Corasick algorithm
	dictLink *state   // Dictionary link to next matching pattern
	children []*state // Child states sorted by transition byte value
	id       uint32   // Unique identifier for this state
	dict     uint32   // Length of pattern ending at this state (0 if none)
	pattern  uint32   // Pattern number for matches at this state
	value    byte     // Character value on incoming transition
}

// child returns the child reached on byte c, or nil.
func (s *state) child(c byte) *state {
	// Binary search; fan-out is usually small but can reach 256.
	lo, hi := 0, len(s.children)
	for lo < hi {
		mid := (lo + hi) / 2
		if s.children[mid].value < c {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(s.children) && s.children[lo].value == c {
		return s.children[lo]
	}
	return nil
}

// insertChild adds t to s.children, keeping the slice sorted by value.
func (s *state) insertChild(t *state) {
	i := sort.Search(len(s.children), func(k int) bool { return s.children[k].value >= t.value })
	s.children = append(s.children, nil)
	copy(s.children[i+1:], s.children[i:])
	s.children[i] = t
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
		id:     uint32(len(tb.states)),
		value:  value,
		parent: parent,
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

	// Follow/create the path for this pattern.
	for _, c := range pattern {
		t := s.child(c)
		if t == nil {
			t = tb.addState(c, s)
			s.insertChild(t)
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
//  2. Renumbering states in BFS order (children visited in byte order)
//     so frequently visited (shallow) states are packed together for
//     cache and TLB locality, and so the numbering — and therefore the
//     serialized form — is deterministic for a given pattern set.
//  3. Converting the state graph into array-based representation: each
//     state's transition row starts as a copy of its fail state's row
//     (already complete, since fail states are strictly shallower and
//     BFS builds shallow rows first) with the state's own children
//     overwriting their bytes. This is the standard NFA-to-DFA row
//     construction and avoids walking failure chains per (state, byte).
//  4. Setting up object pools for match results.
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

	// Renumber states breadth-first, children in byte order. The
	// automaton spends nearly all its time in shallow states; giving
	// them adjacent ids packs their transition rows into a small
	// contiguous prefix of failTrans.
	newID := make([]uint32, numStates)
	order := make([]*state, 0, numStates)
	order = append(order, tb.states[0], tb.root)
	newID[tb.root.id] = 1
	for qi := 1; qi < len(order); qi++ {
		for _, t := range order[qi].children {
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

	// Row 0 (unused state) and the root row: every byte either enters a
	// root child or self-loops at the root.
	rootRow := &trie.failTrans[1]
	for b := range 256 {
		rootRow[b] = 1
	}
	for _, t := range tb.root.children {
		rootRow[t.value] = newID[t.id]
	}
	row0 := &trie.failTrans[0]
	for b := range 256 {
		row0[b] = 1
	}

	// Fill metadata and rows in BFS order. order[i] has new id i.
	trie.dict[1] = tb.root.dict
	trie.pattern[1] = tb.root.pattern
	for i := 2; i < numStates; i++ {
		s := order[i]
		trie.dict[i] = s.dict
		trie.pattern[i] = s.pattern
		if s.dictLink != nil {
			trie.dictLink[i] = newID[s.dictLink.id]
		}
		// The fail state is strictly shallower, so its row (at a
		// smaller BFS index) is already complete.
		trie.failTrans[i] = trie.failTrans[newID[s.failLink.id]]
		for _, t := range s.children {
			trie.failTrans[i][t.value] = newID[t.id]
		}
	}
	if tb.root.dictLink != nil {
		trie.dictLink[1] = newID[tb.root.dictLink.id]
	}

	trie.addOutputFlags()
	trie.buildRootSkip()
	trie.buildFailTrans16()
	// Compute the live-byte set only when a scan path exists to read the
	// class table; single-stop and failTrans16 tries never load it, and
	// building it anyway would retain up to 512B/state of dead weight.
	if trie.classTableUsable() {
		var live [256]bool
		for _, s := range tb.states {
			for _, t := range s.children {
				live[t.value] = true
			}
		}
		trie.buildClassTable(&live)
	}
	trie.setStopEntry()

	return trie
}

// computeFailLinks builds the failure links for the Aho-Corasick algorithm.
// It performs a breadth-first traversal of the trie, setting each state's
// failure link to the longest proper suffix that is also a prefix of some pattern.
func (tb *TrieBuilder) computeFailLinks() {
	queue := make([]*state, 0, len(tb.states)-1)
	queue = append(queue, tb.root)
	for qi := 0; qi < len(queue); qi++ {
		s := queue[qi]
		for _, t := range s.children {
			queue = append(queue, t)
			// Follow failure links until we find a state that has a
			// transition on the current character, or reach the root.
			fail := s.failLink
			for fail != nil && fail.child(t.value) == nil {
				fail = fail.failLink
			}
			if fail != nil {
				t.failLink = fail.child(t.value)
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
