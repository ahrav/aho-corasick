//go:build amd64 && !purego

package ahocorasick

// Per-kernel availability constants: call sites branch on these, so
// unused strategies compile away. The pair kernel wins on amd64; the
// or2 kernel is compiled and tested but not dispatched, because the
// stdlib IndexByte runs AVX2 here and two windowed 80GB/s passes beat
// one 28GB/s SSE2 pass (measured on Zen 4: 39 vs 25GB/s effective on
// the two-stop-byte no-match row). Enabling or2 on amd64 needs an
// AVX2 kernel with runtime feature detection.
const (
	hasPairKernel = true
	hasOr2Kernel  = false
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
