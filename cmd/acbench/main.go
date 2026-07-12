// Command acbench is a tiny end-to-end driver for hyperfine comparisons
// between library versions. Public API only.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	aho "github.com/BobuSumisu/aho-corasick"
)

func loadPatterns(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var pats []string
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			pats = append(pats, s)
		}
	}
	return pats
}

func stride(pats []string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < len(pats) && len(out) < n; i += 10 {
		out = append(out, pats[i])
	}
	return out
}

func main() {
	if len(os.Args) < 2 {
		panic("usage: acbench build|scan|e2e ...")
	}
	dataDir := os.Getenv("ACBENCH_DATA")
	if dataDir == "" {
		dataDir = "./test_data"
	}
	pats := loadPatterns(dataDir + "/NSF-ordlisten.cleaned.txt")
	switch os.Args[1] {
	case "build":
		// acbench build <numPatterns> <iters>
		n, _ := strconv.Atoi(os.Args[2])
		iters, _ := strconv.Atoi(os.Args[3])
		var states int
		for i := 0; i < iters; i++ {
			tr := aho.NewTrieBuilder().AddStrings(pats[:n]).Build()
			ms := tr.MatchString("sanering")
			states += len(ms)
			tr.ReleaseMatches(ms)
		}
		fmt.Println("ok", states)
	case "scan":
		// acbench scan sorted|spread <totalMB> <sliceKB>
		dict := pats[:10000]
		if os.Args[2] == "spread" {
			dict = stride(pats, 10000)
		}
		totalMB, _ := strconv.Atoi(os.Args[3])
		sliceKB, _ := strconv.Atoi(os.Args[4])
		ibsen, err := os.ReadFile(dataDir + "/Ibsen.txt")
		if err != nil {
			panic(err)
		}
		slice := ibsen
		if sliceKB<<10 < len(ibsen) {
			slice = ibsen[:sliceKB<<10]
		}
		tr := aho.NewTrieBuilder().AddStrings(dict).Build()
		total := 0
		matches := 0
		for total < totalMB<<20 {
			ms := tr.Match(slice)
			matches += len(ms)
			tr.ReleaseMatches(ms)
			total += len(slice)
		}
		fmt.Println("matches", matches)
	case "e2e":
		// acbench e2e <numPatterns>: one cold build + one full-file scan
		n, _ := strconv.Atoi(os.Args[2])
		ibsen, err := os.ReadFile(dataDir + "/Ibsen.txt")
		if err != nil {
			panic(err)
		}
		tr := aho.NewTrieBuilder().AddStrings(pats[:n]).Build()
		ms := tr.Match(ibsen)
		fmt.Println("matches", len(ms))
		tr.ReleaseMatches(ms)
	}
}
