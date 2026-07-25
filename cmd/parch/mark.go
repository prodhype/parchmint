package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goodblaster/parchmint/capture"
	"github.com/goodblaster/parchmint/textlayer"
	"github.com/goodblaster/pscription/runner"
)

// runMarkCommand implements `parch mark <phrase> <archive.html>`:
// highlight-on-read. Produces a marked COPY of an existing archive — DOM
// matches wrapped in <mark>, OCR matches (parch index) baked into their
// images — closing the loop: capture → index → store anywhere → find →
// view with highlights, even when the text lives inside an image.
func runMarkCommand(args []string) {
	fs := flag.NewFlagSet("mark", flag.ExitOnError)
	output := fs.String("o", "", "output file (default <archive>.marked.html; '-' for stdout)")
	grayscale := fs.Bool("grayscale", false, "mute images containing hits so the highlight pops")
	color := fs.String("color", "rgba(255, 220, 0, 0.5)", "highlight fill for image hits")
	timeout := fs.Int("timeout", 60, "timeout in seconds")
	var termColors stringsFlag
	fs.Var(&termColors, "c", "highlight color for the Nth phrase (repeatable, pairs with phrases in order; unpaired phrases keep the default)")
	style := fs.String("style", "", "highlight style: bg (default), underline, box, or bold")
	match := registerMatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s mark [-grayscale] [-o out.html] <phrase> [phrase ...] <archive.html>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "'-' as the archive reads it from stdin (output then defaults to stdout).")
		fmt.Fprintln(os.Stderr, "Every argument before the archive is a phrase; all are highlighted")
		fmt.Fprintln(os.Stderr, "(overlaps merge; with -c colors, the last phrase wins). Same matching")
		fmt.Fprintln(os.Stderr, "as `parch find`, including its mode flags (-e regex, …). Run")
		fmt.Fprintln(os.Stderr, "`parch index` first if you want matches inside images.")
		fmt.Fprintln(os.Stderr, "\nExample: parch mark -c yellow -c cyan revenue profit report.html")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, matchBoolFlags(map[string]bool{"grayscale": true})))
	if err := validStyle(*style); err != nil {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(2)
	}
	phrases := fs.Args()[:fs.NArg()-1]
	path := fs.Arg(fs.NArg() - 1)

	die := func(err error) {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}

	// Fail fast on a bad query (a typo'd -e regex, a contradictory flag
	// combo) BEFORE paying for a browser session; the capture layer
	// compiles again per phrase, but by then compilation is known-good.
	for _, p := range phrases {
		if _, err := textlayer.Compile(p, match.options()); err != nil {
			die(err)
		}
	}

	srcData, srcName, err := readArchive(path)
	if err != nil {
		die(err)
	}
	layer, err := textlayer.FromBytes(srcData, srcName)
	if err != nil {
		die(err)
	}
	isMHT := textlayer.IsMHT(srcData)

	// The browser loads the archive over file://; a stdin archive is
	// spilled to a temp file first (removed after the session ends).
	var abs, tmp string
	if path == stdinName {
		if tmp, err = spillArchive(srcData); err != nil {
			die(err)
		}
		abs = tmp
	} else if abs, err = filepath.Abs(path); err != nil {
		die(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	res, err := capture.MarkArchive(ctx, runner.DefaultConfig(), "file://"+abs, phrases, layer, capture.MarkOptions{
		Grayscale: *grayscale,
		Color:     *color,
		Stroke:    "rgba(200, 160, 0, 0.9)",
		Match:     match.options(),
		Colors:    termColors,
		Style:     *style,
	})
	if tmp != "" {
		os.Remove(tmp) // session is over; the spilled stdin copy is done
	}
	if err != nil {
		die(err)
	}

	total := res.DOMMatches + res.OCRMatches
	if total == 0 {
		fmt.Fprintln(os.Stderr, "0 matches; nothing written")
		os.Exit(1)
	}

	// The marked copy keeps the source's format: for an MHT, put the
	// marked document back into the container (every other part — and so
	// every cid: reference — stays intact).
	out := res.HTML
	markedExt := ".marked.html"
	if isMHT {
		out, err = textlayer.ReplaceMHTDocument(srcData, res.HTML)
		if err != nil {
			die(err)
		}
		markedExt = ".marked.mht"
	}

	dest := *output
	if dest == "" && path == stdinName {
		dest = "-" // stdin in, stdout out: no filename to derive from
	}
	switch dest {
	case "-":
		if _, err := os.Stdout.Write(out); err != nil {
			die(err)
		}
		dest = "(stdout)"
	case "":
		dest = strings.TrimSuffix(path, filepath.Ext(path)) + markedExt
		fallthrough
	default:
		if err := os.WriteFile(dest, out, 0o644); err != nil {
			die(err)
		}
	}

	fmt.Fprintf(os.Stderr, "%s: %d text match(es), %d image match(es), %d image(s) marked\n",
		dest, res.DOMMatches, res.OCRMatches, res.ImagesMarked)
}
