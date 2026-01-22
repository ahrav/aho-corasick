package ahocorasick

type rootPrefilter struct {
	bytes  [16]byte
	blocks [16][16]byte
	count  int
	simd   bool
}

func (p *rootPrefilter) init(rootTrans [256]uint32) {
	p.count = 0
	p.simd = false

	for b := 0; b < 256; b++ {
		if rootTrans[b] != rootState {
			if p.count == len(p.bytes) {
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

	for i := 0; i < p.count; i++ {
		for j := 0; j < 16; j++ {
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
