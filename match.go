package ahocorasick

import (
	"bytes"
	"fmt"
)

// Match represents a matched pattern in the input.
type Match struct {
	pos     uint32
	pattern uint32
	match   []byte

	// buf, set only on the first match of a batch, lets ReleaseMatches
	// recycle the whole batch with a single pool operation.
	buf *matchBuf
}

func newMatchString(pos, pattern uint32, match string) *Match {
	return &Match{pos: pos, pattern: pattern, match: []byte(match)}
}

func (m *Match) String() string {
	return fmt.Sprintf("{%d %d %q}", m.pos, m.pattern, m.match)
}

// Pos returns the byte position of the match.
func (m *Match) Pos() uint32 {
	return m.pos
}

// Pattern returns the pattern id of the match.
func (m *Match) Pattern() uint32 {
	return m.pattern
}

// Match returns the pattern matched.
func (m *Match) Match() []byte {
	return m.match
}

// MatchString returns the pattern matched as a string.
func (m *Match) MatchString() string {
	return string(m.match)
}

// MatchEqual reports whether a and b have the same position, pattern id,
// and matched bytes.
func MatchEqual(a, b *Match) bool {
	return a.pos == b.pos && a.pattern == b.pattern && bytes.Equal(a.match, b.match)
}
