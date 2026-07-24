package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/goodblaster/parchmint/textlayer"
)

// runFindCommand implements `parch find <phrase> <archive.html>`:
// block-scoped phrase search over the embedded text layer. Exit status is
// grep-like: 0 with hits, 1 without, 2 on error.
func runFindCommand(args []string) {
	fs := flag.NewFlagSet("find", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "hits as JSON (block, range, boxes)")
	context := fs.Int("context", 40, "context characters shown on each side of a match")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s find [-json] <phrase> <archive.html>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Ctrl-F-style matching within one paragraph (never across): a query")
		fmt.Fprintln(os.Stderr, "word matches anywhere inside a page word (\"phone\" finds iPhone),")
		fmt.Fprintln(os.Stderr, "case/accent/punctuation-insensitively; consecutive words must be")
		fmt.Fprintln(os.Stderr, "consecutive on the page; `*` bridges words (\"apple*card\" and")
		fmt.Fprintln(os.Stderr, "\"apple * card\" both find \"Apple Gift Card\").")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, map[string]bool{"json": true}))
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
	matcher, err := textlayer.Compile(fs.Arg(0), textlayer.MatchOptions{})
	if err != nil {
		die(err)
	}
	hits := textlayer.FindAll(matcher, layer)

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
