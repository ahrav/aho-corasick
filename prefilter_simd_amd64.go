//go:build goexperiment.simd && amd64

package ahocorasick

import (
	"math/bits"

	"simd/archsimd"
)

func (p *rootPrefilter) enableSIMD() bool {
	return p.count > 0 && archsimd.X86.AVX()
}

// nextCandidateSIMD returns the next position at or after start that could
// transition from the root state, or len(input) if none are found.
func (p *rootPrefilter) nextCandidateSIMD(input []byte, start int) int {
	if p.count == 0 {
		return len(input)
	}

	// Load the broadcasted candidates once per call.
	var needles [16]archsimd.Uint8x16
	for i := 0; i < p.count; i++ {
		needles[i] = archsimd.LoadUint8x16(&p.blocks[i])
	}

	i := start
	n := len(input)
	for i+16 <= n {
		hay := archsimd.LoadUint8x16Slice(input[i : i+16])
		var mask uint16
		for j := 0; j < p.count; j++ {
			mask |= hay.Equal(needles[j]).ToBits()
		}
		if mask != 0 {
			// First set bit is the earliest candidate in this block.
			return i + bits.TrailingZeros16(mask)
		}
		i += 16
	}

	// Scalar tail for any remaining bytes.
	for ; i < n; i++ {
		b := input[i]
		for j := 0; j < p.count; j++ {
			if b == p.bytes[j] {
				return i
			}
		}
	}

	return n
}
