package ahocorasick

import (
	"fmt"
	"io/ioutil"
	"testing"
)

// TestCharacterize prints structural facts about the benchmark automaton.
// Lab-only diagnostic, not part of the upstream suite.
func TestCharacterize(t *testing.T) {
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		t.Skip(err)
	}
	for _, n := range []int{100, 1000, 10000, 100000} {
		tr := NewTrieBuilder().AddStrings(patterns[:n]).Build()
		fmt.Printf("patterns=%d states=%d failTrans16=%v stopBytes=%d maxLen=%d\n",
			n, len(tr.failTrans), tr.failTrans16 != nil, countStop(tr), tr.maxLen)
	}

	// Which scan path does BenchmarkMatchIbsen take?
	tr := NewTrieBuilder().AddStrings(patterns[:10000]).Build()
	fmt.Printf("10k: rootStopBytes=%d (single-stop specializations %v)\n",
		len(tr.rootStopBytes), len(tr.rootStopBytes) == 1)

	ibsen, err := ioutil.ReadFile("./test_data/Ibsen.txt")
	if err != nil {
		t.Skip(err)
	}
	ms := tr.Match(ibsen)
	fmt.Printf("ibsen len=%d matches(all)=%d\n", len(ibsen), len(ms))
	// match density on the 100k slice
	ms2 := tr.Match(ibsen[:100000])
	fmt.Printf("ibsen[:100000] matches=%d\n", len(ms2))

	// how many bytes leave root (stop bytes) for 10k?
	stops := 0
	for b := 0; b < 256; b++ {
		if tr.rootStop[b] == 1 {
			stops++
		}
	}
	fmt.Printf("10k stop bytes=%d\n", stops)

	// emit stats: how many states emit, dictLink chain depth histogram
	emit, deep := 0, 0
	for s := range tr.dict {
		if tr.dict[s] != 0 {
			emit++
		}
		d := 0
		for u := tr.dictLink[s]; u != nilState; u = tr.dictLink[u] {
			d++
		}
		if d > deep {
			deep = d
		}
	}
	fmt.Printf("10k emitting states=%d/%d maxDictChain=%d\n", emit, len(tr.dict), deep)
}

func countStop(tr *Trie) int {
	n := 0
	for b := 0; b < 256; b++ {
		if tr.rootStop[b] == 1 {
			n++
		}
	}
	return n
}
