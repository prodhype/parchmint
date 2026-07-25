package capture

import (
	"context"
	"encoding/json"
	"time"

	"github.com/goodblaster/errors"
	"github.com/goodblaster/parchmint/internal/log"
	"github.com/goodblaster/parchmint/textlayer"
	js "github.com/goodblaster/pscription/scripts"
)

// TextLayerType is the script-element type identifying the embedded text
// layer in an HTML archive. See TEXTLAYER.md for the format contract.
const TextLayerType = textlayer.ScriptType

// textLayerPayload is what the extraction script returns; the header
// fields (format/url/capturedAt/generator) are composed Go-side.
type textLayerPayload struct {
	Viewport json.RawMessage `json:"viewport"`
	Stats    json.RawMessage `json:"stats"`
	Blocks   json.RawMessage `json:"blocks"`
	Images   json.RawMessage `json:"images"`
}

// extractPayload runs the text-layer extraction against the live,
// prepared page. When cacheRuns is set, the walk stashes each block's
// text-node map on window.__pmRunCache so a following applyTextMarks can
// wrap ranges against this exact extraction (see applyHighlights).
func extractPayload(ctx context.Context, cacheRuns bool) (*textLayerPayload, error) {
	var payload textLayerPayload
	if err := js.ExtractTextLayer.Action(&payload, cacheRuns).Do(ctx); err != nil {
		return nil, errors.Wrap(err, "text layer extraction")
	}

	var st struct {
		Blocks           int      `json:"blocks"`
		Words            int      `json:"words"`
		OracleMismatches int      `json:"oracleMismatches"`
		PageSimilarity   *float64 `json:"pageSimilarity"`
	}
	_ = json.Unmarshal(payload.Stats, &st)
	l := log.With("blocks", st.Blocks).With("words", st.Words).With("oracleMismatches", st.OracleMismatches)
	if st.PageSimilarity != nil {
		l = l.With("pageSimilarity", *st.PageSimilarity)
	}
	l.Debug("extracted text layer")
	return &payload, nil
}

// markEntry mirrors the marks parameter of apply_text_marks.js: the
// UTF-16 ranges to wrap in a block, each tagged with the query term that
// owns it (for per-term colors).
type markEntry struct {
	Ranges [][3]int `json:"ranges"`
}

// applyHighlights matches the phrases against an extracted payload and
// wraps every match in <mark data-parchmint>. Matching uses the same Go
// matcher as `parch find`, so highlighting and archive search agree by
// construction. The marks are applied by apply_text_marks.js against the
// SAME extraction (its cached run maps) — not a second walk — so every
// match lands even when fonts/layout shift block boundaries between
// calls. Requires the preceding extractPayload to have run with
// cacheRuns=true. Marks land in the DOM before serialization, so every
// backend shows them — yellow pixels in screenshots and PDFs, <mark>
// elements in HTML.
//
// colors gives phrase i its own highlight color (nil / missing entries =
// the default <mark> look); style picks the mark presentation (bg,
// underline, box, bold — empty = bg). Ranges are flattened Go-side to
// DISJOINT per-block spans with "last term wins" on overlap, because the
// JS marker must wrap everything in one back-to-front pass against the
// cached extraction (a second pass would see nodes the first one split).
func applyHighlights(ctx context.Context, payload *textLayerPayload, phrases []string, match textlayer.MatchOptions, colors []string, style string) (int, error) {
	var blocks []textlayer.Block
	if err := json.Unmarshal(payload.Blocks, &blocks); err != nil {
		return 0, errors.Wrap(err, "parse blocks")
	}

	hitsByTerm := make([][]textlayer.Hit, len(phrases))
	total := 0
	for ti, phrase := range phrases {
		m, err := textlayer.Compile(phrase, match)
		if err != nil {
			return 0, err
		}
		for i := range blocks {
			hitsByTerm[ti] = append(hitsByTerm[ti], m.FindBlock(&blocks[i])...)
		}
		total += len(hitsByTerm[ti])
	}
	if total == 0 {
		log.With("phrases", len(phrases)).With("matches", 0).Info("highlighting matches")
		return 0, nil
	}

	marks := map[int]markEntry{}
	for id, ranges := range textlayer.MergeTermRanges(hitsByTerm) {
		e := markEntry{Ranges: make([][3]int, 0, len(ranges))}
		for _, r := range ranges {
			e.Ranges = append(e.Ranges, [3]int{r.Start, r.End, r.Term})
		}
		marks[id] = e
	}

	var st struct {
		Marked  int `json:"marked"`
		Skipped int `json:"skipped"`
	}
	if err := js.ApplyTextMarks.Action(&st, marks, map[string]any{"colors": colors, "style": style}).Do(ctx); err != nil {
		return 0, errors.Wrap(err, "apply text marks")
	}
	log.With("phrases", len(phrases)).With("matches", total).
		With("marked", st.Marked).With("skipped", st.Skipped).Info("highlighting matches")
	return total, nil
}

// composeLayer wraps an extraction payload with the archive header,
// producing the complete parchmint-text/1 document for embedding.
func composeLayer(snap *Snapshot, payload *textLayerPayload) ([]byte, error) {
	layer := struct {
		Format     string          `json:"format"`
		URL        string          `json:"url"`
		CapturedAt string          `json:"capturedAt"`
		Generator  string          `json:"generator"`
		Viewport   json.RawMessage `json:"viewport"`
		Stats      json.RawMessage `json:"stats"`
		Blocks     json.RawMessage `json:"blocks"`
		Images     json.RawMessage `json:"images,omitempty"`
	}{
		Format:     textlayer.Format,
		URL:        snap.URL,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Generator:  "parch",
		Viewport:   payload.Viewport,
		Stats:      payload.Stats,
		Blocks:     payload.Blocks,
		Images:     payload.Images,
	}
	out, err := json.Marshal(layer)
	if err != nil {
		return nil, errors.Wrap(err, "marshal text layer")
	}
	return out, nil
}

// spliceTextLayer embeds the layer in the serialized archive (see
// textlayer.EmbedInHTML for the mechanics).
func spliceTextLayer(html, layer []byte) []byte {
	return textlayer.EmbedInHTML(html, layer)
}
