//go:build (!arm64 && !amd64) || purego

package ahocorasick

// No vector search kernels on this build: the scan paths use the
// portable strategies (rare-byte IndexByte, SWAR pair scan, windowed
// per-value IndexByte). The stubs below are never called; call sites
// are gated on the constant and eliminated at compile time.
const (
	hasPairKernel = false
	hasOr2Kernel  = false
)

func indexPair2(p []byte, m int, a, b byte, d int) int { panic("unreachable") }

func indexOr2(p []byte, m int, a, b byte) int { panic("unreachable") }
