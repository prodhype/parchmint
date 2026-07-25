package capture

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/goodblaster/errors"
)

const maxFaviconBytes = 1 << 20

type faviconDiscovery struct {
	PageURL    string             `json:"pageURL"`
	UserAgent  string             `json:"userAgent"`
	Candidates []faviconCandidate `json:"candidates"`
}

type faviconCandidate struct {
	URL      string `json:"url"`
	Rel      string `json:"rel"`
	Type     string `json:"type"`
	Sizes    string `json:"sizes"`
	Fallback bool   `json:"fallback"`
}

type faviconEmbed struct {
	URL      string `json:"url"`
	Rel      string `json:"rel"`
	MIME     string `json:"mime"`
	Sizes    string `json:"sizes,omitempty"`
	DataURI  string `json:"dataURI"`
	Fallback bool   `json:"fallback"`
}

// captureFavicon inlines the page's icon links as data URIs in the live DOM.
// HTML-like backends then serialize those links without a network dependency.
func captureFavicon(ctx context.Context) ([]faviconEmbed, int, error) {
	discovery, err := discoverFavicons(ctx)
	if err != nil {
		return nil, 0, err
	}
	candidates := normalizeFaviconCandidates(discovery.Candidates)
	if len(candidates) == 0 {
		return nil, 0, nil
	}

	cookies, _ := faviconCookies(ctx, candidates)
	embeds := fetchFaviconEmbeds(ctx, faviconHTTPClient, candidates, discovery.PageURL, discovery.UserAgent, cookies)
	if len(embeds) == 0 {
		return nil, 0, nil
	}
	n, err := injectFavicons(ctx, embeds)
	return embeds, n, err
}

var faviconHTTPClient = &http.Client{Timeout: 30 * time.Second}

func discoverFavicons(ctx context.Context) (faviconDiscovery, error) {
	const script = `(() => {
		const isIconRel = rel => {
			const text = String(rel || "").toLowerCase().trim();
			if (!text) return false;
			const tokens = text.split(/\s+/);
			return tokens.includes("icon") ||
				tokens.includes("apple-touch-icon") ||
				tokens.includes("apple-touch-icon-precomposed") ||
				tokens.includes("mask-icon");
		};
		const candidates = [];
		const base = document.baseURI || location.href;
		for (const el of document.querySelectorAll("link[rel][href]")) {
			const rel = el.getAttribute("rel") || "";
			if (!isIconRel(rel)) continue;
			try {
				candidates.push({
					url: new URL(el.getAttribute("href"), base).href,
					rel,
					type: el.getAttribute("type") || "",
					sizes: el.getAttribute("sizes") || ""
				});
			} catch (_) {}
		}
		try {
			candidates.push({
				url: new URL("/favicon.ico", location.href).href,
				rel: "icon",
				type: "image/x-icon",
				fallback: true
			});
		} catch (_) {}
		return { pageURL: location.href, userAgent: navigator.userAgent, candidates };
	})()`

	var discovery faviconDiscovery
	if err := chromedp.Evaluate(script, &discovery).Do(ctx); err != nil {
		return faviconDiscovery{}, errors.Wrap(err, "discover favicon links")
	}
	return discovery, nil
}

func normalizeFaviconCandidates(in []faviconCandidate) []faviconCandidate {
	out := make([]faviconCandidate, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		if c.URL == "" || seen[c.URL] {
			continue
		}
		if !c.Fallback && !isFaviconRel(c.Rel) {
			continue
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			continue
		}
		switch u.Scheme {
		case "http", "https", "data":
			seen[c.URL] = true
			out = append(out, c)
		}
	}
	return out
}

func isFaviconRel(rel string) bool {
	tokens := strings.Fields(strings.ToLower(rel))
	for _, token := range tokens {
		switch token {
		case "icon", "apple-touch-icon", "apple-touch-icon-precomposed", "mask-icon":
			return true
		}
	}
	return false
}

func faviconCookies(ctx context.Context, candidates []faviconCandidate) ([]*network.Cookie, error) {
	urls := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(c.URL, "http://") || strings.HasPrefix(c.URL, "https://") {
			urls = append(urls, c.URL)
		}
	}
	if len(urls) == 0 {
		return nil, nil
	}
	return network.GetCookies().WithURLs(urls).Do(ctx)
}

func fetchFaviconEmbeds(ctx context.Context, client *http.Client, candidates []faviconCandidate, pageURL, userAgent string, cookies []*network.Cookie) []faviconEmbed {
	var embeds []faviconEmbed
	var fallback []faviconCandidate
	for _, c := range candidates {
		if c.Fallback {
			fallback = append(fallback, c)
			continue
		}
		if embed, ok := fetchFaviconEmbed(ctx, client, c, pageURL, userAgent, cookies); ok {
			embeds = append(embeds, embed)
		}
	}
	if len(embeds) > 0 {
		return embeds
	}
	for _, c := range fallback {
		if embed, ok := fetchFaviconEmbed(ctx, client, c, pageURL, userAgent, cookies); ok {
			return []faviconEmbed{embed}
		}
	}
	return nil
}

func fetchFaviconEmbed(ctx context.Context, client *http.Client, c faviconCandidate, pageURL, userAgent string, cookies []*network.Cookie) (faviconEmbed, bool) {
	if strings.HasPrefix(c.URL, "data:") {
		mimeType := faviconMIMEFromDataURI(c.URL)
		return faviconEmbed{URL: c.URL, Rel: faviconRel(c), MIME: mimeType, Sizes: c.Sizes, DataURI: c.URL, Fallback: c.Fallback}, true
	}

	data, mimeType, err := fetchFaviconHTTP(ctx, client, c, pageURL, userAgent, cookies)
	if err != nil {
		return faviconEmbed{}, false
	}
	return faviconEmbed{
		URL:      c.URL,
		Rel:      faviconRel(c),
		MIME:     mimeType,
		Sizes:    c.Sizes,
		DataURI:  faviconDataURI(mimeType, data),
		Fallback: c.Fallback,
	}, true
}

func fetchFaviconHTTP(ctx context.Context, client *http.Client, c faviconCandidate, pageURL, userAgent string, cookies []*network.Cookie) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	if pageURL != "" {
		req.Header.Set("Referer", pageURL)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if cookieHeader := faviconCookieHeader(c.URL, cookies); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("favicon HTTP status %d", resp.StatusCode)
	}

	data, err := readLimited(resp.Body, maxFaviconBytes)
	if err != nil {
		return nil, "", err
	}
	if isLikelyHTML(data) {
		return nil, "", fmt.Errorf("favicon response is HTML")
	}
	mimeType := faviconMIME(c, resp.Header.Get("Content-Type"), data)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, "", fmt.Errorf("favicon response is %s", mimeType)
	}
	return data, mimeType, nil
}

func readLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("favicon exceeds %d bytes", max)
	}
	return data, nil
}

func faviconCookieHeader(rawURL string, cookies []*network.Cookie) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	var parts []string
	for _, c := range cookies {
		if faviconCookieMatchesURL(c, u) {
			parts = append(parts, (&http.Cookie{Name: c.Name, Value: c.Value}).String())
		}
	}
	return strings.Join(parts, "; ")
}

func faviconCookieMatchesURL(c *network.Cookie, u *url.URL) bool {
	if c == nil || c.Name == "" || u == nil {
		return false
	}
	if c.Secure && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
	if domain == "" || (host != domain && !strings.HasSuffix(host, "."+domain)) {
		return false
	}
	cookiePath := c.Path
	if cookiePath == "" {
		cookiePath = "/"
	}
	if !strings.HasPrefix(u.EscapedPath()+"/", strings.TrimRight(cookiePath, "/")+"/") {
		return false
	}
	return true
}

func faviconMIME(c faviconCandidate, contentType string, data []byte) string {
	for _, raw := range []string{c.Type, contentType} {
		if mimeType := cleanIconMIME(raw); mimeType != "" {
			return mimeType
		}
	}
	if mimeType := mimeByURL(c.URL); mimeType != "" {
		return mimeType
	}
	if isLikelySVG(data) {
		return "image/svg+xml"
	}
	if sniffed := http.DetectContentType(data); strings.HasPrefix(sniffed, "image/") {
		return sniffed
	}
	return "application/octet-stream"
}

func cleanIconMIME(raw string) string {
	if raw == "" {
		return ""
	}
	mimeType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mimeType = strings.TrimSpace(strings.Split(raw, ";")[0])
	}
	mimeType = strings.ToLower(mimeType)
	if mimeType == "image/vnd.microsoft.icon" {
		return "image/x-icon"
	}
	if strings.HasPrefix(mimeType, "image/") {
		return mimeType
	}
	return ""
}

func mimeByURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".ico":
		return "image/x-icon"
	case ".svg":
		return "image/svg+xml"
	}
	return cleanIconMIME(mime.TypeByExtension(path.Ext(u.Path)))
}

func isLikelySVG(data []byte) bool {
	s := strings.TrimSpace(string(data))
	return strings.HasPrefix(s, "<svg") || strings.HasPrefix(s, "<?xml")
}

func isLikelyHTML(data []byte) bool {
	s := strings.ToLower(strings.TrimSpace(string(data)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

func faviconDataURI(mimeType string, data []byte) string {
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func faviconMIMEFromDataURI(dataURI string) string {
	if !strings.HasPrefix(dataURI, "data:") {
		return "image/x-icon"
	}
	rest := strings.TrimPrefix(dataURI, "data:")
	end := strings.IndexAny(rest, ";,")
	if end == -1 {
		return "image/x-icon"
	}
	if mimeType := cleanIconMIME(rest[:end]); mimeType != "" {
		return mimeType
	}
	return "image/x-icon"
}

func faviconRel(c faviconCandidate) string {
	if strings.TrimSpace(c.Rel) != "" {
		return c.Rel
	}
	return "icon"
}

func injectFavicons(ctx context.Context, embeds []faviconEmbed) (int, error) {
	payload, err := json.Marshal(embeds)
	if err != nil {
		return 0, errors.Wrap(err, "marshal favicon embeds")
	}
	script := fmt.Sprintf(`(() => {
		const entries = %s;
		const isIconRel = rel => {
			const text = String(rel || "").toLowerCase().trim();
			const tokens = text.split(/\s+/);
			return tokens.includes("icon") ||
				tokens.includes("apple-touch-icon") ||
				tokens.includes("apple-touch-icon-precomposed") ||
				tokens.includes("mask-icon");
		};
		const head = (() => {
			if (document.head) return document.head;
			const el = document.createElement("head");
			document.documentElement.insertBefore(el, document.body || document.documentElement.firstChild);
			return el;
		})();
		const iconLinks = () => Array.from(document.querySelectorAll("link[rel][href]"))
			.filter(el => isIconRel(el.getAttribute("rel")));
		let updated = 0;
		for (const entry of entries) {
			let matched = false;
			for (const el of iconLinks()) {
				let href;
				try { href = new URL(el.getAttribute("href"), document.baseURI || location.href).href; }
				catch (_) { continue; }
				if (href !== entry.url) continue;
				el.setAttribute("href", entry.dataURI);
				if (entry.mime) el.setAttribute("type", entry.mime);
				if (entry.sizes) el.setAttribute("sizes", entry.sizes);
				matched = true;
				updated++;
			}
			if (!matched) {
				const el = document.createElement("link");
				el.setAttribute("rel", entry.rel || "icon");
				el.setAttribute("href", entry.dataURI);
				if (entry.mime) el.setAttribute("type", entry.mime);
				if (entry.sizes) el.setAttribute("sizes", entry.sizes);
				head.appendChild(el);
				updated++;
			}
		}
		return updated;
	})()`, payload)

	var updated int
	if err := chromedp.Evaluate(script, &updated).Do(ctx); err != nil {
		return 0, errors.Wrap(err, "inject favicon links")
	}
	return updated, nil
}

var faviconLinkRe = regexp.MustCompile(`(?is)<link\b[^>]*\brel\s*=\s*(?:"[^"]*(?:\bicon\b|apple-touch-icon|apple-touch-icon-precomposed|mask-icon)[^"]*"|'[^']*(?:\bicon\b|apple-touch-icon|apple-touch-icon-precomposed|mask-icon)[^']*'|[^\s>]*(?:icon|apple-touch-icon|apple-touch-icon-precomposed|mask-icon)[^\s>]*)[^>]*>`)

func embedFaviconsInHTML(html []byte, embeds []faviconEmbed) []byte {
	if len(embeds) == 0 {
		return html
	}
	html = faviconLinkRe.ReplaceAll(html, nil)

	var tags bytes.Buffer
	tags.WriteByte('\n')
	for _, embed := range embeds {
		tags.WriteString(`<link rel="`)
		tags.WriteString(htmlAttr(faviconRel(faviconCandidate{Rel: embed.Rel})))
		tags.WriteString(`" href="`)
		tags.WriteString(htmlAttr(embed.DataURI))
		tags.WriteByte('"')
		if embed.MIME != "" {
			tags.WriteString(` type="`)
			tags.WriteString(htmlAttr(embed.MIME))
			tags.WriteByte('"')
		}
		if embed.Sizes != "" {
			tags.WriteString(` sizes="`)
			tags.WriteString(htmlAttr(embed.Sizes))
			tags.WriteByte('"')
		}
		tags.WriteString(">\n")
	}

	idx := htmlInsertAfterTag(html, "head")
	if idx == -1 {
		idx = htmlInsertAfterTag(html, "html")
	}
	if idx == -1 {
		out := make([]byte, 0, tags.Len()+len(html))
		out = append(out, tags.Bytes()...)
		out = append(out, html...)
		return out
	}
	out := make([]byte, 0, len(html)+tags.Len())
	out = append(out, html[:idx]...)
	out = append(out, tags.Bytes()...)
	out = append(out, html[idx:]...)
	return out
}

func htmlInsertAfterTag(html []byte, name string) int {
	lower := bytes.ToLower(html)
	needle := []byte("<" + name)
	for offset := 0; offset < len(lower); {
		relStart := bytes.Index(lower[offset:], needle)
		if relStart == -1 {
			return -1
		}
		start := offset + relStart
		end := start + len(needle)
		if end < len(lower) && isTagNameChar(lower[end]) {
			offset = end
			continue
		}
		relEnd := bytes.IndexByte(html[start:], '>')
		if relEnd == -1 {
			return -1
		}
		return start + relEnd + 1
	}
	return -1
}

func isTagNameChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == ':'
}

func htmlAttr(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}
