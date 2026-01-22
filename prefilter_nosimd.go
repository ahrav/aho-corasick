//go:build !goexperiment.simd || !amd64

package ahocorasick

func (p *rootPrefilter) enableSIMD() bool {
	return false
}

func (p *rootPrefilter) nextCandidateSIMD(_ []byte, start int) int {
	return start
}
