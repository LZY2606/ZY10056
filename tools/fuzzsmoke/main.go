// Command fuzzsmoke runs a deterministic, offline fuzz smoke over a fixed
// corpus. Mutations are driven by a fixed-seed PRNG so runs are reproducible.
//
//	fuzzsmoke -seed 1060 -n 300 -artifacts <dir> <corpus-dir>...
//	fuzzsmoke -replay <file>
//
// On failure the offending input is written to the artifacts directory, a
// replay command is printed, and the exit code is 1.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niklasfasching/go-org/org"
)

func main() {
	seed := flag.Int64("seed", 1060, "fixed PRNG seed")
	n := flag.Int("n", 300, "number of mutated inputs to check")
	artifacts := flag.String("artifacts", "", "directory for failure artifacts")
	replay := flag.String("replay", "", "replay a single input file and exit")
	flag.Parse()

	if *replay != "" {
		data, err := os.ReadFile(*replay)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fuzzsmoke: %v\n", err)
			os.Exit(2)
		}
		if err := check(data, *replay, true); err != nil {
			fmt.Fprintf(os.Stderr, "replay FAILED: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("replay ok")
		return
	}

	dirs := flag.Args()
	if len(dirs) == 0 {
		fmt.Fprintf(os.Stderr, "usage: fuzzsmoke -seed S -n N -artifacts DIR <corpus-dir>...\n")
		os.Exit(2)
	}
	corpus, err := loadCorpus(dirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fuzzsmoke: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("corpus: %d files, seed: %d, mutations: %d\n", len(corpus), *seed, *n)

	// 1. raw corpus smoke: every corpus file must round-trip exactly (strict
	// HTML equality). Inputs are parsed with their real path so relative
	// includes/setup files resolve.
	for _, entry := range corpus {
		if err := check(entry.data, entry.path, true); err != nil {
			fail(*artifacts, entry.data, fmt.Sprintf("corpus entry %s", entry.path), err)
		}
	}
	// 2. deterministic mutations of the fixed corpus. Mutated inputs are
	// arbitrary garbage, so here we only assert robustness: no panics and no
	// writer errors. Exact round-trip equality on mutated input is a fuzz
	// finding (see org/fuzz.go), not a smoke invariant.
	rng := rand.New(rand.NewSource(*seed))
	for i := 0; i < *n; i++ {
		base := corpus[rng.Intn(len(corpus))]
		input := mutate(base.data, corpus, rng)
		if err := check(input, base.path, false); err != nil {
			fail(*artifacts, input, fmt.Sprintf("mutation #%d (seed %d)", i, *seed), err)
		}
	}
	fmt.Printf("fuzz smoke ok: %d corpus + %d mutated inputs\n", len(corpus), *n)
}

func fail(artifacts string, input []byte, label string, err error) {
	fmt.Fprintf(os.Stderr, "fuzzsmoke FAILURE (%s): %v\n", label, err)
	if artifacts != "" {
		if mkErr := os.MkdirAll(artifacts, 0o755); mkErr == nil {
			path := filepath.Join(artifacts, "fuzz-failure.org")
			if wErr := os.WriteFile(path, input, 0o644); wErr == nil {
				fmt.Fprintf(os.Stderr, "artifact: %s\nreplay: go run ./tools/fuzzsmoke -replay %s\n", path, path)
			}
		}
	}
	os.Exit(1)
}

type corpusEntry struct {
	path string
	data []byte
}

func loadCorpus(dirs []string) ([]corpusEntry, error) {
	var files []string
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "*.org"))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("empty corpus in %v", dirs)
	}
	corpus := make([]corpusEntry, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		corpus = append(corpus, corpusEntry{f, data})
	}
	return corpus, nil
}

// check parses input, writes Org and HTML, and re-parses the Org output.
// Panics and writer errors are always failures. In strict mode it also
// mirrors the invariant of org/fuzz.go: the re-parsed document must render
// to identical HTML.
func check(input []byte, path string, strict bool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	conf := org.New().Silent()
	d := conf.Parse(bytes.NewReader(input), path)
	orgOutput, err := d.Write(org.NewOrgWriter())
	if err != nil {
		return fmt.Errorf("org write: %w", err)
	}
	htmlA, err := d.Write(org.NewHTMLWriter())
	if err != nil {
		return fmt.Errorf("html write: %w", err)
	}
	htmlB, err := conf.Parse(strings.NewReader(orgOutput), path).Write(org.NewHTMLWriter())
	if err != nil {
		return fmt.Errorf("re-parse html write: %w", err)
	}
	if strict && htmlA != htmlB {
		return fmt.Errorf("rendered org output differs from original html")
	}
	return nil
}

func mutate(base []byte, corpus []corpusEntry, rng *rand.Rand) []byte {
	out := append([]byte(nil), base...)
	for i, n := 0, 1+rng.Intn(4); i < n && len(out) > 0; i++ {
		switch rng.Intn(5) {
		case 0: // flip a byte
			out[rng.Intn(len(out))] = byte(rng.Intn(256))
		case 1: // delete a span
			start := rng.Intn(len(out))
			end := start + rng.Intn(min(64, len(out)-start))
			out = append(out[:start], out[end:]...)
		case 2: // insert a span from another corpus entry
			donor := corpus[rng.Intn(len(corpus))].data
			if len(donor) == 0 {
				continue
			}
			start := rng.Intn(len(donor))
			end := start + rng.Intn(min(64, len(donor)-start))
			at := rng.Intn(len(out) + 1)
			out = append(out[:at:at], append(donor[start:end], out[at:]...)...)
		case 3: // truncate
			out = out[:rng.Intn(len(out)+1)]
		case 4: // duplicate a span
			start := rng.Intn(len(out))
			end := start + rng.Intn(min(64, len(out)-start))
			at := rng.Intn(len(out) + 1)
			out = append(out[:at:at], append(out[start:end], out[at:]...)...)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
