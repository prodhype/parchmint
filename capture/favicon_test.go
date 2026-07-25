package capture

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsFaviconRel(t *testing.T) {
	for _, rel := range []string{
		"icon",
		"shortcut icon",
		"apple-touch-icon",
		"apple-touch-icon-precomposed",
		"mask-icon",
		"alternate ICON",
	} {
		if !isFaviconRel(rel) {
			t.Errorf("isFaviconRel(%q) = false, want true", rel)
		}
	}
	for _, rel := range []string{"stylesheet", "preload", ""} {
		if isFaviconRel(rel) {
			t.Errorf("isFaviconRel(%q) = true, want false", rel)
		}
	}
}

func TestNormalizeFaviconCandidates(t *testing.T) {
	in := []faviconCandidate{
		{URL: "https://ex.com/favicon.ico", Rel: "icon"},
		{URL: "https://ex.com/favicon.ico", Rel: "icon", Sizes: "32x32"},
		{URL: "https://ex.com/apple.png", Rel: "apple-touch-icon"},
		{URL: "https://ex.com/not-icon.css", Rel: "stylesheet"},
		{URL: "file:///tmp/icon.png", Rel: "icon"},
		{URL: "https://ex.com/favicon.ico", Rel: "icon", Fallback: true},
		{URL: "https://ex.com/fallback.ico", Rel: "icon", Fallback: true},
	}
	got := normalizeFaviconCandidates(in)
	if len(got) != 3 {
		t.Fatalf("normalizeFaviconCandidates len = %d, want 3: %+v", len(got), got)
	}
	if got[0].URL != "https://ex.com/favicon.ico" || got[1].URL != "https://ex.com/apple.png" || got[2].URL != "https://ex.com/fallback.ico" {
		t.Fatalf("normalizeFaviconCandidates order/dedupe wrong: %+v", got)
	}
}

func TestFaviconMIMEAndDataURI(t *testing.T) {
	tests := []struct {
		name string
		c    faviconCandidate
		ct   string
		data []byte
		want string
	}{
		{"ico extension beats octet stream", faviconCandidate{URL: "https://ex.com/favicon.ico"}, "application/octet-stream", []byte("ico"), "image/x-icon"},
		{"svg extension", faviconCandidate{URL: "https://ex.com/icon.svg"}, "", []byte("<svg></svg>"), "image/svg+xml"},
		{"declared png type", faviconCandidate{Type: "image/png; charset=binary"}, "", []byte{0x89, 'P', 'N', 'G'}, "image/png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := faviconMIME(tt.c, tt.ct, tt.data); got != tt.want {
				t.Fatalf("faviconMIME() = %q, want %q", got, tt.want)
			}
		})
	}

	data := []byte("icon")
	want := "data:image/x-icon;base64," + base64.StdEncoding.EncodeToString(data)
	if got := faviconDataURI("image/x-icon", data); got != want {
		t.Fatalf("faviconDataURI() = %q, want %q", got, want)
	}
}

func TestFetchFaviconHTTPSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(strings.Repeat("x", maxFaviconBytes+1)))
	}))
	defer srv.Close()

	_, _, err := fetchFaviconHTTP(context.Background(), srv.Client(), faviconCandidate{URL: srv.URL + "/favicon.png", Rel: "icon"}, "", "", nil)
	if err == nil {
		t.Fatal("fetchFaviconHTTP() expected size-limit error")
	}
}

func TestFetchFaviconHTTPRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer srv.Close()

	_, _, err := fetchFaviconHTTP(context.Background(), srv.Client(), faviconCandidate{URL: srv.URL + "/favicon.ico", Rel: "icon"}, "", "", nil)
	if err == nil {
		t.Fatal("fetchFaviconHTTP() expected HTML rejection")
	}
}

func TestEmbedFaviconsInHTMLWithoutHead(t *testing.T) {
	html := []byte(`<!DOCTYPE html><html lang=en><meta charset=utf-8><link rel="icon" href="/favicon.ico"><title>x</title><body><header>x</header>`)
	out := string(embedFaviconsInHTML(html, []faviconEmbed{{
		Rel:     "icon",
		MIME:    "image/png",
		DataURI: "data:image/png;base64,aWNvbg==",
	}}))

	if strings.Contains(out, `href="/favicon.ico"`) {
		t.Fatalf("embedFaviconsInHTML kept old icon link: %s", out)
	}
	if strings.Count(out, `rel="icon"`) != 1 {
		t.Fatalf("embedFaviconsInHTML icon count wrong: %s", out)
	}
	if !strings.Contains(out, `<html lang=en>`+"\n"+`<link rel="icon" href="data:image/png;base64,aWNvbg==" type="image/png">`) {
		t.Fatalf("embedFaviconsInHTML did not insert after html tag: %s", out)
	}
	if strings.Index(out, `rel="icon"`) > strings.Index(out, `<body`) {
		t.Fatalf("embedFaviconsInHTML inserted icon in body: %s", out)
	}
}
