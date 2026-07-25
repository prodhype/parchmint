package capture

import (
	"context"

	"github.com/chromedp/cdproto/emulation"

	"github.com/chromedp/chromedp"
	"github.com/goodblaster/errors"
	"github.com/goodblaster/parchmint/internal/log"
	"github.com/goodblaster/parchmint/textlayer"
	"github.com/goodblaster/pscription/actions"
	"github.com/goodblaster/pscription/runner"
	js "github.com/goodblaster/pscription/scripts"
)

// MarkOptions style the highlights `parch mark` applies to an archive.
type MarkOptions struct {
	// Grayscale mutes images that contain hits so the highlight pops.
	Grayscale bool
	// Color fills highlight rectangles baked into images.
	Color string
	// Stroke outlines them (empty = no outline).
	Stroke string
	// Match selects how the phrases match (zero value = the default
	// phrase matcher).
	Match textlayer.MatchOptions
	// Colors gives phrase i its own highlight color, DOM marks and baked
	// image rectangles alike (missing entries fall back to the default /
	// Color). Where different-color highlights overlap, the LAST phrase
	// wins — the one deterministic rule.
	Colors []string
	// Style picks the mark presentation: bg (default), underline, box,
	// or bold. Baked image highlights render box as an outline; the
	// other styles fill (pixels can't underline).
	Style string
}

// MarkResult reports what MarkArchive did.
type MarkResult struct {
	HTML         []byte
	DOMMatches   int
	OCRMatches   int
	ImagesMarked int
}

// MarkArchive loads an existing archive (file:// URL) at its recorded
// viewport and produces a marked copy: DOM text matches wrapped in
// <mark data-parchmint>, and OCR matches (from `parch index`) BAKED into
// their images as translucent rectangles — image-relative coordinates, so
// the highlight is correct at any viewer size, in any renderer, with no
// scripts. layer is the archive's embedded layer (for the OCR blocks);
// DOM matching runs against a FRESH extraction of the loaded document, and
// the marks are wrapped against that same extraction's cached run maps, so
// every match lands even though a bare archive's fonts/layout are still
// settling (which would shift block boundaries between two separate
// walks).
func MarkArchive(ctx context.Context, cfg runner.Config, fileURL string, phrases []string, layer *textlayer.Layer, opts MarkOptions) (*MarkResult, error) {
	if layer.Viewport.Width > 0 {
		cfg.ViewportWidth = int64(layer.Viewport.Width)
	}
	// BypassCSP lets our injected data URIs load — but it also disarms the
	// archive's script-blocking meta CSP, so any residual script in an
	// (old or third-party) archive could re-run and mutate the page
	// mid-mark. Disable page script execution outright; CDP evaluate is
	// unaffected, and archives are static documents that need no JS.
	cfg.PreNavigate = append(cfg.PreNavigate,
		actions.BypassCSP(),
		emulation.SetScriptExecutionDisabled(true))

	session, err := runner.Start(ctx, fileURL, cfg)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	// Matches inside same-origin iframes cannot be highlighted in the copy:
	// re-opened from file://, Chrome treats subframes as cross-origin, so
	// the walk can't reach their content to wrap marks (and outerHTML would
	// not serialize a live iframe document anyway). Rare — the capture
	// pipeline freezes most iframes to images — but honestly reported.
	if n := frameBlockMatches(layer, phrases, opts.Match); n > 0 {
		log.With("matches", n).Warn("matches inside same-origin iframes are not highlighted in the marked copy")
	}

	res := &MarkResult{}
	err = session.Run(func(ctx context.Context) error {
		// Fonts settle geometry; the walk measures.
		if err := js.EvalSource(ctx, `(async () => { await document.fonts.ready; })`, nil); err != nil {
			log.WithError(err).Debug("fonts.ready wait failed; continuing")
		}

		// DOM text: fresh extraction (caching run maps), match, then wrap
		// against that same extraction — no second walk to diverge.
		payload, err := extractPayload(ctx, true)
		if err != nil {
			return err
		}
		n, err := applyHighlights(ctx, payload, phrases, opts.Match, opts.Colors, opts.Style)
		if err != nil {
			return err
		}
		res.DOMMatches = n

		// OCR text: matched against the EMBEDDED layer's ocr blocks (a
		// fresh walk cannot see inside images), baked via canvas.
		specs, ocrMatches, err := ocrMarkSpecs(layer, phrases, opts.Match)
		if err != nil {
			return err
		}
		res.OCRMatches = ocrMatches
		if len(specs) > 0 {
			var stats struct {
				Marked int `json:"marked"`
				Missed int `json:"missed"`
			}
			if err := js.BakeImageMarks.Action(&stats, specs, map[string]any{
				"grayscale": opts.Grayscale,
				"color":     opts.Color,
				"stroke":    opts.Stroke,
				"colors":    opts.Colors,
				"style":     opts.Style,
			}).Do(ctx); err != nil {
				return errors.Wrap(err, "bake image marks")
			}
			res.ImagesMarked = stats.Marked
			if stats.Missed > 0 {
				log.With("missed", stats.Missed).Warn("some images could not be marked")
			}
		}

		// Inline linked stylesheets before serializing: an MHT source
		// keeps its CSS in cid: parts, which die in a standalone HTML
		// copy (unstyled page, hidden text visible). The CSSOM has the
		// rules regardless of where they came from; HTML sources have no
		// <link> sheets left, so this is a no-op there.
		if err := chromedp.Evaluate(inlineLinkedStylesheets, nil).Do(ctx); err != nil {
			log.WithError(err).Warn("could not inline stylesheets; marked copy may lose styling")
		}

		var html string
		if err := chromedp.Evaluate(`'<!DOCTYPE html>' + document.documentElement.outerHTML`, &html).Do(ctx); err != nil {
			return errors.Wrap(err, "serialize marked document")
		}
		res.HTML = []byte(html)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// frameBlockMatches counts phrase matches that fall inside same-origin
// iframe blocks of the layer (Frame set) — the matches a marked copy
// cannot highlight.
func frameBlockMatches(layer *textlayer.Layer, phrases []string, match textlayer.MatchOptions) int {
	n := 0
	for _, phrase := range phrases {
		m, err := textlayer.Compile(phrase, match)
		if err != nil {
			continue
		}
		for i := range layer.Blocks {
			if layer.Blocks[i].Frame == "" {
				continue
			}
			n += len(m.FindBlock(&layer.Blocks[i]))
		}
	}
	return n
}

// inlineLinkedStylesheets replaces every <link rel=stylesheet> with a
// <style> holding its rendered rules, read from the CSSOM.
const inlineLinkedStylesheets = `(() => {
	for (const sheet of Array.from(document.styleSheets)) {
		const node = sheet.ownerNode;
		if (!node || node.tagName !== 'LINK') continue;
		let css = '';
		try {
			for (const r of sheet.cssRules) css += r.cssText + '\n';
		} catch (e) { continue; }
		const style = document.createElement('style');
		style.textContent = css;
		node.replaceWith(style);
	}
})()`

// ocrMarkSpecs matches phrases against the layer's OCR blocks and returns
// bake specs: image hash → highlight rects as fractions of the image box
// (block.Box IS the source image's recorded box, so fractions transfer to
// natural resolution unchanged). Each rect's fifth element is the phrase
// index, for per-term colors; rects paint in phrase order, so overlaps
// resolve to "last term wins" like the DOM marks.
func ocrMarkSpecs(layer *textlayer.Layer, phrases []string, match textlayer.MatchOptions) (map[string][][5]float64, int, error) {
	specs := map[string][][5]float64{}
	matches := 0
	for term, phrase := range phrases {
		q, err := textlayer.Compile(phrase, match)
		if err != nil {
			return nil, 0, err
		}
		for i := range layer.Blocks {
			b := &layer.Blocks[i]
			if b.Source != "ocr" || b.Image == "" || b.Box[2] == 0 || b.Box[3] == 0 {
				continue
			}
			for _, hit := range q.FindBlock(b) {
				matches++
				for _, r := range mergeLineRects(hit.Words()) {
					specs[b.Image] = append(specs[b.Image], [5]float64{
						float64(r[0]-b.Box[0]) / float64(b.Box[2]),
						float64(r[1]-b.Box[1]) / float64(b.Box[3]),
						float64(r[2]) / float64(b.Box[2]),
						float64(r[3]) / float64(b.Box[3]),
						float64(term),
					})
				}
			}
		}
	}
	return specs, matches, nil
}

// mergeLineRects joins a hit's word boxes into per-line rectangles, so a
// phrase highlights as one continuous band (spaces included) instead of
// per-word confetti — the image-side analogue of the DOM marks' contiguous
// wrapping. Words share a line when they overlap vertically; the merge
// bridges the inter-word gap.
func mergeLineRects(words []textlayer.Word) [][4]int {
	var out [][4]int
	for _, w := range words {
		x, y, ww, wh := w.Box[0], w.Box[1], w.Box[2], w.Box[3]
		merged := false
		for i := range out {
			r := &out[i]
			sameLine := y < r[1]+r[3] && y+wh > r[1]
			adjacent := x <= r[0]+r[2]+wh && x+ww >= r[0]-wh
			if sameLine && adjacent {
				x2 := max(r[0]+r[2], x+ww)
				y2 := max(r[1]+r[3], y+wh)
				if x < r[0] {
					r[0] = x
				}
				if y < r[1] {
					r[1] = y
				}
				r[2] = x2 - r[0]
				r[3] = y2 - r[1]
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, [4]int{x, y, ww, wh})
		}
	}
	return out
}
