package ahocorasick

// xbench_export_test.go — cross-language benchmark corpus exporter.
//
// Writes every (dictionary, haystack) pair used by the Go benchmark matrix
// to a directory, together with a manifest carrying expected match counts
// and an order-independent hash of the match set (start, len) computed by
// this fork's own Match. A foreign harness (the Rust one in ac-xbench)
// replays the same pairs and must reproduce count and hash exactly before
// any timing is trusted.
//
// The generators below call the same in-package helpers the benchmarks use
// (stride10k, spreadDict, synthHaystack, readPatterns), so corpora are
// byte-identical to what the Go benchmarks scan.
//
// Gated behind XBENCH_EXPORT_DIR so `go test ./...` never runs it:
//
//	XBENCH_EXPORT_DIR=/path/to/ac-xbench/corpora go test -run TestXBenchExport -count=1 .

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"math/rand"
)

type xbDict struct {
	DedupFile string `json:"dedup_file"`
	RawFile   string `json:"raw_file,omitempty"`
	NRaw      int    `json:"n_raw"`
	NDedup    int    `json:"n_dedup"`
	States    int    `json:"go_states"`
	StopBytes int    `json:"go_stop_bytes"`
	FT16      bool   `json:"go_ft16"`
	FTC       bool   `json:"go_ftc"`
}

type xbHay struct {
	File string `json:"file"`
	Len  int    `json:"len"`
}

type xbPair struct {
	Bench      string `json:"bench"`
	Dict       string `json:"dict"`
	Hay        string `json:"hay,omitempty"`
	HayLen     int    `json:"hay_len,omitempty"`
	Mode       string `json:"mode"` // match | walk | first | build
	Count      int64  `json:"count,omitempty"`
	Hash       string `json:"hash,omitempty"`
	FirstStart int64  `json:"first_start,omitempty"`
	FirstLen   int64  `json:"first_len,omitempty"`
}

type xbManifest struct {
	Dicts     map[string]*xbDict `json:"dicts"`
	Haystacks map[string]*xbHay  `json:"haystacks"`
	Pairs     []*xbPair          `json:"pairs"`
}

// splitmix64 must match the Rust harness implementation bit-for-bit.
func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// matchSetHash is an order-independent hash of the {(start,len)} multiset.
func matchSetHash(ms []*Match) (int64, string) {
	var sum uint64
	for _, m := range ms {
		start := uint64(m.Pos())
		l := uint64(len(m.Match()))
		sum += splitmix64(start<<20 | l)
	}
	return int64(len(ms)), fmt.Sprintf("%016x", sum)
}

// dedupKeepOrder removes duplicate patterns, keeping first occurrence.
// The fork's trie dedups by construction; the Rust library assigns each
// duplicate its own pattern ID and would emit extra overlapping matches.
func dedupKeepOrder(pats []string) []string {
	seen := make(map[string]struct{}, len(pats))
	out := make([]string, 0, len(pats))
	for _, p := range pats {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func TestXBenchExport(t *testing.T) {
	outDir := os.Getenv("XBENCH_EXPORT_DIR")
	if outDir == "" {
		t.Skip("XBENCH_EXPORT_DIR not set")
	}
	for _, d := range []string{"dicts", "haystacks"} {
		if err := os.MkdirAll(filepath.Join(outDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	man := &xbManifest{
		Dicts:     map[string]*xbDict{},
		Haystacks: map[string]*xbHay{},
	}

	// ---- Load base data via the same loader the Lab benchmarks use. ----
	patterns, err := readPatterns("./test_data/NSF-ordlisten.cleaned.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range patterns {
		if p == "" {
			t.Fatalf("empty pattern at line %d: loaders would diverge", i)
		}
	}
	ibsen, err := os.ReadFile("./test_data/Ibsen.txt")
	if err != nil {
		t.Fatal(err)
	}
	gpl, err := os.ReadFile("./test_data/gpl.txt")
	if err != nil {
		t.Fatal(err)
	}
	opphav, err := os.ReadFile("./test_data/opphavsrett.txt")
	if err != nil {
		t.Fatal(err)
	}

	// ---- Dictionaries (mirroring every benchmark's pattern selection). ----
	tries := map[string]*Trie{}

	addDict := func(name string, raw []string, exportRaw bool) {
		dd := dedupKeepOrder(raw)
		df := filepath.Join("dicts", name+".txt")
		if err := os.WriteFile(filepath.Join(outDir, df), []byte(strings.Join(dd, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		d := &xbDict{DedupFile: df, NRaw: len(raw), NDedup: len(dd)}
		if exportRaw {
			rf := filepath.Join("dicts", name+".raw.txt")
			if err := os.WriteFile(filepath.Join(outDir, rf), []byte(strings.Join(raw, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			d.RawFile = rf
		}
		tr := NewTrieBuilder().AddStrings(raw).Build()
		tries[name] = tr
		d.States = len(tr.failTrans)
		d.StopBytes = countStop(tr)
		d.FT16 = tr.failTrans16 != nil
		d.FTC = tr.failTransC != nil
		man.Dicts[name] = d
	}

	// aWords: first 10000 words starting with 'a' (RootSkip single-byte sweep).
	var aWords []string
	for _, w := range patterns {
		if w[0] == 'a' {
			aWords = append(aWords, w)
			if len(aWords) == 10000 {
				break
			}
		}
	}
	// abcde10k: 2000 words per first letter a..e (RootSkip multi-byte sweep).
	byFirst := map[byte][]string{}
	for _, w := range patterns {
		byFirst[w[0]] = append(byFirst[w[0]], w)
	}
	var abcde []string
	for _, c := range []byte{'a', 'b', 'c', 'd', 'e'} {
		ws := byFirst[c]
		if len(ws) > 2000 {
			ws = ws[:2000]
		}
		abcde = append(abcde, ws...)
	}
	// skip2: first 1000 'k' words + first 1000 'v' words (LabSkip2).
	var ks, vs []string
	for _, p := range patterns {
		if p[0] == 'k' && len(ks) < 1000 {
			ks = append(ks, p)
		} else if p[0] == 'v' && len(vs) < 1000 {
			vs = append(vs, p)
		}
	}
	skip2 := append(append([]string{}, ks...), vs...)
	// multismall3k: every 33rd word, cap 3000 (LabMultiSmall).
	var msmall []string
	for i := 0; i < len(patterns) && len(msmall) < 3000; i += 33 {
		msmall = append(msmall, patterns[i])
	}

	addDict("sorted100", patterns[:100], true)
	addDict("sorted1k", patterns[:1000], true)
	addDict("sorted10k", patterns[:10000], true)
	addDict("big100k", patterns[:100000], true)
	addDict("stride10k", stride10k(patterns), false)
	addDict("even10k", spreadDict(patterns, 10000), false)
	addDict("even1k", spreadDict(patterns, 1000), false)
	addDict("even100", spreadDict(patterns, 100), false)
	addDict("multismall3k", msmall, false)
	addDict("skip2", skip2, false)
	addDict("awords10k", aWords, false)
	addDict("abcde10k", abcde, false)
	addDict("dense6", []string{"a", "ab", "aba", "abab", "b", "ba"}, false)
	oh16 := make([]string, 16)
	for i := range oh16 {
		oh16[i] = strings.Repeat("a", i+1)
	}
	addDict("oh16", oh16, false)
	addDict("anchor", []string{"Hedvig"}, false)
	addDict("late", []string{"imorges"}, false)

	// Cardinality-sweep dictionaries (RootSkip_CardinalitySweep).
	letters := []byte("abcdefghijklmnopqrstuvwxyz")
	cardDicts := map[int][]string{}
	for _, card := range []int{1, 2, 5, 13, 26} {
		var pats []string
		per := 10000 / card
		got := 0
		for _, l := range letters {
			if got == card {
				break
			}
			ws := byFirst[l]
			if len(ws) == 0 {
				continue
			}
			if len(ws) > per {
				ws = ws[:per]
			}
			pats = append(pats, ws...)
			got++
		}
		if got < card {
			continue
		}
		cardDicts[card] = pats
		addDict(fmt.Sprintf("card%d", card), pats, false)
	}

	// ---- Haystacks. ----
	addHay := func(name string, data []byte) {
		f := filepath.Join("haystacks", name+".bin")
		if err := os.WriteFile(filepath.Join(outDir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
		man.Haystacks[name] = &xbHay{File: f, Len: len(data)}
	}
	hays := map[string][]byte{}
	reg := func(name string, data []byte) []byte {
		hays[name] = data
		addHay(name, data)
		return data
	}

	reg("ibsen", ibsen)
	reg("gpl", gpl)
	reg("opphavsrett", opphav)

	big8m := make([]byte, 0, 8<<20)
	for len(big8m) < 8<<20 {
		big8m = append(big8m, ibsen...)
	}
	reg("ibsen8m", big8m)

	digits := func(n int, seed int64) []byte {
		rng := rand.New(rand.NewSource(seed))
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('0' + rng.Intn(10))
		}
		return b
	}
	reg("digits1m_s7", digits(1<<20, 7))
	reg("digits8m_s11", digits(8<<20, 11))
	reg("digits100k_s3", digits(100000, 3))

	ab := make([]byte, 65536)
	for i := range ab {
		if i%2 == 0 {
			ab[i] = 'a'
		} else {
			ab[i] = 'b'
		}
	}
	reg("ab64k", ab)

	var concatSorted []byte
	for i := 0; len(concatSorted) < 256<<10; i++ {
		concatSorted = append(concatSorted, patterns[i%10000]...)
	}
	reg("concat_sorted", concatSorted)

	strideSel := stride10k(patterns)
	var concatStride []byte
	for i := 0; len(concatStride) < 64<<10; i++ {
		concatStride = append(concatStride, strideSel[i%len(strideSel)]...)
	}
	reg("concat_stride", concatStride)

	// Gap sweep (LabDensitySweep): word + 'x'*gap, 96KiB, labeled with density.
	gapDensityLabel := map[int]string{}
	for _, gap := range []int{0, 4, 8, 16, 32, 64} {
		var sb []byte
		i := 0
		for len(sb) < 96<<10 {
			sb = append(sb, patterns[i%10000]...)
			for g := 0; g < gap; g++ {
				sb = append(sb, 'x')
			}
			i++
		}
		input := sb[:96<<10]
		c := tries["sorted10k"].rootStopBytes[0]
		density := float64(bytes.Count(input, []byte{c})) / float64(len(input))
		gapDensityLabel[gap] = fmt.Sprintf("gap%d-d%.3f", gap, density)
		reg(fmt.Sprintf("gap%d_96k", gap), input)
	}

	// Synthetic density sweeps (RootSkip_DensitySweep_*).
	for _, pct := range []int{1, 2, 4, 8, 16, 32, 64, 100} {
		reg(fmt.Sprintf("synth1_%dpct", pct), synthHaystack(262144, []byte{'a'}, float64(pct)/100, 1))
		reg(fmt.Sprintf("synth5_%dpct", pct), synthHaystack(262144, []byte{'a', 'b', 'c', 'd', 'e'}, float64(pct)/100, 2))
	}

	reg("oh_a256k", bytes.Repeat([]byte{'a'}, 1<<18))

	ohWords := func(pats []string) []byte {
		var sb strings.Builder
		for i := 0; sb.Len() < 1<<18; i = (i + 7919) % len(pats) {
			sb.WriteString(pats[i])
			sb.WriteByte(' ')
		}
		return []byte(sb.String())
	}
	reg("oh_words", ohWords(patterns[:10000]))
	reg("oh_spread", ohWords(spreadDict(patterns, 10000)))

	// ---- Pairs (one per Go benchmark row, names mirrored exactly). ----
	addPair := func(bench, dict, hay string, hayLen int, mode string) {
		p := &xbPair{Bench: bench, Dict: dict, Hay: hay, HayLen: hayLen, Mode: mode}
		if mode == "match" || mode == "walk" {
			ms := tries[dict].Match(hays[hay][:hayLen])
			p.Count, p.Hash = matchSetHash(ms)
			tries[dict].ReleaseMatches(ms)
		}
		if mode == "first" {
			m := tries[dict].MatchFirst(hays[hay][:hayLen])
			if m == nil {
				t.Fatalf("%s: expected a first match", bench)
			}
			p.FirstStart = int64(m.Pos())
			p.FirstLen = int64(len(m.Match()))
		}
		man.Pairs = append(man.Pairs, p)
	}
	addBuild := func(bench, dict string) {
		man.Pairs = append(man.Pairs, &xbPair{Bench: bench, Dict: dict, Mode: "build"})
	}

	// Lab matrix.
	for _, s := range []struct {
		label string
		n     int
	}{{"ibsen-1k", 1000}, {"ibsen-4k", 4000}, {"ibsen-100k", 100000}} {
		addPair("BenchmarkLabSingleStop/"+s.label, "sorted10k", "ibsen", s.n, "match")
		addPair("BenchmarkLabMultiStop/"+s.label, "stride10k", "ibsen", s.n, "match")
		addPair("BenchmarkLabMultiSmall/"+s.label, "multismall3k", "ibsen", s.n, "match")
	}
	addPair("BenchmarkLabBig/ibsen-100k", "big100k", "ibsen", 100000, "match")
	addPair("BenchmarkLabNoMatch/single-stop-100k", "sorted10k", "digits1m_s7", 100000, "match")
	addPair("BenchmarkLabNoMatch/multi-stop-100k", "stride10k", "digits1m_s7", 100000, "match")
	addPair("BenchmarkLabDense/ab-64k", "dense6", "ab64k", 65536, "match")
	addPair("BenchmarkLabWalk/single-100k", "sorted10k", "ibsen", 100000, "walk")
	addPair("BenchmarkLabWalk/multi-100k", "stride10k", "ibsen", 100000, "walk")
	addPair("BenchmarkLabMatchFirstLate", "late", "ibsen", 100000, "first")
	addBuild("BenchmarkLabBuild10k", "sorted10k")
	for _, size := range []int{16 << 10, 32 << 10, 48 << 10, 64 << 10, 96 << 10, 128 << 10, 1 << 19, 1 << 21, 1 << 23} {
		addPair(fmt.Sprintf("BenchmarkLabParaCross/%dk", size>>10), "sorted10k", "ibsen8m", size, "match")
	}
	for _, size := range []int{16 << 10, 32 << 10, 64 << 10, 128 << 10, 256 << 10, 512 << 10} {
		addPair(fmt.Sprintf("BenchmarkLabParaCrossMulti/%dk", size>>10), "stride10k", "ibsen8m", size, "match")
	}
	for _, size := range []int{4 << 10, 64 << 10, 256 << 10} {
		addPair(fmt.Sprintf("BenchmarkLabInAutomaton/%dk", size>>10), "sorted10k", "concat_sorted", size, "match")
	}
	for _, gap := range []int{0, 4, 8, 16, 32, 64} {
		addPair("BenchmarkLabDensitySweep/"+gapDensityLabel[gap], "sorted10k", fmt.Sprintf("gap%d_96k", gap), 96<<10, "match")
	}
	for _, size := range []int{2 << 10, 4 << 10, 8 << 10, 16 << 10, 32 << 10, 64 << 10, 96 << 10, 256 << 10} {
		addPair(fmt.Sprintf("BenchmarkLabDenseSize/%dk", size>>10), "sorted10k", "concat_sorted", size, "match")
	}
	for _, size := range []int{4 << 10, 16 << 10, 31 << 10} {
		addPair(fmt.Sprintf("BenchmarkLabMultiDense/%dk", size>>10), "stride10k", "concat_stride", size, "match")
	}
	addPair("BenchmarkLabSkip2/nomatch-100k", "skip2", "digits100k_s3", 100000, "match")
	addPair("BenchmarkLabSkip2/ibsen-100k", "skip2", "ibsen", 100000, "match")
	for _, size := range []int{1 << 20, 8 << 20} {
		addPair(fmt.Sprintf("BenchmarkLabNoMatchBig/single-%dm", size>>20), "sorted10k", "digits8m_s11", size, "match")
		addPair(fmt.Sprintf("BenchmarkLabNoMatchBig/multi-%dm", size>>20), "stride10k", "digits8m_s11", size, "match")
	}
	addPair("BenchmarkLabAnchor/ibsen-100k", "anchor", "ibsen", 100000, "match")

	// Pub matrix.
	for _, size := range []int{1 << 10, 4 << 10, 100 << 10} {
		addPair(fmt.Sprintf("BenchmarkPubText_Sorted10k/%dk", size>>10), "sorted10k", "ibsen", size, "match")
	}
	for _, size := range []int{4 << 10, 100 << 10} {
		addPair(fmt.Sprintf("BenchmarkPubText_Spread10k/%dk", size>>10), "stride10k", "ibsen", size, "match")
	}
	addPair("BenchmarkPubText_Big100k/100k", "big100k", "ibsen", 100<<10, "match")
	for _, size := range []int{512 << 10, 2 << 20, 8 << 20} {
		addPair(fmt.Sprintf("BenchmarkPubLarge_Sorted10k/%dk", size>>10), "sorted10k", "ibsen8m", size, "match")
	}
	addPair("BenchmarkPubNoMatch/sorted-100k", "sorted10k", "digits1m_s7", 100<<10, "match")
	addPair("BenchmarkPubNoMatch/sorted-1m", "sorted10k", "digits1m_s7", 1<<20, "match")
	addPair("BenchmarkPubNoMatch/spread-100k", "stride10k", "digits1m_s7", 100<<10, "match")
	addPair("BenchmarkPubNoMatch/spread-1m", "stride10k", "digits1m_s7", 1<<20, "match")
	addPair("BenchmarkPubDense/ab-64k", "dense6", "ab64k", 64<<10, "match")
	addPair("BenchmarkPubConcat/64k", "stride10k", "concat_stride", 64<<10, "match")
	addPair("BenchmarkPubWalk/sorted-100k", "sorted10k", "ibsen", 100<<10, "walk")
	addPair("BenchmarkPubWalk/spread-100k", "stride10k", "ibsen", 100<<10, "walk")
	addPair("BenchmarkPubMatchFirstLate", "late", "ibsen", 100<<10, "first")
	addBuild("BenchmarkPubBuild/1000", "sorted1k")
	addBuild("BenchmarkPubBuild/10000", "sorted10k")
	addBuild("BenchmarkPubBuild/100000", "big100k")

	// RootSkip matrix.
	for _, d := range []struct{ label, dict string }{
		{"sorted10k_1stopbyte", "sorted10k"},
		{"spread10k_multistop", "even10k"},
	} {
		for _, h := range []struct{ label, hay string }{
			{"Ibsen_no", "ibsen"}, {"GPL_en", "gpl"}, {"opphavsrett_no", "opphavsrett"},
		} {
			addPair("BenchmarkRootSkip_RealCorpora/"+d.label+"/"+h.label, d.dict, h.hay, len(hays[h.hay]), "match")
		}
	}
	for _, pct := range []int{1, 2, 4, 8, 16, 32, 64, 100} {
		addPair(fmt.Sprintf("BenchmarkRootSkip_DensitySweep_SingleByte/density_%dpct", pct),
			"awords10k", fmt.Sprintf("synth1_%dpct", pct), 262144, "match")
		addPair(fmt.Sprintf("BenchmarkRootSkip_DensitySweep_MultiByte/density_%dpct", pct),
			"abcde10k", fmt.Sprintf("synth5_%dpct", pct), 262144, "match")
	}
	for _, card := range []int{1, 2, 5, 13, 26} {
		pats, ok := cardDicts[card]
		if !ok {
			continue
		}
		var stop [256]bool
		for _, p := range pats {
			stop[p[0]] = true
		}
		stopCount := 0
		for _, c := range ibsen {
			if stop[c] {
				stopCount++
			}
		}
		densityPct := stopCount * 100 / len(ibsen)
		addPair(fmt.Sprintf("BenchmarkRootSkip_CardinalitySweep/firstbytes_%d/density_%dpct", card, densityPct),
			fmt.Sprintf("card%d", card), "ibsen", len(ibsen), "match")
	}

	// Midsize + OutputHeavy.
	addPair("BenchmarkMidsize_Spread1k_Ibsen", "even1k", "ibsen", len(ibsen), "match")
	addPair("BenchmarkMidsize_Spread1k_GPL", "even1k", "gpl", len(gpl), "match")
	addPair("BenchmarkMidsize_Spread100_Ibsen", "even100", "ibsen", len(ibsen), "match")
	addPair("BenchmarkOutputHeavy_Extreme", "oh16", "oh_a256k", 1<<18, "match")
	addPair("BenchmarkOutputHeavy_DenseWords", "sorted10k", "oh_words", len(hays["oh_words"]), "match")
	addPair("BenchmarkOutputHeavy_DenseSpread", "even10k", "oh_spread", len(hays["oh_spread"]), "match")

	// Legacy trie_test benchmarks.
	addBuild("BenchmarkTrieBuild/100", "sorted100")
	addBuild("BenchmarkTrieBuild/1000", "sorted1k")
	addBuild("BenchmarkTrieBuild/10000", "sorted10k")
	addBuild("BenchmarkTrieBuild/100000", "big100k")
	for _, n := range []int{100, 1000, 10000} {
		addPair(fmt.Sprintf("BenchmarkMatchIbsen/%d", n), "sorted10k", "ibsen", n, "match")
	}

	// Sanity: Walk and Match must agree on the match set for one pair.
	// Compare the full (start, length) multiset fingerprint, not just
	// counts, so a Walk regression in positions or lengths fails here.
	{
		var cnt int64
		var sum uint64
		tries["sorted10k"].Walk(ibsen[:100000], func(end, l, _ uint32) bool {
			cnt++
			start := uint64(end - l + 1)
			sum += splitmix64(start<<20 | uint64(l))
			return true
		})
		ms := tries["sorted10k"].Match(ibsen[:100000])
		matchCount, matchHash := matchSetHash(ms)
		walkHash := fmt.Sprintf("%016x", sum)
		if cnt != matchCount || walkHash != matchHash {
			t.Fatalf("Walk (%d, %s) and Match (%d, %s) disagree",
				cnt, walkHash, matchCount, matchHash)
		}
		tries["sorted10k"].ReleaseMatches(ms)
	}

	out, err := json.MarshalIndent(man, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("exported %d dicts, %d haystacks, %d pairs to %s",
		len(man.Dicts), len(man.Haystacks), len(man.Pairs), outDir)
}
