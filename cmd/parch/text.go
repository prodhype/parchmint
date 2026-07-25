package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/goodblaster/parchmint/textlayer"
)

// runTextCommand implements `parch text <archive.html>`: read the embedded
// parchmint-text layer back out of an archive. Default output is the
// page's plain text (blocks separated by blank lines) — clean feed for
// pipes and LLMs; -json dumps the whole layer; -blocks emits one JSON
// object per block (NDJSON) for external indexers.
func runTextCommand(args []string) {
	fs := flag.NewFlagSet("text", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw text layer as indented JSON")
	asBlocks := fs.Bool("blocks", false, "one JSON object per block, newline-delimited — paragraph feed for external indexers (Elasticsearch bulk, jq)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s text [-json|-blocks] <archive.html>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "-blocks emits NDJSON: each line is one paragraph-level block with the")
		fmt.Fprintln(os.Stderr, "archive identity (url, capturedAt) repeated, so every line stands")
		fmt.Fprintln(os.Stderr, "alone as an indexable document. Note the text is RAW rendered text —")
		fmt.Fprintln(os.Stderr, "it can contain soft hyphens, zero-width characters, and curly quotes;")
		fmt.Fprintln(os.Stderr, "index it with a folding analyzer (e.g. icu_folding) or matching will")
		fmt.Fprintln(os.Stderr, "under-recall. Block ids are stable only within one archive.")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, map[string]bool{"json": true, "blocks": true}))
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	if *asJSON && *asBlocks {
		fmt.Fprintln(os.Stderr, "parch: -json and -blocks are mutually exclusive")
		os.Exit(2)
	}

	layer, err := textlayer.FromFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(1)
	}

	if *asJSON {
		out, err := json.MarshalIndent(layer, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "parch: "+err.Error())
			os.Exit(1)
		}
		fmt.Println(string(out))
		return
	}

	if *asBlocks {
		if err := writeBlockLines(os.Stdout, fs.Arg(0), layer); err != nil {
			fmt.Fprintln(os.Stderr, "parch: "+err.Error())
			os.Exit(1)
		}
		return
	}

	for i, b := range layer.Blocks {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(b.Text)
	}
}

// blockLine is one NDJSON export record: a paragraph-level block plus
// enough archive identity to stand alone in an external index. Geometry
// (word boxes) is deliberately omitted — offsets and highlighting stay
// parch-side; external systems only need the text and where it came from.
type blockLine struct {
	Archive    string  `json:"archive"`
	URL        string  `json:"url"`
	CapturedAt string  `json:"capturedAt"`
	Block      int     `json:"block"`
	Type       string  `json:"type"`
	Source     string  `json:"source,omitempty"`
	Image      string  `json:"image,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Frame      string  `json:"frame,omitempty"`
	Text       string  `json:"text"`
}

// writeBlockLines emits the layer's blocks as NDJSON, one standalone
// document per line.
func writeBlockLines(w io.Writer, archive string, layer *textlayer.Layer) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, b := range layer.Blocks {
		line := blockLine{
			Archive:    archive,
			URL:        layer.URL,
			CapturedAt: layer.CapturedAt,
			Block:      b.ID,
			Type:       b.Type,
			Source:     b.Source,
			Image:      b.Image,
			Confidence: b.Confidence,
			Frame:      b.Frame,
			Text:       b.Text,
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return bw.Flush()
}
