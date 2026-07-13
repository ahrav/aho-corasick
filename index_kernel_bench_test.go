//go:build (arm64 || amd64) && !purego

package ahocorasick

import (
	"bytes"
	"testing"
)

func BenchmarkKernelPair2(b *testing.B) {
	p := bytes.Repeat([]byte{'x'}, 100032)
	b.SetBytes(100000)
	for n := 0; n < b.N; n++ {
		if indexPair2(p, 100000, 'A', 'B', 3) != -1 {
			b.Fatal("unexpected hit")
		}
	}
}

func BenchmarkKernelOr2(b *testing.B) {
	p := bytes.Repeat([]byte{'x'}, 100032)
	b.SetBytes(100000)
	for n := 0; n < b.N; n++ {
		if indexOr2(p, 100000, 'A', 'B') != -1 {
			b.Fatal("unexpected hit")
		}
	}
}
