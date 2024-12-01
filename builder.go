package ahocorasick

import (
	"bufio"
	"encoding/hex"
	"os"
	"strings"
	"sync"
)

type state struct {
	parent   *state
	failLink *state
	dictLink *state
	trans    map[byte]*state
	id       uint32
	dict     uint32
	pattern  uint32
	value    byte
}

// TrieBuilder is used to build Tries.
type TrieBuilder struct {
	states      []*state
	root        *state
	numPatterns uint32
}

// NewTrieBuilder creates and initializes a new TrieBuilder.
func NewTrieBuilder() *TrieBuilder {
	tb := &TrieBuilder{
		states:      make([]*state, 0),
		root:        nil,
		numPatterns: 0,
	}
	tb.addState(0, nil)
	tb.addState(0, nil)
	tb.root = tb.states[1]
	return tb
}

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
func (tb *TrieBuilder) AddPattern(pattern []byte) *TrieBuilder {
	s := tb.root
	var t *state
	var ok bool

	for _, c := range pattern {
		if t, ok = s.trans[c]; !ok {
			t = tb.addState(c, s)
			s.trans[c] = t
		}
		s = t
	}

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

// Build constructs the Trie.
func (tb *TrieBuilder) Build() *Trie {
	tb.computeFailLinks()
	tb.computeDictLinks()

	numStates := len(tb.states)
	trans := make([][256]uint32, numStates)
	failLink := make([]uint32, numStates)

	trie := &Trie{
		failTrans: make([][256]uint32, numStates),
		dictLink:  make([]uint32, numStates),
		dict:      make([]uint32, numStates),
		pattern:   make([]uint32, numStates),
	}

	trie.matchPool = sync.Pool{
		New: func() any { return make([]*Match, 0, 16) },
	}
	trie.matchStructPool = sync.Pool{
		New: func() any { return new(Match) },
	}

	// Populate the pool
	for range 64 {
		trie.matchPool.Put(make([]*Match, 0, 16))
		trie.matchStructPool.Put(new(Match))
	}

	for i, s := range tb.states {
		trie.dict[i] = s.dict
		trie.pattern[i] = s.pattern
		for c, t := range s.trans {
			trans[i][c] = t.id
		}
		if s.failLink != nil {
			failLink[i] = s.failLink.id
		}
		if s.dictLink != nil {
			trie.dictLink[i] = s.dictLink.id
		}
		// Precompute fail transitions.
		for b := range 256 {
			c := byte(b)
			trie.failTrans[i][c] = tb.computeFailTransition(s, c)
		}
	}

	return trie
}

// computeFailTransition precomputes the fail transition for a given state and character.
func (tb *TrieBuilder) computeFailTransition(s *state, c byte) uint32 {
	for t := s; t != nil; t = t.failLink {
		if next, exists := t.trans[c]; exists {
			return next.id
		}
	}
	return tb.root.id
}

func (tb *TrieBuilder) computeFailLinks() {
	queue := []*state{tb.root}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]

		for _, t := range s.trans {
			queue = append(queue, t)
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

func (tb *TrieBuilder) computeDictLinks() {
	for _, s := range tb.states {
		if s == tb.root {
			continue
		}
		for fail := s.failLink; fail != nil; fail = fail.failLink {
			if fail.dict > 0 {
				s.dictLink = fail
				break
			}
		}
	}
}
