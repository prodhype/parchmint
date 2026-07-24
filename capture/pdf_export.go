package capture

import (
	"context"

	"github.com/chromedp/cdproto/emulation"

	"github.com/goodblaster/errors"
	"github.com/goodblaster/parchmint/internal/log"
	"github.com/goodblaster/parchmint/textlayer"
	"github.com/goodblaster/pscription/actions"
	"github.com/goodblaster/pscription/runner"
	js "github.com/goodblaster/pscription/scripts"
)

// PDFOptions style the highlights an ExportPDF applies.
type PDFOptions struct {
	// Color fills the highlight behind matched OCR words (the invisible
	// overlay text carries the selection; this makes matches visible).
	Color string
	// Match selects how the phrases match (zero value = the default
	// phrase matcher).
	Match textlayer.MatchOptions
}

// PDFResult reports what ExportPDF produced.
type PDFResult struct {
	PDF            []byte
	DOMMatches     int
	OCRMatches     int
	ImagesOverlaid int
	OverlayWords   int
}

// ExportPDF renders an existing archive (file:// URL) to a PDF that carries
// four layers: native selectable text (Chrome's printToPDF of the DOM),
// invisible selectable text over images at their OCR word positions (so
// image text is searchable in any viewer), visible highlights for the
// given phrases (DOM matches as <mark>, image matches as yellow overlay
// backgrounds), and the parchmint-text/1 JSON as an embedded-file
// attachment (so `parch text`/`find` work on the .pdf). layer is the
// archive's embedded layer; its OCR blocks (from `parch index`) drive the
// image overlays.
func ExportPDF(ctx context.Context, cfg runner.Config, fileURL string, phrases []string, layer *textlayer.Layer, opts PDFOptions) (*PDFResult, error) {
	if layer.Viewport.Width > 0 {
		cfg.ViewportWidth = int64(layer.Viewport.Width)
	}
	// Same rationale as MarkArchive: archives are static; nothing in them
	// should execute while we overlay and print (CDP evaluate still works).
	cfg.PreNavigate = append(cfg.PreNavigate,
		actions.BypassCSP(),
		emulation.SetScriptExecutionDisabled(true))

	session, err := runner.Start(ctx, fileURL, cfg)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	res := &PDFResult{}
	err = session.Run(func(ctx context.Context) error {
		if err := js.EvalSource(ctx, `(async () => { await document.fonts.ready; })`, nil); err != nil {
			log.WithError(err).Debug("fonts.ready wait failed; continuing")
		}

		// DOM text highlights: same matcher and cached-extraction wrap as
		// `parch mark` / capture-time -highlight.
		if len(phrases) > 0 {
			payload, err := extractPayload(ctx, true)
			if err != nil {
				return err
			}
			n, err := applyHighlights(ctx, payload, phrases, opts.Match)
			if err != nil {
				return err
			}
			res.DOMMatches = n
		}

		// OCR image overlays: invisible selectable text for every OCR word,
		// yellow background for matched ones. Injected before printToPDF so
		// Chrome renders them into the PDF.
		specs, ocrMatches := ocrOverlaySpecs(layer, phrases, opts.Match)
		res.OCRMatches = ocrMatches
		if len(specs) > 0 {
			var stats struct {
				Images int `json:"images"`
				Words  int `json:"words"`
				Missed int `json:"missed"`
			}
			color := opts.Color
			if color == "" {
				color = "rgba(255, 220, 0, 0.45)"
			}
			if err := js.OverlayOcrText.Action(&stats, specs, map[string]any{"color": color}).Do(ctx); err != nil {
				return errors.Wrap(err, "overlay ocr text")
			}
			res.ImagesOverlaid = stats.Images
			res.OverlayWords = stats.Words
			if stats.Missed > 0 {
				log.With("missed", stats.Missed).Warn("some images could not be overlaid")
			}
		}

		// Render. Reuse the PDF backend (screen media, fixed-position
		// flattening, single tall page, native text + fonts).
		snap := &Snapshot{URL: layer.URL, ViewportWidth: cfg.ViewportWidth}
		if err := (PDF{}).Action(snap).Do(ctx); err != nil {
			return err
		}

		// Attach the layer JSON so the PDF is at parity with .html/.mht.
		layerJSON, err := layer.Marshal()
		if err != nil {
			return errors.Wrap(err, "marshal text layer")
		}
		withLayer, err := textlayer.EmbedInPDF(snap.Bytes, layerJSON)
		if err != nil {
			log.WithError(err).Warn("could not attach text layer to pdf; the pdf is still searchable natively")
			res.PDF = snap.Bytes
		} else {
			res.PDF = withLayer
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ocrWord is one overlay word: text plus an image-relative box (fractions
// of the displayed image) and whether it matched a phrase.
type ocrWord struct {
	T  string  `json:"t"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	W  float64 `json:"w"`
	H  float64 `json:"h"`
	Hl bool    `json:"hl"`
}

// ocrOverlaySpecs turns the layer's OCR blocks into per-image overlay word
// lists (keyed by image hash), flagging words that match any phrase. The
// int returned is the number of phrase matches inside images.
func ocrOverlaySpecs(layer *textlayer.Layer, phrases []string, match textlayer.MatchOptions) (map[string][]ocrWord, int) {
	queries := make([]textlayer.Matcher, 0, len(phrases))
	for _, p := range phrases {
		if q, err := textlayer.Compile(p, match); err == nil {
			queries = append(queries, q)
		}
	}

	specs := map[string][]ocrWord{}
	matches := 0
	for i := range layer.Blocks {
		b := &layer.Blocks[i]
		if b.Source != "ocr" || b.Image == "" || b.Box[2] == 0 || b.Box[3] == 0 {
			continue
		}

		// Char ranges matched by any phrase, for flagging words.
		var ranges [][2]int
		for _, q := range queries {
			for _, hit := range q.FindBlock(b) {
				matches++
				ranges = append(ranges, [2]int{hit.Start, hit.End})
			}
		}

		fx, fy := float64(b.Box[0]), float64(b.Box[1])
		fw, fh := float64(b.Box[2]), float64(b.Box[3])
		for _, w := range b.Words {
			hl := false
			for _, r := range ranges {
				if w.Start < r[1] && w.End > r[0] {
					hl = true
					break
				}
			}
			specs[b.Image] = append(specs[b.Image], ocrWord{
				T:  textlayer.UTF16Slice(b.Text, w.Start, w.End),
				X:  (float64(w.Box[0]) - fx) / fw,
				Y:  (float64(w.Box[1]) - fy) / fh,
				W:  float64(w.Box[2]) / fw,
				H:  float64(w.Box[3]) / fh,
				Hl: hl,
			})
		}
	}
	return specs, matches
}
