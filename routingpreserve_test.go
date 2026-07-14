package ahocorasick

// routingpreserve_test.go - tripwire for the dual-vs-single routing
// verdict. chainSample's budget scales with input size
// (chainSampleSmallMax), so this asserts the ROUTING VERDICT (not the
// raw votes) on the corpus shapes whose routing the calibration
// constants promise, at sizes on both sides of the budget threshold. A
// sampling change that flips one of these mis-routes a scan family:
// word-like dense input loses the dual-cursor win, or shallow-chain
// dense input loses the single-cursor win (measured 1.4x on that
// shape).

import (
	"os"
	"testing"
)

func TestRoutingPreserved(t *testing.T) {
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		t.Fatal(err)
	}
	ibsen := mustRead(t, "./test_data/Ibsen.txt")
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()

	var wordlike []byte
	for i := 0; len(wordlike) < 256<<10; i++ {
		wordlike = append(wordlike, patterns[i%10000]...)
	}
	fs := spFalseStartCorpus(tr.rootStopBytes[0], 256<<10)

	// Routing the calibration constants promise; must hold at both
	// sampling budgets (below and at-or-above chainSampleSmallMax).
	for _, c := range []struct {
		name  string
		input []byte
		want  bool
	}{
		// Long-chain dense input routes dual at every scale, chunk or whole.
		{"wordlike-12k", wordlike[:12<<10], true},
		{"wordlike-16k", wordlike[:16<<10], true},
		{"wordlike-32k", wordlike[:32<<10], true},
		{"wordlike-96k", wordlike[:96<<10], true},
		// Depth-1 excursions route single despite maximal density.
		{"falsestart-12k", fs[:12<<10], false},
		{"falsestart-16k", fs[:16<<10], false},
		{"falsestart-96k", fs[:96<<10], false},
		// Natural text (short chains, sparse-ish) routes single.
		{"ibsen-12k", ibsen[:12<<10], false},
		{"ibsen-48k", ibsen[:48<<10], false},
		{"ibsen-96k", ibsen[:96<<10], false},
	} {
		if got := tr.dualWorthwhile(c.input); got != c.want {
			t.Errorf("%s: dualWorthwhile=%v, want %v (routing flipped)", c.name, got, c.want)
		}
	}

	// Characterization only: separator-broken corpora sit near the vote
	// thresholds, so their verdicts are logged rather than asserted;
	// compare against BenchmarkSPGray when a flip appears.
	for _, gap := range []int{2, 4, 8} {
		var sb []byte
		i := 0
		for len(sb) < 96<<10 {
			sb = append(sb, patterns[i%10000]...)
			for g := 0; g < gap; g++ {
				sb = append(sb, 'x')
			}
			i++
		}
		for _, size := range []int{12 << 10, 96 << 10} {
			t.Logf("gray gap%d %dKiB: dualWorthwhile=%v",
				gap, size>>10, tr.dualWorthwhile(sb[:size]))
		}
	}

}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
