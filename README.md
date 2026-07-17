# Aho-Corasick

[![CI](https://github.com/ahrav/aho-corasick/actions/workflows/ci.yml/badge.svg?branch=master)](https://github.com/ahrav/aho-corasick/actions/workflows/ci.yml)
![Go Version](https://img.shields.io/github/go-mod/go-version/BobuSumisu/aho-corasick)
![Latest Tag](https://img.shields.io/github/v/tag/BobuSumisu/aho-corasick)

Implementation of the Aho-Corasick string-search algorithm in Go.

Licensed under MIT License.

## Details

This implementation does not use a [Double-Array Trie](https://linux.thai.net/~thep/datrie/datrie.html) as in my
[implementation](https://github.com/BobuSumisu/go-ahocorasick) from a couple of years back.

This reduces the build time drastically, but at the cost of higher memory consumption.

See [Performance](#performance) for current benchmark results.

## Documentation

Can be found at [godoc.org](https://godoc.org/github.com/BobuSumisu/aho-corasick).

## Example Usage

Use a `TrieBuilder` to build a `Trie`:

```go
trie := NewTrieBuilder().
    AddStrings([]string{"or", "amet"}).
    Build()
```

Then go and match something interesting:

```go
matches := trie.MatchString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.")
fmt.Printf("Got %d matches.\n", len(matches))

// => Got 3 matches.
```

What did we match?

```go
for _, match := range matches {
    fmt.Printf("Matched pattern %d %q at position %d.\n", match.Match(),
        match.Pattern(), match.Pos())
}

// => Matched pattern 0 "or" at position 1.
// => Matched pattern 0 "or" at position 15.
// => Matched pattern 1 "amet" at position 22.
```

## Building

You can easily load patterns from file:

```go
builder := NewTrieBuilder()
builder.LoadPatterns("patterns.txt")
builder.LoadStrings("strings.txt")
```

Both functions expects a text file with one pattern per line. `LoadPatterns` expects the pattern to
be in hexadecimal form.

## Storing

Use `Encode` to store a `Trie` in gzip compressed binary format:

```go
f, err := os.Create("trie.gz")
err := Encode(f, trie)
```

And `Decode` to load it from binary format:

```go
f, err := os.Open("trie.gz")
trie, err := Decode(f)
```

## Performance

Against upstream commit `b4b5728`, this fork at `1e0b467` reduced
single-core time by 30% to 98% across six preselected workloads on AWS
Graviton3.

| Workload | Upstream | Fork | Time reduction |
|---|---:|---:|---:|
| Natural text, spread 10k dictionary, 100 KiB | 464.921 us | 325.286 us | 30.02% |
| No match, spread 10k dictionary, 1 MiB | 2.983 ms | 336.482 us | 88.72% |
| Dense overlapping matches, 64 KiB | 10.626 ms | 897.377 us | 91.51% |
| `MatchFirst`, late match in 100 KiB | 282.567 us | 5.177 us | 98.17% |
| Build 10k-pattern trie | 111.702 ms | 13.124 ms | 88.26% |
| Natural text, sorted 10k dictionary, 8 MiB | 37.780 ms | 8.724 ms | 76.87% |

Times are medians across 31 paired process executions per revision. See
[STOCK-COMPARISON.md](STOCK-COMPARISON.md) for confidence intervals, raw
samples, and the reproduction protocol.

### Compared to Other Implementation

See
[aho-corasick-benchmark](https://github.com/Bobusumisu/aho-corasick-benchmark).

### Memory Usage

Memory consumption is higher than a double-array trie implementation,
especially during the build phase.
