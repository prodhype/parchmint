package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goodblaster/parchmint/textlayer"
)

// runFindCommand implements `parch find <query> <archive...>`: block-scoped
// search over embedded text layers, shaped like grep — multiple files,
// filename prefixes, -l/-c/-q/-m/-r, and grep exit codes: 0 with matches,
// 1 without, 2 on error (with several files: 0 if ANY file matched).
func runFindCommand(args []string) {
	fs := flag.NewFlagSet("find", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "hits as JSON: one indented document for one file, NDJSON (an object per hit) for several")
	context := fs.Int("context", 40, "context characters shown on each side of a match")
	var invert, listFiles, countOnly, quiet, withFile, noFile, nullSep bool
	fs.BoolVar(&invert, "v", false, "select blocks with NO match instead of hits")
	fs.BoolVar(&invert, "invert", false, "alias of -v")
	fs.BoolVar(&listFiles, "l", false, "print only the names of files with matches")
	fs.BoolVar(&listFiles, "files-with-matches", false, "alias of -l")
	fs.BoolVar(&countOnly, "c", false, "print only a match count per file")
	fs.BoolVar(&countOnly, "count", false, "alias of -c")
	fs.BoolVar(&quiet, "q", false, "print nothing; exit status only")
	fs.BoolVar(&quiet, "quiet", false, "alias of -q")
	maxCount := fs.Int("m", 0, "stop after this many matches per file (0 = unlimited)")
	fs.IntVar(maxCount, "max-count", 0, "alias of -m")
	recurseDir := fs.String("r", "", "also search every archive under this directory (.html, .htm, .mht, .mhtml, .pdf)")
	fs.StringVar(recurseDir, "recursive", "", "alias of -r")
	fs.BoolVar(&withFile, "H", false, "always prefix output with the file name")
	fs.BoolVar(&noFile, "h", false, "never prefix output with the file name (use -help for usage)")
	fs.BoolVar(&nullSep, "0", false, "with -l: NUL-separated names, for xargs -0")
	fs.BoolVar(&nullSep, "null", false, "alias of -0")
	colorMode := fs.String("color", "auto", "colorize matches in human output: auto (only on a terminal), always, never")
	match := registerMatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s find [options] <query> <archive...>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Ctrl-F-style matching within one paragraph (never across): a query")
		fmt.Fprintln(os.Stderr, "word matches anywhere inside a page word (\"phone\" finds iPhone),")
		fmt.Fprintln(os.Stderr, "case/accent/punctuation-insensitively; consecutive words must be")
		fmt.Fprintln(os.Stderr, "consecutive on the page; `*` bridges words (\"apple*card\" and")
		fmt.Fprintln(os.Stderr, "\"apple * card\" both find \"Apple Gift Card\").")
		fmt.Fprintln(os.Stderr, "\nWith -e the query is a Go regular expression instead, matched")
		fmt.Fprintln(os.Stderr, "against the raw block text (no case/accent/punctuation folding;")
		fmt.Fprintln(os.Stderr, "-i for case-insensitive). `.` does not match a block's internal")
		fmt.Fprintln(os.Stderr, "newlines (from <br>) unless the pattern uses (?s).")
		fmt.Fprintln(os.Stderr, "\nWith several files (or -r), output lines carry a file: prefix")
		fmt.Fprintln(os.Stderr, "(-H forces it, -h suppresses it) and exit status is 0 when any")
		fmt.Fprintln(os.Stderr, "file matched. `-` as an archive reads bytes from stdin; `--` ends")
		fmt.Fprintln(os.Stderr, "options, so queries starting with a dash are searchable. Corpus idiom:")
		fmt.Fprintf(os.Stderr, "  %s find -l -0 'phrase' *.html | xargs -0 -n1 %s mark 'phrase'\n", os.Args[0], os.Args[0])
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, matchBoolFlags(map[string]bool{
		"json": true, "v": true, "invert": true,
		"l": true, "files-with-matches": true,
		"c": true, "count": true,
		"q": true, "quiet": true,
		"H": true, "h": true,
		"0": true, "null": true,
	})))
	if fs.NArg() < 2 && !(fs.NArg() == 1 && *recurseDir != "") {
		fs.Usage()
		os.Exit(2)
	}

	die := func(err error) {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}

	// ANSI SGR, grep's palette: bold red match, magenta file names.
	// Plain when piped (auto), so downstream tools never see escapes.
	var useColor bool
	switch *colorMode {
	case "always":
		useColor = true
	case "auto":
		useColor = stdoutIsTerminal()
	case "never":
	default:
		die(fmt.Errorf("unknown -color mode %q (want auto, always, or never)", *colorMode))
	}
	openMark, closeMark := "«", "»"
	if useColor {
		openMark, closeMark = "\x1b[01;31m«", "»\x1b[0m"
	}

	query := fs.Arg(0)
	files := fs.Args()[1:]
	if *recurseDir != "" {
		err := filepath.Walk(*recurseDir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".html", ".htm", ".mht", ".mhtml", ".pdf":
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			die(err)
		}
	}

	matcher, err := textlayer.Compile(query, match.options())
	if err != nil {
		die(err)
	}

	// grep-shaped naming: prefix with the file name when searching more
	// than one file, or always with -H, never with -h.
	multi := len(files) > 1 || *recurseDir != ""
	showName := (withFile || multi) && !noFile
	prefixFor := func(name string) string {
		if !showName {
			return ""
		}
		if useColor {
			return "\x1b[35m" + name + "\x1b[0m:"
		}
		return name + ":"
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)

	// -q and -l only need to know WHETHER a file matched; stop at the
	// first hit instead of collecting them all.
	effMax := *maxCount
	if quiet || listFiles {
		effMax = 1
	}

	totalSelected := 0
	filesSelected := 0
	sawError := false
	for _, path := range files {
		layer, err := loadLayer(path)
		if err != nil {
			sawError = true
			fmt.Fprintln(os.Stderr, "parch: "+err.Error())
			continue
		}
		if path == stdinName {
			path = "(stdin)"
		}

		// The per-file selection: hits, or with -v the blocks without one.
		var hits []textlayer.Hit
		var blocks []*textlayer.Block
		n := 0
		if invert {
			blocks = unmatchedBlocks(matcher, layer, effMax)
			n = len(blocks)
		} else {
			hits = findHits(matcher, layer, effMax)
			n = len(hits)
		}
		totalSelected += n
		if n > 0 {
			filesSelected++
		}

		switch {
		case quiet:
			if n > 0 {
				os.Exit(0)
			}
		case listFiles:
			if n > 0 {
				if nullSep {
					fmt.Print(path + "\x00")
				} else {
					fmt.Println(path)
				}
			}
		case countOnly:
			fmt.Printf("%s%d\n", prefixFor(path), n)
		case *asJSON && multi:
			// NDJSON: one standalone object per hit (or per unmatched
			// block with -v) — streams straight into jq.
			if err := emitNDJSON(enc, path, layer, hits, blocks, *context); err != nil {
				die(err)
			}
		case *asJSON:
			if err := emitJSONDocument(query, path, layer, hits, blocks, invert, *context); err != nil {
				die(err)
			}
		default:
			for _, h := range hits {
				box := h.Box()
				fmt.Printf("%s#%d %s @%d,%d  %s\n", prefixFor(path), h.Block.ID, h.Block.Type,
					box[0], box[1], h.Context(*context, openMark, closeMark))
			}
			for _, b := range blocks {
				fmt.Printf("%s#%d %s  %s\n", prefixFor(path), b.ID, b.Type, snippet(b.Text, *context*2))
			}
		}
	}

	if !quiet && !listFiles && !countOnly && !*asJSON {
		what := "match(es)"
		if invert {
			what = "block(s) without a match"
		}
		if multi {
			fmt.Fprintf(os.Stderr, "%d %s in %d of %d file(s)\n", totalSelected, what, filesSelected, len(files))
		} else {
			fmt.Fprintf(os.Stderr, "%d %s\n", totalSelected, what)
		}
	}

	switch {
	case totalSelected > 0:
		// 0: something matched somewhere, even if other files errored.
	case sawError:
		os.Exit(2)
	default:
		os.Exit(1)
	}
}

// findHits collects a matcher's hits across a layer, stopping at max
// (0 = unlimited) so -m doesn't pay for matches it will not print.
func findHits(m textlayer.Matcher, layer *textlayer.Layer, max int) []textlayer.Hit {
	var hits []textlayer.Hit
	for i := range layer.Blocks {
		hits = append(hits, m.FindBlock(&layer.Blocks[i])...)
		if max > 0 && len(hits) >= max {
			return hits[:max]
		}
	}
	return hits
}

// unmatchedBlocks is the -v selection: blocks with no match, capped at
// max (0 = unlimited).
func unmatchedBlocks(m textlayer.Matcher, layer *textlayer.Layer, max int) []*textlayer.Block {
	var out []*textlayer.Block
	for i := range layer.Blocks {
		b := &layer.Blocks[i]
		if len(m.FindBlock(b)) == 0 {
			out = append(out, b)
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	return out
}

// emitNDJSON writes one JSON object per selected hit/block, each line
// standalone (archive identity included) for streaming multi-file output.
func emitNDJSON(enc *json.Encoder, path string, layer *textlayer.Layer, hits []textlayer.Hit, blocks []*textlayer.Block, context int) error {
	for _, h := range hits {
		line := struct {
			Archive string        `json:"archive"`
			URL     string        `json:"url"`
			Block   int           `json:"block"`
			Type    string        `json:"type"`
			Range   [2]int        `json:"range"`
			Match   string        `json:"match"`
			Context string        `json:"context"`
			Box     textlayer.Box `json:"box"`
		}{path, layer.URL, h.Block.ID, h.Block.Type, [2]int{h.Start, h.End}, h.Text(), h.Context(context, "«", "»"), h.Box()}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	for _, b := range blocks {
		line := struct {
			Archive  string `json:"archive"`
			URL      string `json:"url"`
			Block    int    `json:"block"`
			Type     string `json:"type"`
			Text     string `json:"text"`
			Inverted bool   `json:"inverted"`
		}{path, layer.URL, b.ID, b.Type, b.Text, true}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return nil
}

// emitJSONDocument writes the single-file JSON envelope (the original
// `find -json` format, kept stable for existing consumers).
func emitJSONDocument(query, path string, layer *textlayer.Layer, hits []textlayer.Hit, blocks []*textlayer.Block, inverted bool, context int) error {
	if inverted {
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
			Query:      query,
			Normalizer: textlayer.NormVersion,
			Archive:    path,
			URL:        layer.URL,
			Inverted:   true,
			Blocks:     []jsonBlock{},
		}
		for _, b := range blocks {
			out.Blocks = append(out.Blocks, jsonBlock{Block: b.ID, Type: b.Type, Text: b.Text})
		}
		enc, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(enc))
		return nil
	}

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
		Query:      query,
		Normalizer: textlayer.NormVersion,
		Archive:    path,
		URL:        layer.URL,
		Hits:       []jsonHit{},
	}
	for _, h := range hits {
		jh := jsonHit{
			Block: h.Block.ID,
			Type:  h.Block.Type,
			Range: [2]int{h.Start, h.End},
			Match: h.Text(),
			Text:  h.Context(context, "«", "»"),
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
		return err
	}
	fmt.Println(string(enc))
	return nil
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
