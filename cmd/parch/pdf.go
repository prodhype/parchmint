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

// runPdfCommand implements `parch pdf [phrase ...] <archive.html|.mht>`:
// render an existing archive to a rich PDF — native selectable text,
// invisible selectable text over OCR'd images (searchable in any viewer),
// visible highlights for the given phrases (page text and image text), and
// the parchmint-text/1 layer attached so `parch text`/`find` work on the
// .pdf. Run `parch index` on the archive first for image text.
func runPdfCommand(args []string) {
	fs := flag.NewFlagSet("pdf", flag.ExitOnError)
	output := fs.String("o", "", "output file (default <archive>.pdf; '-' for stdout)")
	color := fs.String("color", "rgba(255, 220, 0, 0.45)", "highlight fill for matched image text")
	timeout := fs.Int("timeout", 120, "timeout in seconds")
	var termColors stringsFlag
	fs.Var(&termColors, "c", "highlight color for the Nth phrase (repeatable, pairs with phrases in order; unpaired phrases keep the default)")
	style := fs.String("style", "", "highlight style: bg (default), underline, box, or bold")
	match := registerMatchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s pdf [-o out.pdf] [phrase ...] <archive.html|.mht>\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "'-' as the archive reads it from stdin (output then defaults to stdout).")
		fmt.Fprintln(os.Stderr, "Renders an archive to a PDF with a searchable text layer (including")
		fmt.Fprintln(os.Stderr, "text inside images, if the archive was `parch index`ed). Any phrases")
		fmt.Fprintln(os.Stderr, "before the archive are highlighted, page text and image text alike;")
		fmt.Fprintln(os.Stderr, "matching modes are the same as `parch find` (-e regex, …), and -c")
		fmt.Fprintln(os.Stderr, "gives each phrase its own color.")
		fmt.Fprintln(os.Stderr, "\nOptions:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(reorderFlags(args, matchBoolFlags(nil)))
	if err := validStyle(*style); err != nil {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(2)
	}
	phrases := fs.Args()[:fs.NArg()-1]
	path := fs.Arg(fs.NArg() - 1)

	die := func(err error) {
		fmt.Fprintln(os.Stderr, "parch: "+err.Error())
		os.Exit(2)
	}

	srcData, srcName, err := readArchive(path)
	if err != nil {
		die(err)
	}
	layer, err := textlayer.FromBytes(srcData, srcName)
	if err != nil {
		die(err)
	}

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

	res, err := capture.ExportPDF(ctx, runner.DefaultConfig(), "file://"+abs, phrases, layer, capture.PDFOptions{
		Color:  *color,
		Match:  match.options(),
		Colors: termColors,
		Style:  *style,
	})
	if tmp != "" {
		os.Remove(tmp) // session is over; the spilled stdin copy is done
	}
	if err != nil {
		die(err)
	}

	dest := *output
	if dest == "" && path == stdinName {
		dest = "-" // stdin in, stdout out: no filename to derive from
	}
	switch dest {
	case "-":
		if _, err := os.Stdout.Write(res.PDF); err != nil {
			die(err)
		}
		dest = "(stdout)"
	case "":
		dest = strings.TrimSuffix(path, filepath.Ext(path)) + ".pdf"
		fallthrough
	default:
		if err := os.WriteFile(dest, res.PDF, 0o644); err != nil {
			die(err)
		}
	}

	fmt.Fprintf(os.Stderr, "%s: %d bytes; %d image(s) overlaid (%d words)",
		dest, len(res.PDF), res.ImagesOverlaid, res.OverlayWords)
	if len(phrases) > 0 {
		fmt.Fprintf(os.Stderr, "; highlighted %d text + %d image match(es)", res.DOMMatches, res.OCRMatches)
	}
	fmt.Fprintln(os.Stderr)
}
