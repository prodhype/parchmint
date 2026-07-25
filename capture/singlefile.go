package capture

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/goodblaster/errors"
)

//go:embed vendor/single-file-bundle.js
var singleFileBundleJS string

// SingleFile captures the page as a self-contained HTML document using the
// vendored SingleFile library (the engine behind the browser extension).
// All resources are embedded as data URIs; the output needs no network to
// render. This is the preferred capture backend. When the capture ran with
// Options.TextLayer, the extracted layer (Snapshot.TextLayer) is embedded
// in the output.
type SingleFile struct{}

func (SingleFile) Name() string { return "singlefile" }
func (SingleFile) Ext() string  { return ".html" }

type singleFilePageData struct {
	Content  string `json:"content"`
	Title    string `json:"title"`
	Filename string `json:"filename"`
}

func (SingleFile) Action(snap *Snapshot) chromedp.ActionFunc {
	return func(ctx context.Context) error {
		script, err := extractScriptFromBundle(singleFileBundleJS)
		if err != nil {
			return errors.Wrap(err, "failed to extract SingleFile script")
		}

		// Inject and initialize SingleFile in the page.
		if err := chromedp.Evaluate(script, nil).Do(ctx); err != nil {
			return errors.Wrap(err, "failed to inject SingleFile")
		}
		initScript := `window.singlefile.init({ fetch: (url, options) => fetch(url, options) });`
		if err := chromedp.Evaluate(initScript, nil).Do(ctx); err != nil {
			return errors.Wrap(err, "failed to initialize SingleFile")
		}
		if err := chromedp.Sleep(500 * time.Millisecond).Do(ctx); err != nil {
			return err
		}

		// Run the capture; getPageData is async so we await the promise.
		// blockScripts is essential: a snapshot that keeps the site's JS
		// re-runs it on open (age gates, preloaders, ...) against a page
		// with no backend, which breaks the frozen state.
		captureScript := `
			(async () => {
				return await window.singlefile.getPageData({
					removeHiddenElements: false,
					removeUnusedStyles: true,
					removeUnusedFonts: true,
					blockScripts: true,
					compressHTML: true,
					insertMetaCSP: true
				});
			})()
		`
		v, exception, err := runtime.Evaluate(captureScript).
			WithAwaitPromise(true).
			WithReturnByValue(true).
			Do(ctx)
		if err != nil {
			return errors.Wrap(err, "SingleFile capture failed")
		}
		if exception != nil {
			return fmt.Errorf("SingleFile exception %q (%d:%d): %s",
				exception.Text, exception.LineNumber, exception.ColumnNumber,
				exception.Exception.Description)
		}
		if v.Value == nil {
			return errors.New("SingleFile returned no data")
		}

		var pageData singleFilePageData
		if err := json.Unmarshal(v.Value, &pageData); err != nil {
			return errors.Wrap(err, "failed to decode SingleFile result")
		}

		snap.MIME = "text/html"
		snap.Bytes = []byte(pageData.Content)
		snap.Bytes = embedFaviconsInHTML(snap.Bytes, snap.favicons)
		if snap.TextLayer != nil {
			snap.Bytes = spliceTextLayer(snap.Bytes, snap.TextLayer)
		}
		if pageData.Title != "" {
			snap.Title = pageData.Title
		}
		return nil
	}
}

// extractScriptFromBundle pulls the `const script = "..."` string literal out
// of the vendored ES6 module and unescapes it.
func extractScriptFromBundle(bundle string) (string, error) {
	const marker = `const script = "`
	start := strings.Index(bundle, marker)
	if start == -1 {
		return "", fmt.Errorf("could not find script constant in bundle")
	}
	start += len(marker)

	escaped := false
	end := -1
	for i := start; i < len(bundle); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch bundle[i] {
		case '\\':
			escaped = true
		case '"':
			if i+1 < len(bundle) && bundle[i+1] == ';' {
				end = i
			}
		}
		if end != -1 {
			break
		}
	}
	if end == -1 {
		return "", fmt.Errorf("could not find end of script constant")
	}

	unescaped, err := strconv.Unquote(`"` + bundle[start:end] + `"`)
	if err != nil {
		return "", fmt.Errorf("failed to unescape script: %w", err)
	}
	return unescaped, nil
}
