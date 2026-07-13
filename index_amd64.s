// SSE2 search kernels: the amd64 counterpart of index_arm64.s, same
// contracts. 32 input bytes per iteration via two 16-byte compares;
// PMOVMSKB folds each compare into a 16-bit mask (one bit per byte, in
// input order), so a combined 32-bit mask plus BSF locates the first
// hit exactly. SSE2 only — available on every amd64 CPU, no feature
// detection needed.
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
	MOVQ	p+0(FP), SI
	MOVQ	m+8(FP), CX
	MOVBLZX	a+16(FP), AX
	MOVBLZX	b+17(FP), BX
	MOVQ	d+24(FP), DX
	LEAQ	(SI)(DX*1), DI  // stream B cursor = p + d

	// Broadcast a into X0, b into X1 (bytealg idiom).
	MOVD	AX, X0
	PUNPCKLBW	X0, X0
	PUNPCKLBW	X0, X0
	PSHUFL	$0, X0, X0
	MOVD	BX, X1
	PUNPCKLBW	X1, X1
	PUNPCKLBW	X1, X1
	PSHUFL	$0, X1, X1

	XORQ	R10, R10        // positions consumed

loop:
	MOVQ	CX, R9
	SUBQ	R10, R9         // remaining positions
	CMPQ	R9, $32
	JLT	notfound
	MOVOU	(SI), X2
	MOVOU	16(SI), X3
	MOVOU	(DI), X4
	MOVOU	16(DI), X5
	PCMPEQB	X0, X2          // stream A == a, bytes 0-15
	PCMPEQB	X0, X3          // bytes 16-31
	PCMPEQB	X1, X4          // stream B == b, bytes 0-15
	PCMPEQB	X1, X5          // bytes 16-31
	PAND	X4, X2          // pair condition
	PAND	X5, X3
	PMOVMSKB	X2, R11
	PMOVMSKB	X3, R12
	SHLL	$16, R12
	ORL	R12, R11        // 32-bit mask, one bit per position
	TESTL	R11, R11
	JNZ	found
	ADDQ	$32, SI
	ADDQ	$32, DI
	ADDQ	$32, R10
	JMP	loop

found:
	BSFL	R11, R11
	LEAQ	(R10)(R11*1), AX
	MOVQ	AX, ret+32(FP)
	RET

notfound:
	MOVQ	$-1, AX
	MOVQ	AX, ret+32(FP)
	RET

// func indexOr2Asm(p *byte, m int, a byte, b byte) int
//
// Returns the smallest i in [0, m&^31) with p[i] == a || p[i] == b, or
// -1 if there is none. Reads p[0 : m&^31] only.
TEXT ·indexOr2Asm(SB), NOSPLIT, $0-32
	MOVQ	p+0(FP), SI
	MOVQ	m+8(FP), CX
	MOVBLZX	a+16(FP), AX
	MOVBLZX	b+17(FP), BX

	MOVD	AX, X0
	PUNPCKLBW	X0, X0
	PUNPCKLBW	X0, X0
	PSHUFL	$0, X0, X0
	MOVD	BX, X1
	PUNPCKLBW	X1, X1
	PUNPCKLBW	X1, X1
	PSHUFL	$0, X1, X1

	XORQ	R10, R10

loop:
	MOVQ	CX, R9
	SUBQ	R10, R9
	CMPQ	R9, $32
	JLT	notfound
	MOVOU	(SI), X2
	MOVOU	16(SI), X3
	MOVOA	X2, X4
	MOVOA	X3, X5
	PCMPEQB	X0, X2          // == a
	PCMPEQB	X0, X3
	PCMPEQB	X1, X4          // == b
	PCMPEQB	X1, X5
	POR	X4, X2          // either value
	POR	X5, X3
	PMOVMSKB	X2, R11
	PMOVMSKB	X3, R12
	SHLL	$16, R12
	ORL	R12, R11
	TESTL	R11, R11
	JNZ	found
	ADDQ	$32, SI
	ADDQ	$32, R10
	JMP	loop

found:
	BSFL	R11, R11
	LEAQ	(R10)(R11*1), AX
	MOVQ	AX, ret+24(FP)
	RET

notfound:
	MOVQ	$-1, AX
	MOVQ	AX, ret+24(FP)
	RET
