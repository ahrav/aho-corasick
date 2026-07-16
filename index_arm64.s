// NEON search kernels for the single-pattern fast path and the two-value
// root skip. Both follow the internal/bytealg syndrome idiom: compare 32
// input bytes per iteration, AND the match mask with the magic constant
// 0x40100401 (bytes of each 4-byte group get distinct bits 1/4/16/64),
// then two pairwise adds fold the 256-bit mask into a 64-bit syndrome
// with two bits per input byte, in input order. RBIT+CLZ converts the
// first set bit into the matching byte's offset.
//
// Neither kernel reads past its caller-proven region: both process only
// full 32-byte blocks and report "no hit in the full blocks" with -1;
// the Go callers finish the < 32-byte tail with scalar code.

//go:build !purego

#include "textflag.h"

// func indexPair2Asm(p *byte, m int, a byte, b byte, d int) int
//
// Returns the smallest i in [0, m&^31) with p[i] == a && p[i+d] == b,
// or -1 if there is none. The caller guarantees p[0 : m&^31 + d] is
// readable (stream B reads p[d : d+(m&^31)]).
TEXT ·indexPair2Asm(SB), NOSPLIT, $0-40
	MOVD	p+0(FP), R0
	MOVD	m+8(FP), R1
	MOVBU	a+16(FP), R2
	MOVBU	b+17(FP), R3
	MOVD	d+24(FP), R4
	ADD	R0, R4, R4      // stream B cursor = p + d
	VMOV	R2, V0.B16      // broadcast a
	VMOV	R3, V7.B16      // broadcast b
	MOVD	$0x40100401, R5
	VMOV	R5, V5.S4       // syndrome magic
	MOVD	ZR, R6          // positions consumed

loop:
	SUB	R6, R1, R7      // remaining positions
	CMP	$32, R7
	BLT	notfound
	VLD1.P	32(R0), [V1.B16, V2.B16]
	VLD1.P	32(R4), [V3.B16, V4.B16]
	VCMEQ	V0.B16, V1.B16, V1.B16
	VCMEQ	V0.B16, V2.B16, V2.B16
	VCMEQ	V7.B16, V3.B16, V3.B16
	VCMEQ	V7.B16, V4.B16, V4.B16
	VAND	V3.B16, V1.B16, V3.B16  // pair condition, bytes 0-15
	VAND	V4.B16, V2.B16, V4.B16  // pair condition, bytes 16-31
	// Cheap existence check in the hot loop (bytealg idiom); the
	// positional syndrome is computed only after a hit.
	VORR	V4.B16, V3.B16, V6.B16
	VADDP	V6.D2, V6.D2, V6.D2
	VMOV	V6.D[0], R8
	CBNZ	R8, found
	ADD	$32, R6
	B	loop

found:
	VAND	V5.B16, V3.B16, V3.B16
	VAND	V5.B16, V4.B16, V4.B16
	VADDP	V4.B16, V3.B16, V6.B16  // 256 -> 128
	VADDP	V6.B16, V6.B16, V6.B16  // 128 -> 64
	VMOV	V6.D[0], R8
	RBIT	R8, R8
	CLZ	R8, R8
	ADD	R8>>1, R6, R0   // index = consumed + syndrome-bit/2
	MOVD	R0, ret+32(FP)
	RET

notfound:
	MOVD	$-1, R0
	MOVD	R0, ret+32(FP)
	RET

// func indexOr2Asm(p *byte, m int, a byte, b byte) int
//
// Returns the smallest i in [0, m&^31) with p[i] == a || p[i] == b, or
// -1 if there is none. Reads p[0 : m&^31] only.
TEXT ·indexOr2Asm(SB), NOSPLIT, $0-32
	MOVD	p+0(FP), R0
	MOVD	m+8(FP), R1
	MOVBU	a+16(FP), R2
	MOVBU	b+17(FP), R3
	VMOV	R2, V0.B16
	VMOV	R3, V7.B16
	MOVD	$0x40100401, R5
	VMOV	R5, V5.S4
	MOVD	ZR, R6

loop:
	SUB	R6, R1, R7
	CMP	$32, R7
	BLT	notfound
	VLD1.P	32(R0), [V1.B16, V2.B16]
	VCMEQ	V0.B16, V1.B16, V3.B16
	VCMEQ	V0.B16, V2.B16, V4.B16
	VCMEQ	V7.B16, V1.B16, V8.B16
	VCMEQ	V7.B16, V2.B16, V9.B16
	VORR	V8.B16, V3.B16, V3.B16
	VORR	V9.B16, V4.B16, V4.B16
	// Cheap existence check; positional syndrome only after a hit.
	VORR	V4.B16, V3.B16, V6.B16
	VADDP	V6.D2, V6.D2, V6.D2
	VMOV	V6.D[0], R8
	CBNZ	R8, found
	ADD	$32, R6
	B	loop

found:
	VAND	V5.B16, V3.B16, V3.B16
	VAND	V5.B16, V4.B16, V4.B16
	VADDP	V4.B16, V3.B16, V6.B16
	VADDP	V6.B16, V6.B16, V6.B16
	VMOV	V6.D[0], R8
	RBIT	R8, R8
	CLZ	R8, R8
	ADD	R8>>1, R6, R0
	MOVD	R0, ret+24(FP)
	RET

notfound:
	MOVD	$-1, R0
	MOVD	R0, ret+24(FP)
	RET
