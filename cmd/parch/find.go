package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/goodblaster/parchmint/textlayer"
)

// runFindCommand implements `parch find <phrase> <archive.html>`:
// block-scoped phrase search over the embedded text layer. Exit status is
// grep-like: 0 with hits, 1 without, 2 on error.
func runFindCommand(args []string) {
	fs := flag.NewFlagSet("find", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "hits as JSON (block, range, boxes)")
	context := fs.Int("context", 40, "context characters shown on each side of a match")
	var invert bool
	fs.BoolVar(&invert, "v", false, "select blocks with NO match instead of hits")
	fs.BoolVar(&invert, "invert", false, "alias of -v")
	match := registerMatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s find [options] <query> <archive.html>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Ctrl-F-style matching within one paragraph (never across): a query")
		fmt.Fprintln(os.Stderr, "word matches anywhere inside a page word (\"phone\" finds iPhone),")
		fmt.Fprintln(os.Stderr, "case/accent/punctuation-insensitively; consecutive words must be")
		fmt.Fprintln(os.Stderr, "consecutive on the page; `*` bridges words (\"apple*card\" and")
		fmt.Fprintln(os.Stderr, "\"apple * card\" both find \"Apple Gift Card\").")
		fmt.Fprintln(os.Stderr, "\nWith -e the query is a Go regular expression instead, matched")
		fmt.Fprintln(os.Stderr, "against the raw block text (no case/accent/punctuation folding;")
		fmt.Fprintln(os.Stderr, "-i for case-insensitive). `.` does not match a block's internal")
		fmt.Fprintln(os.Stderr, "newlines (from <br>) unless the pattern uses (?s).")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, matchBoolFlags(map[string]bool{"json": true, "v": true, "invert": true})))
	if fs.NArg() != 2 {
		fs.Usage()
		os.Exit(2)
	}

	die := func(err error) {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}
	layer, err := textlayer.FromFile(fs.Arg(1))
	if err != nil {
		die(err)
	}
	matcher, err := textlayer.Compile(fs.Arg(0), match.options())
	if err != nil {
		die(err)
	}
	hits := textlayer.FindAll(matcher, layer)

	// -v selects whole blocks with no match — a filter, not a span
	// search, which is why invert exists only on find: there is nothing
	// for mark/pdf to highlight in a non-match.
	if invert {
		matched := map[int]bool{}
		for _, h := range hits {
			matched[h.Block.ID] = true
		}
		var blocks []*textlayer.Block
		for i := range layer.Blocks {
			if b := &layer.Blocks[i]; !matched[b.ID] {
				blocks = append(blocks, b)
			}
		}
		if *asJSON {
			type jsonBlock struct {
				Block int    `json:"block"`
				Type  string `json:"type"`
				Text  string `json:"text"`
			}
			out := struct {
				Query      string      `json:"query"`
				Normalizer string      `json:"normalizer"`
				Archive    string      `json:"archive"`
				URL        string      `json:"url"`
				Inverted   bool        `json:"inverted"`
				Blocks     []jsonBlock `json:"blocks"`
			}{
				Query:      fs.Arg(0),
				Normalizer: textlayer.NormVersion,
				Archive:    fs.Arg(1),
				URL:        layer.URL,
				Inverted:   true,
				Blocks:     []jsonBlock{},
			}
			for _, b := range blocks {
				out.Blocks = append(out.Blocks, jsonBlock{Block: b.ID, Type: b.Type, Text: b.Text})
			}
			enc, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				die(err)
			}
			fmt.Println(string(enc))
		} else {
			for _, b := range blocks {
				fmt.Printf("#%d %s  %s\n", b.ID, b.Type, snippet(b.Text, 2**context))
			}
			fmt.Fprintf(os.Stderr, "%d block(s) without a match\n", len(blocks))
		}
		if len(blocks) == 0 {
			os.Exit(1)
		}
		return
	}

	if *asJSON {
		type jsonHit struct {
			Block int              `json:"block"`
			Type  string           `json:"type"`
			Range [2]int           `json:"range"`
			Match string           `json:"match"`
			Text  string           `json:"context"`
			Box   textlayer.Box    `json:"box"`
			Words []map[string]any `json:"words"`
		}
		out := struct {
			Query      string    `json:"query"`
			Normalizer string    `json:"normalizer"`
			Archive    string    `json:"archive"`
			URL        string    `json:"url"`
			Hits       []jsonHit `json:"hits"`
		}{
			Query:      fs.Arg(0),
			Normalizer: textlayer.NormVersion,
			Archive:    fs.Arg(1),
			URL:        layer.URL,
			Hits:       []jsonHit{},
		}
		for _, h := range hits {
			jh := jsonHit{
				Block: h.Block.ID,
				Type:  h.Block.Type,
				Range: [2]int{h.Start, h.End},
				Match: h.Text(),
				Text:  h.Context(*context, "«", "»"),
				Box:   h.Box(),
			}
			for _, w := range h.Words() {
				jh.Words = append(jh.Words, map[string]any{
					"range": [2]int{w.Start, w.End},
					"box":   w.Box,
				})
			}
			out.Hits = append(out.Hits, jh)
		}
		enc, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			die(err)
		}
		fmt.Println(string(enc))
	} else {
		for _, h := range hits {
			box := h.Box()
			fmt.Printf("#%d %s @%d,%d  %s\n", h.Block.ID, h.Block.Type, box[0], box[1],
				h.Context(*context, "«", "»"))
		}
		fmt.Fprintf(os.Stderr, "%d match(es)\n", len(hits))
	}

	if len(hits) == 0 {
		os.Exit(1)
	}
}

// snippet flattens newlines and truncates s to max runes for one-line
// block listings (-v output).
func snippet(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
