package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// runLinesCommand implements `parch lines <archive>`: one line of plain
// text per paragraph block — the simplest possible pipe feed (grep, awk,
// line-per-record loaders). Internal newlines (from <br>) are flattened
// to spaces so the line/paragraph correspondence is exact; line N is
// block N only when no block is empty, so downstream tools should treat
// lines as records, not as block ids.
func runLinesCommand(args []string) {
	fs := flag.NewFlagSet("lines", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s lines <archive.html|.mht|.pdf> ('-' = stdin)\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Prints one line per paragraph block of the embedded text layer —")
		fmt.Fprintln(os.Stderr, "plain text only. For block ids and provenance use `text -blocks`.")
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	layer, err := loadLayer(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(1)
	}

	w := bufio.NewWriter(os.Stdout)
	for _, b := range layer.Blocks {
		line := strings.TrimSpace(strings.ReplaceAll(b.Text, "\n", " "))
		if line == "" {
			continue
		}
		fmt.Fprintln(w, line)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(1)
	}
}
