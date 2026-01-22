package ahocorasick

// rootPrefilter accelerates scanning at the root state by skipping bytes that
// cannot start any pattern. It tracks up to 16 candidate bytes for SIMD use.
type rootPrefilter struct {
	bytes  [16]byte     // Candidate bytes that transition away from root.
	blocks [16][16]byte // SIMD broadcast blocks for each candidate byte.
	count  int          // Number of candidates in bytes/blocks.
	simd   bool         // Whether SIMD scanning is enabled for this trie.
}

func (p *rootPrefilter) init(rootTrans [256]uint32) {
	p.count = 0
	p.simd = false

	for b := range 256 {
		if rootTrans[b] != rootState {
			if p.count == len(p.bytes) {
				// Too many candidates for the SIMD prefilter; disable it.
				p.count = 0
				return
			}
			p.bytes[p.count] = byte(b)
			p.count++
		}
	}

	if p.count == 0 {
		return
	}

	// Pre-broadcast each candidate byte for SIMD comparisons.
	for i := 0; i < p.count; i++ {
		for j := range 16 {
			p.blocks[i][j] = p.bytes[i]
		}
	}

	p.simd = p.enableSIMD()
}

func (tr *Trie) initPrefilter() {
	if len(tr.failTrans) <= int(rootState) {
		tr.prefilter = rootPrefilter{}
		return
	}
	tr.prefilter.init(tr.failTrans[rootState])
}
