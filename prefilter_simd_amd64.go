//go:build goexperiment.simd && amd64

package ahocorasick

import (
	"math/bits"

	"simd/archsimd"
)

func (p *rootPrefilter) enableSIMD() bool {
	return p.count > 0 && archsimd.X86.AVX()
}

func (p *rootPrefilter) nextCandidateSIMD(input []byte, start int) int {
	if p.count == 0 {
		return len(input)
	}

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
			return i + bits.TrailingZeros16(mask)
		}
		i += 16
	}

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
