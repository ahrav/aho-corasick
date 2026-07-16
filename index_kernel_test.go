//go:build arm64 && !purego

package ahocorasick

// Kernel-level tests for the vector search kernels: exhaustive differential
// against scalar oracles across sizes, positions, and pair distances. The
// guard-page test proving the read contracts lives in
// index_kernel_unix_test.go (mmap/mprotect are unix-only).

import (
	"math/rand"
	"testing"
)

func oraclePair2(p []byte, m int, a, b byte, d int) int {
	m &^= 31
	for i := 0; i < m; i++ {
		if p[i] == a && p[i+d] == b {
			return i
		}
	}
	return -1
}

func oracleOr2(p []byte, m int, a, b byte) int {
	m &^= 31
	for i := 0; i < m; i++ {
		if p[i] == a || p[i] == b {
			return i
		}
	}
	return -1
}

func TestIndexPair2Exhaustive(t *testing.T) {
	// Sizes crossing every block boundary; hit planted at every position;
	// distances covering typical pattern spans plus the beyond-one-block
	// cases (d is bounded only by pattern length, so d > 32 must place
	// stream B more than a whole block past stream A).
	for _, m := range []int{0, 1, 31, 32, 33, 63, 64, 65, 96, 127, 130} {
		for _, d := range []int{1, 2, 5, 7, 15, 32, 33, 64, 100} {
			buf := make([]byte, m+d)
			for i := range buf {
				buf[i] = 'x'
			}
			// No hit anywhere.
			if got := indexPair2(append([]byte{}, buf...), m, 'A', 'B', d); m > 0 && got != -1 {
				t.Fatalf("m=%d d=%d empty: got %d want -1", m, d, got)
			}
			for pos := 0; pos < m; pos++ {
				p := append([]byte{}, buf...)
				p[pos] = 'A'
				p[pos+d] = 'B'
				want := oraclePair2(p, m, 'A', 'B', d)
				got := indexPair2(p, m, 'A', 'B', d)
				if got != want {
					t.Fatalf("m=%d d=%d pos=%d: got %d want %d", m, d, pos, got, want)
				}
			}
		}
	}
}

func TestIndexPair2EqualBytes(t *testing.T) {
	// a == b and overlapping planted pairs.
	p := make([]byte, 128)
	for i := range p {
		p[i] = 'x'
	}
	p[40], p[43] = 'z', 'z'
	if got, want := indexPair2(p, 96, 'z', 'z', 3), 40; got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestIndexOr2Exhaustive(t *testing.T) {
	for _, m := range []int{0, 1, 31, 32, 33, 64, 65, 127, 130} {
		buf := make([]byte, m)
		for i := range buf {
			buf[i] = 'x'
		}
		for pos := 0; pos < m; pos++ {
			for _, c := range []byte{'A', 'B'} {
				p := append([]byte{}, buf...)
				p[pos] = c
				want := oracleOr2(p, m, 'A', 'B')
				got := indexOr2(p, m, 'A', 'B')
				if got != want {
					t.Fatalf("m=%d pos=%d c=%c: got %d want %d", m, pos, c, got, want)
				}
			}
		}
	}
}

func TestIndexKernelsRandomDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	for iter := 0; iter < 5000; iter++ {
		m := rng.Intn(300)
		d := 1 + rng.Intn(16)
		p := make([]byte, m+d)
		for i := range p {
			p[i] = byte('a' + rng.Intn(4)) // small alphabet: many hits
		}
		a, b := byte('a'+rng.Intn(4)), byte('a'+rng.Intn(4))
		if m > 0 {
			if got, want := indexPair2(p, m, a, b, d), oraclePair2(p, m, a, b, d); got != want {
				t.Fatalf("pair m=%d d=%d a=%c b=%c: got %d want %d (%q)", m, d, a, b, got, want, p)
			}
			if got, want := indexOr2(p, m, a, b), oracleOr2(p, m, a, b); got != want {
				t.Fatalf("or m=%d a=%c b=%c: got %d want %d (%q)", m, a, b, got, want, p)
			}
		}
	}
}
