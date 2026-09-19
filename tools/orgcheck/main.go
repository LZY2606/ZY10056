// Command orgcheck performs the fixture verification stages of tools/verify.sh:
//
//	orgcheck struct <fixture-dir>      parse -> Org write -> re-parse structural check
//	orgcheck golden-html <fixture-dir> read-only HTML golden comparison
//	orgcheck golden-org <fixture-dir>  read-only Org golden comparison
//	orgcheck update <fixture-dir>      regenerate goldens (manual, human-reviewed only)
//
// struct and golden never write to the repository. On mismatch they print the
// first difference location and exit 1.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niklasfasching/go-org/org"
	h "golang.org/x/net/html"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: orgcheck struct|golden|update <fixture-dir>\n")
		os.Exit(2)
	}
	mode, dir := os.Args[1], os.Args[2]
	fixtures, err := filepath.Glob(filepath.Join(dir, "*.org"))
	if err != nil || len(fixtures) == 0 {
		fmt.Fprintf(os.Stderr, "orgcheck: no fixtures found in %s\n", dir)
		os.Exit(2)
	}
	sort.Strings(fixtures)
	failed := false
	for _, f := range fixtures {
		var err error
		switch mode {
		case "struct":
			err = structCheck(f)
		case "golden-html":
			err = goldenCheckHTML(dir, f)
		case "golden-org":
			err = goldenCheckOrg(dir, f)
		case "update":
			err = goldenUpdate(dir, f)
		default:
			fmt.Fprintf(os.Stderr, "orgcheck: unknown mode %q\n", mode)
			os.Exit(2)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", f, err)
			failed = true
		} else if mode != "update" {
			fmt.Printf("ok   %s\n", f)
		}
	}
	if failed {
		os.Exit(1)
	}
}

func parse(input, path string) (*org.Document, error) {
	d := org.New().Silent().Parse(strings.NewReader(input), path)
	if d.Error != nil {
		return nil, d.Error
	}
	return d, nil
}

func renderOrg(input, path string) (string, error) {
	d, err := parse(input, path)
	if err != nil {
		return "", err
	}
	return d.Write(org.NewOrgWriter())
}

func renderHTML(input, path string) (string, error) {
	d, err := parse(input, path)
	if err != nil {
		return "", err
	}
	return d.Write(org.NewHTMLWriter())
}

// structCheck compares the structural summary of a fixture with the summary of
// its parse -> Org write -> re-parse round trip.
func structCheck(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := parse(string(data), path)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	orgOut, err := d.Write(org.NewOrgWriter())
	if err != nil {
		return fmt.Errorf("org write: %w", err)
	}
	reparsed, err := parse(orgOut, path)
	if err != nil {
		return fmt.Errorf("re-parse of Org writer output: %w", err)
	}
	a, b := summarize(d), summarize(reparsed)
	if diff := firstDiff(a, b); diff != "" {
		return fmt.Errorf("parse-write-parse structural drift:\n%s", diff)
	}
	return nil
}

// summarize extracts headline levels, link targets, footnote references, code
// block languages and list structure from a parsed document.
func summarize(d *org.Document) []string {
	var out []string
	var walk func(nodes []org.Node, depth int)
	walk = func(nodes []org.Node, depth int) {
		for _, n := range nodes {
			switch n := n.(type) {
			case org.Headline:
				out = append(out, fmt.Sprintf("headline lvl=%d title=%q", n.Lvl, org.String(n.Title...)))
				walk(n.Children, depth)
			case org.Block:
				lang := ""
				if strings.EqualFold(n.Name, "SRC") && len(n.Parameters) > 0 {
					lang = n.Parameters[0]
				}
				out = append(out, fmt.Sprintf("block name=%s lang=%q", strings.ToUpper(n.Name), lang))
			case org.List:
				out = append(out, fmt.Sprintf("list kind=%s items=%d depth=%d", n.Kind, len(n.Items), depth))
				walk(n.Items, depth+1)
			case org.ListItem:
				walk(n.Children, depth)
			case org.DescriptiveListItem:
				walk(n.Details, depth)
			case org.Paragraph:
				walk(n.Children, depth)
			case org.RegularLink:
				out = append(out, fmt.Sprintf("link url=%q", n.URL))
			case org.FootnoteLink:
				out = append(out, fmt.Sprintf("footnote-ref name=%q", n.Name))
			case org.FootnoteDefinition:
				out = append(out, fmt.Sprintf("footnote-def name=%q", n.Name))
				walk(n.Children, depth)
			}
		}
	}
	walk(d.Nodes, 0)
	return out
}

// goldenCheckHTML compares rendered HTML against the committed golden without
// writing anything. The comparison is done on normalized semantic nodes.
func goldenCheckHTML(dir, fixture string) error {
	data, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}
	golden := filepath.Join(dir, "golden", strings.TrimSuffix(filepath.Base(fixture), ".org")+".html")
	actual, err := renderHTML(string(data), fixture)
	if err != nil {
		return fmt.Errorf("html render: %w", err)
	}
	want, err := canonicalHTMLFile(golden)
	if err != nil {
		return err
	}
	got, err := canonicalHTML(actual)
	if err != nil {
		return fmt.Errorf("rendered HTML does not parse: %w", err)
	}
	if diff := firstDiff(want, got); diff != "" {
		return fmt.Errorf("HTML golden drift (%s):\n%s", golden, diff)
	}
	return nil
}

// goldenCheckOrg compares Org writer output against the committed golden
// byte for byte, without writing anything.
func goldenCheckOrg(dir, fixture string) error {
	data, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}
	golden := filepath.Join(dir, "golden", strings.TrimSuffix(filepath.Base(fixture), ".org")+".org")
	actual, err := renderOrg(string(data), fixture)
	if err != nil {
		return fmt.Errorf("org render: %w", err)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		return err
	}
	if diff := firstDiff(strings.Split(string(want), "\n"), strings.Split(actual, "\n")); diff != "" {
		return fmt.Errorf("Org golden drift (%s):\n%s", golden, diff)
	}
	return nil
}

// goldenUpdate regenerates golden files. Manual use only - never called by
// tools/verify.sh.
func goldenUpdate(dir, fixture string) error {
	data, err := os.ReadFile(fixture)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(fixture), ".org")
	html, err := renderHTML(string(data), fixture)
	if err != nil {
		return err
	}
	orgOut, err := renderOrg(string(data), fixture)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "golden", name+".html"), []byte(html), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "golden", name+".org"), []byte(orgOut), 0o644); err != nil {
		return err
	}
	fmt.Printf("updated golden for %s\n", fixture)
	return nil
}

// canonicalHTML reduces HTML to a list of semantic nodes: elements with sorted
// attributes and normalized text (whitespace collapsed outside <pre>).
func canonicalHTML(s string) ([]string, error) {
	root, err := h.Parse(strings.NewReader(s))
	if err != nil {
		return nil, err
	}
	var lines []string
	var walk func(n *h.Node, inPre bool)
	walk = func(n *h.Node, inPre bool) {
		switch n.Type {
		case h.ElementNode:
			attrs := make([]string, 0, len(n.Attr))
			for _, a := range n.Attr {
				attrs = append(attrs, fmt.Sprintf("%s=%q", a.Key, a.Val))
			}
			sort.Strings(attrs)
			lines = append(lines, "<"+n.Data+" "+strings.Join(attrs, " ")+">")
			if n.Data == "pre" {
				inPre = true
			}
		case h.TextNode:
			text := n.Data
			if !inPre {
				text = strings.Join(strings.Fields(text), " ")
			}
			if text != "" {
				lines = append(lines, "text: "+text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inPre)
		}
	}
	walk(root, false)
	return lines, nil
}

func canonicalHTMLFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return canonicalHTML(string(data))
}

// firstDiff returns a minimal description of the first differing line, or "".
func firstDiff(want, got []string) string {
	for i := 0; i < len(want) && i < len(got); i++ {
		if want[i] != got[i] {
			return fmt.Sprintf("first difference at line %d:\n  golden: %q\n  actual: %q", i+1, want[i], got[i])
		}
	}
	if len(want) != len(got) {
		shorter, longer := want, got
		label := "actual"
		if len(want) > len(got) {
			shorter, longer = got, want
			label = "golden"
		}
		return fmt.Sprintf("line count differs (golden %d, actual %d); first extra %s line %d: %q",
			len(want), len(got), label, len(shorter)+1, longer[len(shorter)])
	}
	return ""
}
