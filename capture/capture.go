// Package capture serializes a prepared page into a self-contained snapshot.
//
// A Backend decides the output format (SingleFile HTML, MHTML, screenshot);
// Capture runs a preparation recipe and then the backend in one browser
// session.
package capture

import (
	"context"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/goodblaster/errors"
	"github.com/goodblaster/parchmint/internal/log"
	"github.com/goodblaster/parchmint/textlayer"
	"github.com/goodblaster/pscription/pipeline"
	"github.com/goodblaster/pscription/runner"
	js "github.com/goodblaster/pscription/scripts"
)

// maxCaptureAttempts bounds the retry loop for pages that come out of
// preparation in a detectably broken state (e.g. a carousel library that
// intermittently fails to initialize in headless, leaving slides stacked).
const maxCaptureAttempts = 3

// Snapshot is the result of capturing a page.
type Snapshot struct {
	URL   string
	Title string
	MIME  string
	Bytes []byte

	// ViewportWidth is the CSS-pixel layout width the page was rendered
	// at, from the session config. Backends that need the page's width
	// (PDF paper sizing) read it from here — never from a global.
	ViewportWidth int64

	// TextLayer is the composed parchmint-text/1 document, extracted from
	// the prepared page when Options.TextLayer is set; the SingleFile
	// backend embeds it. Nil when extraction was off or failed.
	TextLayer []byte

	favicons []faviconEmbed
}

// Options control capture extras beyond the recipe and backend.
type Options struct {
	// TextLayer extracts the text layer (TEXTLAYER.md) from the prepared
	// page into Snapshot.TextLayer, for backends that can embed it.
	TextLayer bool

	// Favicon inlines the page's favicon link(s) into the prepared DOM before
	// serialization, for archive backends that preserve HTML head metadata.
	Favicon bool

	// Highlight wraps every match of these phrases in
	// <mark data-parchmint> before serialization — same matcher as
	// `parch find` — so highlights appear in every backend's output.
	Highlight []string

	// Match selects how the Highlight phrases match (zero value = the
	// default phrase matcher).
	Match textlayer.MatchOptions
}

// Backend serializes the current page state into a snapshot.
type Backend interface {
	// Name identifies the backend (e.g. "singlefile", "mht").
	Name() string

	// Ext is the conventional file extension for this backend's output,
	// including the dot (e.g. ".html").
	Ext() string

	// Action fills snap from the current browser state. It runs after the
	// preparation recipe in the same session.
	Action(snap *Snapshot) chromedp.ActionFunc
}

// Capture loads url, runs the preparation recipe, and serializes the page
// with the given backend, all in one browser session.
func Capture(ctx context.Context, url string, recipe pipeline.Recipe, backend Backend) (*Snapshot, error) {
	return CaptureWithOptions(ctx, url, runner.DefaultConfig(), recipe, backend, Options{})
}

// CaptureWithConfig is Capture with a custom runner configuration.
func CaptureWithConfig(ctx context.Context, url string, cfg runner.Config, recipe pipeline.Recipe, backend Backend) (*Snapshot, error) {
	return CaptureWithOptions(ctx, url, cfg, recipe, backend, Options{})
}

// CaptureWithOptions is the full form. If a prepared page is detected to
// be in a broken layout state (a slider that failed to initialize), the
// whole capture is retried in a fresh session, since the failure is
// intermittent.
func CaptureWithOptions(ctx context.Context, url string, cfg runner.Config, recipe pipeline.Recipe, backend Backend, opts Options) (*Snapshot, error) {
	cfg.PreNavigate = append(cfg.PreNavigate, recipe.PreNavigateActions(url)...)

	for attempt := 1; ; attempt++ {
		snap, defect, err := captureOnce(ctx, url, cfg, recipe, backend, opts, attempt < maxCaptureAttempts)
		if err != nil {
			return nil, errors.Wrapf(err, "%s capture failed", backend.Name())
		}
		if !defect {
			return snap, nil
		}
		log.With("attempt", attempt).With("url", url).Warn("layout defect detected (slider not initialized); retrying capture")
	}
}

// captureOnce runs one prepare+capture pass. When retryable is true it checks
// for a layout defect BEFORE serializing (so a doomed attempt doesn't waste
// backend work) and, if found, returns defect=true with no snapshot.
func captureOnce(ctx context.Context, url string, cfg runner.Config, recipe pipeline.Recipe, backend Backend, opts Options, retryable bool) (snap *Snapshot, defect bool, err error) {
	session, err := runner.Start(ctx, url, cfg)
	if err != nil {
		return nil, false, err
	}
	defer session.Close()

	snap = &Snapshot{URL: url, ViewportWidth: cfg.ViewportWidth}
	if err := session.Run(recipe.Action(), captureTitle(&snap.Title)); err != nil {
		return nil, false, err
	}

	if retryable {
		_ = session.Run(detectDefect(&defect))
		if defect {
			// Cheap in-session recovery before escalating to a full
			// re-capture: sliders reliably initialize given the time the
			// later prep steps take, but occasionally one is still pending
			// here — give it a few more seconds in THIS session and re-check.
			// A full retry (fresh navigation) only happens if that fails.
			log.With("url", url).Debug("slider not yet initialized; recovering in-session")
			_ = session.Run(js.WaitForSliders.Action(nil, 5000))
			_ = session.Run(detectDefect(&defect))
		}
		if defect {
			return nil, true, nil
		}
	}

	if opts.Favicon {
		_ = session.Run(func(ctx context.Context) error {
			embeds, n, err := captureFavicon(ctx)
			snap.favicons = embeds
			if err != nil {
				log.WithError(err).Warn("favicon capture had an issue; will embed fetched icons if available")
				return nil
			}
			if n > 0 {
				log.With("icons", n).Debug("captured favicon")
			}
			return nil
		})
	}

	// Text layer and capture-time highlighting, on the prepared page
	// before serialization: the extraction is needed for either, the
	// highlight marks must be in the DOM for every backend to render, and
	// the embedded layer uses the pre-mark payload (measure first — marks
	// are inline and layout-neutral, but the layer stays authoritative).
	// Failures here degrade to a warning: a capture without the extras
	// beats no capture.
	if opts.TextLayer || len(opts.Highlight) > 0 {
		_ = session.Run(func(ctx context.Context) error {
			payload, err := extractPayload(ctx, len(opts.Highlight) > 0)
			if err != nil {
				log.WithError(err).Warn("text layer extraction failed; archiving without it")
				return nil
			}
			if len(opts.Highlight) > 0 {
				if _, err := applyHighlights(ctx, payload, opts.Highlight, opts.Match, nil, ""); err != nil {
					log.WithError(err).Warn("highlighting failed; archiving without marks")
				}
			}
			if opts.TextLayer {
				layer, err := composeLayer(snap, payload)
				if err != nil {
					log.WithError(err).Warn("text layer composition failed; archiving without it")
					return nil
				}
				snap.TextLayer = layer
			}
			return nil
		})
	}

	if err := session.Run(backend.Action(snap)); err != nil {
		return nil, false, err
	}
	return snap, false, nil
}

func detectDefect(defect *bool) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		var res struct {
			Defect bool `json:"defect"`
		}
		if err := js.DetectSliderDefect.Action(&res)(ctx); err != nil {
			return err
		}
		*defect = res.Defect
		return nil
	}
}

func captureTitle(title *string) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		return chromedp.Title(title).Do(ctx)
	}
}

// flattenFixedPositioning converts position:fixed elements to absolute.
// Chrome's print engine paints fixed elements on EVERY page, so a fixed nav
// bar becomes a header repeated on every PDF page. Absolute stops that repeat
// (only fixed repeats) while keeping the element out of normal flow — so,
// unlike converting to static, it adds no height and can't tip a borderline
// page into pagination. The element keeps its top/left placement and appears
// once. Only fixed is touched — sticky already prints once in flow. PDF-only:
// full-page screenshots aren't paginated, so fixed elements already appear
// once there.
func flattenFixedPositioning(ctx context.Context) error {
	return chromedp.Evaluate(
		`(() => {
			for (const el of document.querySelectorAll('body *')) {
				if (getComputedStyle(el).position === 'fixed') {
					el.style.setProperty('position', 'absolute', 'important');
				}
			}
		})()`,
		nil).Do(ctx)
}

// normalizeBackgroundAttachment forces background-attachment:scroll. Chrome's
// print pipeline (PDF) and full-page screenshots don't paint fixed (parallax)
// backgrounds beyond the first viewport, so such sections come out blank —
// and any white-on-dark text in them becomes invisible. Parallax is
// meaningless in a static capture anyway. Used by the "flat" backends (PDF,
// PNG, JPEG); the HTML/MHT backends render in a real browser and don't need
// it. Background-attachment doesn't affect layout, so this triggers no
// reflow.
func normalizeBackgroundAttachment(ctx context.Context) error {
	return chromedp.Evaluate(
		`(() => { const s = document.createElement('style');
			s.textContent = '*{background-attachment:scroll !important}';
			(document.head || document.documentElement).appendChild(s); })()`,
		nil).Do(ctx)
}

// captureFullImage takes a full-page screenshot in the given lossy format,
// clamping the captured height to the format's per-dimension encoder limit
// (WebP: 16383, JPEG: 65535). Chrome doesn't error past the limit — it
// silently returns ZERO bytes — so tall pages must be cut off at the
// bottom to produce anything at all.
func captureFullImage(ctx context.Context, format page.CaptureScreenshotFormat, quality, maxDim int64) ([]byte, error) {
	_, _, _, _, _, cssContentSize, err := page.GetLayoutMetrics().Do(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get layout metrics")
	}
	if cssContentSize == nil {
		return nil, errors.New("layout metrics returned no content size")
	}
	width, height := cssContentSize.Width, cssContentSize.Height
	if width > float64(maxDim) {
		// Cropping horizontally would cut into every line of the page;
		// unlike height there is no sensible partial capture.
		return nil, errors.Newf("page width %.0f exceeds the %s encoder limit %d", width, format, maxDim)
	}
	if height > float64(maxDim) {
		log.With("height", int64(height)).With("limit", maxDim).With("format", string(format)).
			Warn("page taller than the encoder limit; capture is cut off at the bottom (use the png backend for full length)")
		height = float64(maxDim)
	}

	buf, err := page.CaptureScreenshot().
		WithCaptureBeyondViewport(true).
		WithFromSurface(true).
		WithFormat(format).
		WithQuality(quality).
		WithClip(&page.Viewport{X: 0, Y: 0, Width: width, Height: height, Scale: 1}).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	if len(buf) == 0 {
		return nil, errors.Newf("Chrome returned an empty %s screenshot", string(format))
	}
	return buf, nil
}
