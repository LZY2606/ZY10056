package org

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFuzzSmoke is a deterministic, offline fuzz smoke test. It mutates the
// fixed corpus in testdata/ with a fixed-seed PRNG and asserts that the
// parser and both writers never panic or error. Budget and seed are bounded
// via environment variables so tools/verify.sh can run the same check in
// quick and full mode:
//
//	GO_ORG_FUZZ_SEED          PRNG seed (default: fixed constant)
//	GO_ORG_FUZZ_BUDGET        number of mutated inputs (default: 100)
//	GO_ORG_FUZZ_ARTIFACT_DIR  directory for failing inputs (default: temp dir)
//	GO_ORG_FUZZ_REPLAY        path to a failing input to replay once
func TestFuzzSmoke(t *testing.T) {
	if replay := os.Getenv("GO_ORG_FUZZ_REPLAY"); replay != "" {
		input, err := os.ReadFile(replay)
		if err != nil {
			t.Fatalf("could not read replay input: %s", err)
		}
		if err := fuzzCheck(input); err != nil {
			t.Fatalf("replay of %s failed: %s", replay, err)
		}
		return
	}

	seed := int64(20240517)
	if v := os.Getenv("GO_ORG_FUZZ_SEED"); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("invalid GO_ORG_FUZZ_SEED %q: %s", v, err)
		}
		seed = parsed
	}
	budget := 100
	if v := os.Getenv("GO_ORG_FUZZ_BUDGET"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 1 {
			t.Fatalf("invalid GO_ORG_FUZZ_BUDGET %q", v)
		}
		budget = parsed
	}

	corpus := [][]byte{}
	for _, path := range orgTestFiles() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("could not read corpus file %s: %s", path, err)
		}
		corpus = append(corpus, data)
	}
	if len(corpus) == 0 {
		t.Fatal("empty fuzz corpus")
	}

	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < budget; i++ {
		input := fuzzMutate(rng, corpus[rng.Intn(len(corpus))])
		if err := fuzzCheck(input); err != nil {
			artifact := fuzzWriteArtifact(t, input, i)
			t.Fatalf("fuzz iteration %d failed: %s\nfailing input written to %s\nreplay with: GO_ORG_FUZZ_REPLAY=%s go test ./org -run TestFuzzSmoke -count=1 -v", i, err, artifact, artifact)
		}
	}
}

// fuzzCheck parses input, renders it with both writers and re-parses the Org
// writer output. It reports panics and writer errors as failures.
func fuzzCheck(input []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	conf := New().Silent()
	d := conf.Parse(bytes.NewReader(input), "fuzz.org")
	orgOut, err := d.Write(NewOrgWriter())
	if err != nil {
		return fmt.Errorf("org writer: %s", err)
	}
	if _, err := d.Write(NewHTMLWriter()); err != nil {
		return fmt.Errorf("html writer: %s", err)
	}
	roundtrip := conf.Parse(strings.NewReader(orgOut), "fuzz.org")
	if _, err := roundtrip.Write(NewHTMLWriter()); err != nil {
		return fmt.Errorf("html writer after org roundtrip: %s", err)
	}
	return nil
}

func fuzzMutate(rng *rand.Rand, input []byte) []byte {
	out := append([]byte(nil), input...)
	mutations := 1 + rng.Intn(8)
	for i := 0; i < mutations && len(out) > 0; i++ {
		switch rng.Intn(6) {
		case 0: // flip a byte
			out[rng.Intn(len(out))] = byte(rng.Intn(256))
		case 1: // delete a slice
			start := rng.Intn(len(out))
			end := start + rng.Intn(len(out)-start+1)
			out = append(out[:start], out[end:]...)
		case 2: // duplicate a slice
			start := rng.Intn(len(out))
			end := start + rng.Intn(len(out)-start+1)
			dup := append([]byte(nil), out[start:end]...)
			out = append(out[:start], append(dup, out[start:]...)...)
		case 3: // truncate
			out = out[:rng.Intn(len(out)+1)]
		case 4: // inject special tokens
			tokens := []string{"#+BEGIN_SRC", "[fn:", "[[", "* ", "#+", "\r\n", "\x00", "{{{"}
			pos := rng.Intn(len(out) + 1)
			tok := tokens[rng.Intn(len(tokens))]
			out = append(out[:pos], append([]byte(tok), out[pos:]...)...)
		case 5: // shuffle lines
			lines := strings.Split(string(out), "\n")
			rng.Shuffle(len(lines), func(a, b int) { lines[a], lines[b] = lines[b], lines[a] })
			out = []byte(strings.Join(lines, "\n"))
		}
	}
	return out
}

func fuzzWriteArtifact(t *testing.T, input []byte, iteration int) string {
	dir := os.Getenv("GO_ORG_FUZZ_ARTIFACT_DIR")
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "go-org-fuzz-artifacts")
		if err != nil {
			t.Fatalf("could not create artifact dir: %s", err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("could not create artifact dir %s: %s", dir, err)
	}
	path := filepath.Join(dir, fmt.Sprintf("fuzz-crash-%d-%d.org", time.Now().Unix(), iteration))
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatalf("could not write fuzz artifact %s: %s", path, err)
	}
	return path
}
