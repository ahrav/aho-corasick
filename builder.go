package ahocorasick

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
)

// state represents a node in the Aho-Corasick trie during construction.
// States live in TrieBuilder.states and reference each other by index:
// children form a singly linked sibling list kept sorted by byte value,
// which makes the BFS numbering (and thus Encode output) deterministic
// without a sort pass. Index-based value states keep the builder free
// of per-node allocations and GC pointer scanning.
type state struct {
	firstChild uint32 // Head of the sorted sibling list (0 if leaf)
	nextSib    uint32 // Next sibling in the parent's list (0 if last)
	failLink   uint32 // Failure link for the Aho-Corasick algorithm
	dictLink   uint32 // Dictionary link to next matching pattern
	dict       uint32 // Length of pattern ending at this state (0 if none)
	pattern    uint32 // Pattern number for matches at this state
	value      byte   // Character value on incoming transition
}

// TrieBuilder constructs an Aho-Corasick string matching automaton.
// It builds the trie structure incrementally and computes failure/dictionary
// links before producing the final optimized Trie.
type TrieBuilder struct {
	states      []state // All states; index 0 unused, index 1 is the root
	numPatterns uint32  // Number of patterns added
}

// NewTrieBuilder creates and initializes a new TrieBuilder.
// It creates two initial states - state 0 (unused) and state 1 (root).
// State 0 exists to maintain consistency with the paper's state numbering.
func NewTrieBuilder() *TrieBuilder {
	tb := &TrieBuilder{states: make([]state, 2)}
	return tb
}

// child returns the index of s's child on byte c, or 0 if none.
func (tb *TrieBuilder) child(s uint32, c byte) uint32 {
	for t := tb.states[s].firstChild; t != 0; t = tb.states[t].nextSib {
		if v := tb.states[t].value; v == c {
			return t
		} else if v > c {
			return 0
		}
	}
	return 0
}

// addChild inserts a new child of s on byte c, keeping the sibling list
// sorted by byte value, and returns its index.
func (tb *TrieBuilder) addChild(s uint32, c byte) uint32 {
	id := uint32(len(tb.states))
	tb.states = append(tb.states, state{value: c})

	// Find the insertion point in the sorted sibling list.
	prev := uint32(0)
	next := tb.states[s].firstChild
	for next != 0 && tb.states[next].value < c {
		prev = next
		next = tb.states[next].nextSib
	}
	tb.states[id].nextSib = next
	if prev == 0 {
		tb.states[s].firstChild = id
	} else {
		tb.states[prev].nextSib = id
	}
	return id
}

// AddPattern adds a byte pattern to the Trie under construction.
// It creates new states as needed while following/creating the path
// for the pattern in the trie. The final state is marked with the
// pattern length and assigned a unique pattern number.
func (tb *TrieBuilder) AddPattern(pattern []byte) *TrieBuilder {
	s := rootState

	// Follow/create the path for this pattern.
	for _, c := range pattern {
		t := tb.child(s, c)
		if t == 0 {
			t = tb.addChild(s, c)
		}
		s = t
	}

	// Mark the final state with pattern info.
	tb.states[s].dict = uint32(len(pattern))
	tb.states[s].pattern = tb.numPatterns
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
//  3. Converting the state graph into array-based representation,
//     pre-computing all transitions and output flags in one DP pass.
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

	// Renumber states breadth-first. The automaton spends nearly all
	// its time in shallow states; giving them adjacent ids packs their
	// transition rows into a small contiguous prefix of failTrans.
	// Sibling lists are sorted by byte, so the numbering — and thus
	// Encode output — is deterministic for a given pattern set.
	newID := make([]uint32, numStates)
	order := make([]uint32, 2, numStates)
	order[0], order[1] = 0, rootState
	newID[rootState] = 1
	for qi := 1; qi < len(order); qi++ {
		for t := tb.states[order[qi]].firstChild; t != 0; t = tb.states[t].nextSib {
			newID[t] = uint32(len(order))
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

	half := numStates <= 1<<15
	if half {
		trie.failTrans16 = make([]uint16, numStates*256)
	}

	// Convert the state graph into arrays using the BFS numbering.
	// Transition rows are built by the classic goto/fail dynamic
	// program: a state's row is its fail state's row with the state's
	// own children overwritten. BFS order guarantees the fail state
	// (always shallower) is processed first, so each row is one 1KB
	// copy plus one write per child instead of 256 fail-chain walks.
	// Output flags ride along: copied entries keep the fail row's
	// flags (same targets), and each own-child entry takes its flag
	// straight from the child's dict/dictLink, so no separate flag
	// pass over the table is needed. The half-width table is built by
	// the same DP.
	for i, sid := range order {
		s := &tb.states[sid]
		trie.dict[i] = s.dict
		trie.pattern[i] = s.pattern
		if s.dictLink != 0 {
			trie.dictLink[i] = newID[s.dictLink]
		}
		row := &trie.failTrans[i]
		if sid == 0 || sid == rootState {
			// State 0 (unused) and the root: every unclaimed byte
			// goes to the root, which never emits.
			for b := range row {
				row[b] = rootState
			}
		} else {
			// copy (memmove) beats a struct assignment (duffcopy)
			// for the 1KB row on amd64.
			copy(row[:], trie.failTrans[newID[s.failLink]][:])
		}
		var row16 []uint16
		if half {
			row16 = trie.failTrans16[i<<8 : i<<8+256]
			if sid == 0 || sid == rootState {
				for b := range row16 {
					row16[b] = uint16(rootState)
				}
			} else {
				copy(row16, trie.failTrans16[int(newID[s.failLink])<<8:])
			}
		}
		for t := s.firstChild; t != 0; t = tb.states[t].nextSib {
			ts := &tb.states[t]
			v := newID[t]
			if ts.dict != 0 || ts.dictLink != 0 {
				v |= outputFlag
			}
			row[ts.value] = v
			if half {
				row16[ts.value] = uint16(v&stateMask) | uint16(v>>16)&(1<<15)
			}
		}
	}

	trie.buildDictPat()
	trie.buildRootSkip()
	// Compute the live-byte set only when a scan path exists to read the
	// class table; single-stop and failTrans16 tries never load it, and
	// building it anyway would retain up to 512B/state of dead weight.
	// Every state except 0 and the root is some state's child, and value
	// is the byte on its incoming edge, so indexing the flat state slice
	// yields the same set the child walk did.
	if trie.classTableUsable() {
		var live [256]bool
		for i := range tb.states {
			if i != 0 && uint32(i) != rootState {
				live[tb.states[i].value] = true
			}
		}
		trie.buildClassTable(&live)
	}
	trie.setStopEntry()

	return trie
}

// computeFailTransition determines the next state for a given state and input byte.
// It follows failure links until it finds a valid transition or reaches the root.
// Kept as the reference definition of the transition function; Build derives
// the same values with the row DP, and TestDPEquivalence cross-checks them.
func (tb *TrieBuilder) computeFailTransition(s uint32, c byte) uint32 {
	for t := s; t != 0; t = tb.states[t].failLink {
		if next := tb.child(t, c); next != 0 {
			return next
		}
	}
	return rootState
}

// computeFailLinks builds the failure links for the Aho-Corasick algorithm.
// It performs a breadth-first traversal of the trie, setting each state's
// failure link to the longest proper suffix that is also a prefix of some pattern.
func (tb *TrieBuilder) computeFailLinks() {
	queue := make([]uint32, 1, len(tb.states))
	queue[0] = rootState
	for qi := 0; qi < len(queue); qi++ {
		s := queue[qi]
		for t := tb.states[s].firstChild; t != 0; t = tb.states[t].nextSib {
			queue = append(queue, t)
			// Follow failure links until we find a state that has a transition
			// on the current character, or reach the root.
			c := tb.states[t].value
			fail := tb.states[s].failLink
			for fail != 0 && tb.child(fail, c) == 0 {
				fail = tb.states[fail].failLink
			}
			if fail != 0 {
				tb.states[t].failLink = tb.child(fail, c)
			} else {
				tb.states[t].failLink = rootState
			}
		}
	}
}

// computeDictLinks builds dictionary links that connect states representing
// overlapping patterns. This allows finding all matching patterns that end
// at the current position in a single traversal.
func (tb *TrieBuilder) computeDictLinks() {
	for i := range tb.states {
		if uint32(i) == rootState || i == 0 {
			continue
		}
		// Follow failure links until we find a state that represents
		// the end of some pattern.
		for fail := tb.states[i].failLink; fail != 0; fail = tb.states[fail].failLink {
			if tb.states[fail].dict > 0 {
				tb.states[i].dictLink = fail
				break
			}
		}
	}
}
