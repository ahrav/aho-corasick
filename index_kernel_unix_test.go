//go:build (arm64 || amd64) && !purego && unix

package ahocorasick

// Guard-page proof of the kernels' read contracts: indexPair2 reads
// p[0 : m&^31 + d], indexOr2 reads p[0 : m&^31]. The buffers end exactly
// at a PROT_NONE page, so one byte of overread segfaults the test.
// Separate file: mmap/mprotect exist only on unix.

import (
	"syscall"
	"testing"
)

// guardedBuf returns a slice of n bytes whose LAST byte sits immediately
// before an inaccessible page: any read past p[n-1] faults deterministically.
func guardedBuf(t *testing.T, n int) []byte {
	t.Helper()
	pg := syscall.Getpagesize()
	pages := (n+pg-1)/pg + 1
	mem, err := syscall.Mmap(-1, 0, pages*pg, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Munmap(mem) })
	guard := mem[(pages-1)*pg:]
	if err := syscall.Mprotect(guard, syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	return mem[(pages-1)*pg-n : (pages-1)*pg]
}

func TestIndexKernelsGuardPage(t *testing.T) {
	for _, m := range []int{32, 33, 63, 64, 96, 127, 128, 4096} {
		for _, d := range []int{1, 7, 32, 33, 100} {
			n := m&^31 + d // exactly the documented readable region
			p := guardedBuf(t, n)
			for i := range p {
				p[i] = 'x'
			}
			_ = indexPair2(p, m, 'A', 'B', d)
			// Plant a hit in the last block to force full reads.
			if m&^31 > 0 {
				p[m&^31-1] = 'A'
				if m&^31-1+d < n {
					p[m&^31-1+d] = 'B'
				}
				_ = indexPair2(p, m, 'A', 'B', d)
			}
		}
		p := guardedBuf(t, m&^31)
		for i := range p {
			p[i] = 'x'
		}
		_ = indexOr2(p, m, 'A', 'B')
		if m&^31 > 0 {
			p[m&^31-1] = 'B'
			_ = indexOr2(p, m, 'A', 'B')
		}
	}
}
