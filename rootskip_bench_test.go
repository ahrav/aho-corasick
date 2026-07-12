package ahocorasick

// Benchmarks for the root self-loop skip, varying the two corpus properties
// that determine its effect on match throughput.
//
// BenchmarkMatchIbsen measures one operating point: patterns[:10000] of an
// alphabetically sorted word list all begin with 'a', so the root has one
// stop byte (Walk dispatches to walkStopByte) and Ibsen has 3.8% stop-byte
// density. It does not vary density and never reaches walkTable.
//
// The skip's effect depends on two properties, both swept here:
//
//   1. stop-byte density = fraction of haystack bytes that leave the root.
//      Low density leaves long self-loop runs the skip jumps over; at high
//      density the skip runs on nearly every byte and adds cost.
//   2. number of distinct pattern first bytes. One selects the walkStopByte
//      SWAR/IndexByte path; two or more select the walkTable OR-table path.
//
// The benchmarks use only the public API (NewTrieBuilder/AddStrings/Build/
// Match/ReleaseMatches), which is stable across the perf branch stack, so
// this file can be dropped onto any of those branches to compare them.

import (
	"bufio"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// benchReadLines loads up to n non-empty trimmed lines (n<=0 => all).
func benchReadLines(tb testing.TB, path string, n int) []string {
	tb.Helper()
	f, err := os.Open(path)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 1<<20), 1<<20)
	out := make([]string, 0, 1024)
	for s.Scan() && (n <= 0 || len(out) < n) {
		if t := strings.TrimSpace(s.Text()); t != "" {
			out = append(out, t)
		}
	}
	if err := s.Err(); err != nil {
		tb.Fatal(err)
	}
	return out
}

// benchReadFile reads a whole file as bytes.
func benchReadFile(tb testing.TB, path string) []byte {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// spreadDict returns n patterns evenly sampled across the full word list.
// The samples span the alphabet, so the trie has many root stop bytes and
// Match takes walkTable. The sorted prefix all[:n] instead shares one first
// letter and takes walkStopByte.
func spreadDict(all []string, n int) []string {
	if n >= len(all) {
		return all
	}
	step := len(all) / n
	if step < 1 {
		step = 1
	}
	out := make([]string, 0, n)
	for i := 0; i < len(all) && len(out) < n; i += step {
		out = append(out, all[i])
	}
	return out
}

// synthHaystack builds a length-n haystack with stop-byte density near
// `density`: each byte is a random stop byte with probability `density`,
// otherwise a space (spaces self-loop at the root for every pattern set here).
// The same `seed` produces the same haystack. Isolated stop bytes rarely form
// full matches, so the dict/dictLink work after leaving the root stays small
// and the measured difference between skip on and off is the skip itself.
func synthHaystack(n int, stopBytes []byte, density float64, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, n)
	for i := range buf {
		if rng.Float64() < density {
			buf[i] = stopBytes[rng.Intn(len(stopBytes))]
		} else {
			buf[i] = ' '
		}
	}
	return buf
}

func benchMatchOver(b *testing.B, trie *Trie, hay []byte) {
	b.SetBytes(int64(len(hay)))
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		m := trie.Match(hay)
		trie.ReleaseMatches(m)
	}
}

// BenchmarkRootSkip_RealCorpora matches a 10k-pattern trie over each real
// haystack twice: once with the sorted all-'a' slice (one stop byte,
// walkStopByte) and once with an evenly-spread dictionary (many stop bytes,
// walkTable). Same pattern count as BenchmarkMatchIbsen; both paths measured.
func BenchmarkRootSkip_RealCorpora(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	sortedDict := all[:10000]        // all begin with 'a' => walkStopByte
	spread := spreadDict(all, 10000) // spans the alphabet => walkTable

	haystacks := []struct{ name, path string }{
		{"Ibsen_no", "./test_data/Ibsen.txt"},
		{"GPL_en", "./test_data/gpl.txt"},
		{"opphavsrett_no", "./test_data/opphavsrett.txt"},
	}
	dicts := []struct {
		name string
		pats []string
	}{
		{"sorted10k_1stopbyte", sortedDict},
		{"spread10k_multistop", spread},
	}
	for _, d := range dicts {
		trie := NewTrieBuilder().AddStrings(d.pats).Build()
		for _, h := range haystacks {
			hay := benchReadFile(b, h.path)
			b.Run(d.name+"/"+h.name, func(b *testing.B) { benchMatchOver(b, trie, hay) })
		}
	}
}

// BenchmarkRootSkip_DensitySweep_SingleByte sweeps haystack stop-byte density
// from 1% to 100% with a walkStopByte trie (patterns all begin with 'a', one
// stop byte), locating the density at which the skip stops paying off.
func BenchmarkRootSkip_DensitySweep_SingleByte(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	// Words starting with 'a': one stop byte, guaranteed walkStopByte.
	var aWords []string
	for _, w := range all {
		if w[0] == 'a' {
			aWords = append(aWords, w)
			if len(aWords) == 10000 {
				break
			}
		}
	}
	trie := NewTrieBuilder().AddStrings(aWords).Build()
	const n = 262144
	for _, pct := range []int{1, 2, 4, 8, 16, 32, 64, 100} {
		hay := synthHaystack(n, []byte{'a'}, float64(pct)/100, 1)
		b.Run(itoaPct(pct), func(b *testing.B) { benchMatchOver(b, trie, hay) })
	}
}

// BenchmarkRootSkip_DensitySweep_MultiByte runs the same density sweep with a
// walkTable trie (patterns begin with a..e, five stop bytes), exercising the
// OR-table skip that BenchmarkMatchIbsen does not reach.
func BenchmarkRootSkip_DensitySweep_MultiByte(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	stop := []byte{'a', 'b', 'c', 'd', 'e'}
	// The word list is sorted, so take an equal share of words from each of
	// the five first letters. Collecting the first 10000 words that start
	// with any of a..e would take only 'a' words (there are far more than
	// 10000) and yield one stop byte, not five.
	byFirst := map[byte][]string{}
	for _, w := range all {
		if c := w[0]; c >= 'a' && c <= 'e' {
			byFirst[c] = append(byFirst[c], w)
		}
	}
	var pats []string
	per := 10000 / len(stop)
	for _, c := range stop {
		ws := byFirst[c]
		if len(ws) > per {
			ws = ws[:per]
		}
		pats = append(pats, ws...)
	}
	trie := NewTrieBuilder().AddStrings(pats).Build()
	const n = 262144
	for _, pct := range []int{1, 2, 4, 8, 16, 32, 64, 100} {
		hay := synthHaystack(n, stop, float64(pct)/100, 2)
		b.Run(itoaPct(pct), func(b *testing.B) { benchMatchOver(b, trie, hay) })
	}
}

// BenchmarkRootSkip_CardinalitySweep holds the haystack fixed (Ibsen) and
// grows the number of distinct pattern first bytes from 1 to 26, crossing the
// walkStopByte (1) to walkTable (>1) boundary. Natural-letter frequencies are
// skewed, so adding first bytes also raises the haystack's stop-byte density;
// the sweep therefore reports the measured density in each subtest label so
// the cardinality and density effects can be told apart when reading results.
func BenchmarkRootSkip_CardinalitySweep(b *testing.B) {
	all := benchReadLines(b, "./test_data/NSF-ordlisten.cleaned.txt", 0)
	hay := benchReadFile(b, "./test_data/Ibsen.txt")
	// Bucket words by first byte so we can assemble sets with a chosen
	// number of distinct first letters.
	byFirst := map[byte][]string{}
	for _, w := range all {
		byFirst[w[0]] = append(byFirst[w[0]], w)
	}
	letters := []byte("abcdefghijklmnopqrstuvwxyz")
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
			continue // not enough distinct first letters available
		}
		// Measure the actual stop-byte density this pattern set induces on
		// the fixed haystack, so the confound with cardinality is visible.
		var stop [256]bool
		for _, p := range pats {
			stop[p[0]] = true
		}
		stopCount := 0
		for _, c := range hay {
			if stop[c] {
				stopCount++
			}
		}
		densityPct := stopCount * 100 / len(hay)
		trie := NewTrieBuilder().AddStrings(pats).Build()
		b.Run(itoaCard(card)+"/"+itoaPct(densityPct), func(b *testing.B) { benchMatchOver(b, trie, hay) })
	}
}

// itoaPct and itoaCard build subtest labels without importing fmt.
func itoaPct(p int) string  { return "density_" + itoa(p) + "pct" }
func itoaCard(c int) string { return "firstbytes_" + itoa(c) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
