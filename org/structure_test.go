package org

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// defaultStructureFixtures lists the representative documents (relative to
// testdata/, without extension) that are exercised by the parse-write-parse
// structural check. tools/verify.sh may override the list via
// GO_ORG_VERIFY_FIXTURES (space separated) to keep it in sync with
// tools/fixtures.manifest.
var defaultStructureFixtures = []string{
	"crlf",
	"east_asian_line_breaks",
	"footnotes",
	"blocks",
	"inline",
	"lists",
}

func structureFixtures() []string {
	if v := os.Getenv("GO_ORG_VERIFY_FIXTURES"); v != "" {
		return strings.Fields(v)
	}
	return defaultStructureFixtures
}

// structureSummary flattens the semantic structure of a parsed document into
// a deterministic list of lines: headline levels, link targets, footnote
// references, code block languages and list structure. Comparing summaries
// (instead of raw output) catches writer/parser semantic drift.
func structureSummary(nodes []Node, depth int) []string {
	lines := []string{}
	var walk func(ns []Node, depth int)
	walk = func(ns []Node, depth int) {
		for _, n := range ns {
			switch n := n.(type) {
			case Headline:
				lines = append(lines, fmt.Sprintf("headline lvl=%d title=%q", n.Lvl, String(n.Title...)))
				walk(n.Title, depth)
				walk(n.Children, depth)
			case Paragraph:
				walk(n.Children, depth)
			case Block:
				lang := ""
				if n.Name == "SRC" && len(n.Parameters) > 0 {
					lang = n.Parameters[0]
				}
				lines = append(lines, fmt.Sprintf("block name=%s lang=%s", n.Name, lang))
				walk(n.Children, depth)
			case List:
				lines = append(lines, fmt.Sprintf("list kind=%s items=%d depth=%d", n.Kind, len(n.Items), depth))
				walk(n.Items, depth+1)
			case ListItem:
				lines = append(lines, fmt.Sprintf("item bullet=%s status=%s depth=%d", n.Bullet, n.Status, depth))
				walk(n.Children, depth)
			case DescriptiveListItem:
				lines = append(lines, fmt.Sprintf("descriptive-item bullet=%s status=%s depth=%d", n.Bullet, n.Status, depth))
				walk(n.Term, depth)
				walk(n.Details, depth)
			case FootnoteDefinition:
				lines = append(lines, fmt.Sprintf("footnote-def name=%s", n.Name))
				walk(n.Children, depth)
			case FootnoteLink:
				lines = append(lines, fmt.Sprintf("footnote-ref name=%s", n.Name))
			case RegularLink:
				lines = append(lines, fmt.Sprintf("link protocol=%s url=%s", n.Protocol, n.URL))
				walk(n.Description, depth)
			}
		}
	}
	walk(nodes, depth)
	return lines
}

// TestParseWriteParseStructure parses each representative document, renders it
// with the Org writer, re-parses the result and asserts that both the
// extracted document structure and the normalized HTML output are identical.
func TestParseWriteParseStructure(t *testing.T) {
	for _, name := range structureFixtures() {
		t.Run(name, func(t *testing.T) {
			path := "testdata/" + name + ".org"
			input := fileString(t, path)
			direct := New().Silent().Parse(strings.NewReader(input), path)
			orgOut, err := direct.Write(NewOrgWriter())
			if err != nil {
				t.Fatalf("%s: org write error: %s", path, err)
			}
			roundtrip := New().Silent().Parse(strings.NewReader(orgOut), path)

			directSummary := strings.Join(structureSummary(direct.Nodes, 0), "\n")
			roundtripSummary := strings.Join(structureSummary(roundtrip.Nodes, 0), "\n")
			if directSummary != roundtripSummary {
				t.Errorf("%s: structure changed after parse-write-parse:\n%s", path, diff(roundtripSummary, directSummary))
			}

			htmlDirect, err := direct.Write(NewHTMLWriter())
			if err != nil {
				t.Fatalf("%s: html write error: %s", path, err)
			}
			htmlRoundtrip, err := roundtrip.Write(NewHTMLWriter())
			if err != nil {
				t.Fatalf("%s: html write error after roundtrip: %s", path, err)
			}
			if htmlDirect != htmlRoundtrip {
				t.Errorf("%s: normalized HTML changed after parse-write-parse:\n%s", path, diff(htmlRoundtrip, htmlDirect))
			}
		})
	}
}
