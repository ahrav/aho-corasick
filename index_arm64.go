//go:build arm64 && !purego

package ahocorasick

// Per-kernel availability constants: call sites branch on these, so
// unused strategies compile away. On arm64 both NEON kernels beat the
// portable strategies (single-pass NEON vs SWAR or dual-pass windowed
// IndexByte).
const (
	hasPairKernel = true
	hasOr2Kernel  = true
)

// indexPair2Asm returns the smallest i in [0, m&^31) with p[i] == a and
// p[i+d] == b, or -1 when the full 32-byte blocks contain no such
// position (the caller scans the remaining tail with scalar code).
// The caller must guarantee p[0 : m&^31 + d] is readable.
//
//go:noescape
func indexPair2Asm(p *byte, m int, a, b byte, d int) int

// indexOr2Asm returns the smallest i in [0, m&^31) with p[i] == a or
// p[i] == b, or -1 when the full blocks contain neither value. Reads
// p[0 : m&^31] only.
//
//go:noescape
func indexOr2Asm(p *byte, m int, a, b byte) int

func indexPair2(p []byte, m int, a, b byte, d int) int {
	return indexPair2Asm(&p[0], m, a, b, d)
}

func indexOr2(p []byte, m int, a, b byte) int {
	return indexOr2Asm(&p[0], m, a, b)
}
