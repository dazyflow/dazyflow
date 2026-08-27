// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// pngBytes is a real 1x1 PNG, so the sniffing path has something to sniff.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
	0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// icoBytes is an ICO header, which is what Go's sniffer recognises. Enough to
// exercise the "served with the wrong Content-Type" path, which is the common
// case for favicon.ico.
var icoBytes = append([]byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, make([]byte, 32)...)

// fakeDoer serves a fixed URL -> response map and records what was asked for,
// which is how these tests assert on the ORDER of the guesses without standing
// up TLS.
type fakeDoer struct {
	pages map[string]fakeResponse
	asked []string
}

type fakeResponse struct {
	status int
	ctype  string
	body   []byte
	err    error
}

func (f *fakeDoer) do(_ context.Context, _, url string, _ map[string]string, _ []byte, _, maxBytes int) (int, []byte, http.Header, error) {
	f.asked = append(f.asked, url)
	resp, ok := f.pages[url]
	if !ok {
		return http.StatusNotFound, []byte("nope"), http.Header{}, nil
	}
	if resp.err != nil {
		return 0, nil, nil, resp.err
	}
	if len(resp.body) > maxBytes {
		// drops/net.Do refuses an over-cap body rather than truncating it, and
		// a test that truncated instead would let a broken cap pass.
		return resp.status, nil, http.Header{}, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	h := http.Header{}
	if resp.ctype != "" {
		h.Set("Content-Type", resp.ctype)
	}
	return resp.status, resp.body, h, nil
}

func html(body string) fakeResponse {
	return fakeResponse{status: 200, ctype: "text/html; charset=utf-8", body: []byte(body)}
}

func wantPNG(t *testing.T, got string) {
	t.Helper()
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	if got != want {
		t.Errorf("logo = %.60q…, want the inlined PNG", got)
	}
}

// The page's declared icon is preferred over /favicon.ico, and the biggest
// declared size wins — the whole reason the page is read at all.
func TestResolveLogo_PrefersTheLargestDeclaredIcon(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://api.example.com/": html(`<html><head>
			<link rel="icon" sizes="16x16" href="/small.png">
			<link rel="apple-touch-icon" sizes="180x180" href="/big.png">
		</head></html>`),
		"https://api.example.com/big.png":   {status: 200, ctype: "image/png", body: pngBytes},
		"https://api.example.com/small.png": {status: 200, ctype: "image/png", body: []byte("not an image")},
	}}
	wantPNG(t, resolveLogo(context.Background(), f.do, "https://api.example.com/v1"))
	for _, url := range f.asked {
		if strings.HasSuffix(url, "/small.png") {
			t.Errorf("fetched the 16x16 icon: %v", f.asked)
		}
	}
}

// An SVG beats every raster regardless of declared size: it is the one that is
// right at whatever size the node card draws.
func TestResolveLogo_PrefersSVG(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://api.example.com/": html(`<head>
			<link rel="apple-touch-icon" sizes="180x180" href="/big.png">
			<link rel="icon" type="image/svg+xml" href="/mark.svg">
		</head>`),
		"https://api.example.com/mark.svg": {status: 200, ctype: "image/svg+xml", body: svg},
		"https://api.example.com/big.png":  {status: 200, ctype: "image/png", body: pngBytes},
	}}
	got := resolveLogo(context.Background(), f.do, "https://api.example.com/v1")
	if !strings.HasPrefix(got, "data:image/svg+xml;base64,") {
		t.Errorf("logo = %.40q…, want the SVG", got)
	}
}

// A mask-icon is a monochrome path meant to be tinted, so rendering it as an
// image gives a black silhouette. It must not be chosen even when it is the
// only icon declared.
func TestResolveLogo_IgnoresMaskIcon(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://api.example.com/": html(`<head>
			<link rel="mask-icon" color="#000" href="/mask.svg">
		</head>`),
		"https://api.example.com/mask.svg": {status: 200, ctype: "image/svg+xml", body: []byte("<svg/>")},
	}}
	if got := resolveLogo(context.Background(), f.do, "https://api.example.com"); got != "" {
		t.Errorf("logo = %.40q…, want none", got)
	}
	for _, url := range f.asked {
		if strings.HasSuffix(url, "mask.svg") {
			t.Errorf("fetched the mask icon: %v", f.asked)
		}
	}
}

// The convention is the fallback when a service serves no HTML, and the
// Content-Type is not trusted over the bytes: favicon.ico is routinely served
// as octet-stream, and refusing those would lose most of them.
func TestResolveLogo_FaviconWithAWrongContentType(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://api.example.com/": {status: 200, ctype: "application/json", body: []byte(`{"error":"not found"}`)},
		"https://api.example.com/favicon.ico": {
			status: 200, ctype: "application/octet-stream", body: icoBytes,
		},
	}}
	got := resolveLogo(context.Background(), f.do, "https://api.example.com/v1")
	if !strings.HasPrefix(got, "data:image/x-icon;base64,") {
		t.Errorf("logo = %.40q…, want the sniffed .ico", got)
	}
}

// The reason originsFor exists: an API host serves no site, and the mark is on
// the company's own page one label up.
func TestResolveLogo_FallsBackToTheParentDomain(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://example.com/":         html(`<head><link rel="icon" href="/icon.png"></head>`),
		"https://example.com/icon.png": {status: 200, ctype: "image/png", body: pngBytes},
	}}
	wantPNG(t, resolveLogo(context.Background(), f.do, "https://api.example.com/v1"))
	if len(f.asked) < 3 || f.asked[0] != "https://api.example.com/" {
		t.Errorf("asked = %v, want the catalog's own host first", f.asked)
	}
}

// A two-label host has no parent worth guessing, and a bare apex must not be
// asked twice.
func TestResolveLogo_ApexHostAsksOneOrigin(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{}}
	resolveLogo(context.Background(), f.do, "https://example.com/v1")
	for _, url := range f.asked {
		if !strings.HasPrefix(url, "https://example.com/") {
			t.Errorf("asked %q, want only the one origin: %v", url, f.asked)
		}
	}
	if len(f.asked) != 2 {
		t.Errorf("asked = %v, want the page and then favicon.ico", f.asked)
	}
}

// Cleartext buys a decoration at the cost of putting "which org runs what" on
// the wire, so it is refused before any request.
func TestResolveLogo_RefusesNonHTTPS(t *testing.T) {
	for _, base := range []string{"http://api.example.com", "ftp://example.com", "", "not a url at all::"} {
		f := &fakeDoer{pages: map[string]fakeResponse{}}
		if got := resolveLogo(context.Background(), f.do, base); got != "" {
			t.Errorf("%q: logo = %.40q…, want none", base, got)
		}
		if len(f.asked) != 0 {
			t.Errorf("%q: made requests %v", base, f.asked)
		}
	}
}

// A body that is not an image is not inlined, whatever it claims to be.
func TestResolveLogo_RefusesNonImages(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://example.com/": html(`<head><link rel="icon" href="/icon.png"></head>`),
		"https://example.com/icon.png": {
			status: 200, ctype: "text/html", body: []byte("<script>alert(1)</script>"),
		},
		"https://example.com/favicon.ico": {status: 200, ctype: "image/png", body: []byte("still not a png")},
	}}
	if got := resolveLogo(context.Background(), f.do, "https://example.com"); got != "" {
		t.Errorf("logo = %.60q…, want none", got)
	}
}

// An oversized icon is refused by the doer's cap, and that must read as "no
// logo" rather than as a saved response with no body.
func TestResolveLogo_RefusesAnOversizedIcon(t *testing.T) {
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://example.com/favicon.ico": {
			status: 200, ctype: "image/png", body: make([]byte, maxLogoBytes+1),
		},
	}}
	if got := resolveLogo(context.Background(), f.do, "https://example.com"); got != "" {
		t.Errorf("logo = %.40q…, want none", got)
	}
}

// A page may inline its own icon. It is accepted, but re-encoded from bytes we
// decoded ourselves — see normalizeLogoData.
func TestResolveLogo_NormalizesAnInlineIcon(t *testing.T) {
	src := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	f := &fakeDoer{pages: map[string]fakeResponse{
		"https://example.com/": html(`<head><link rel="icon" href="` + src + `"></head>`),
	}}
	wantPNG(t, resolveLogo(context.Background(), f.do, "https://example.com"))
}

func TestNormalizeLogoData_Refusals(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(pngBytes)
	for _, src := range []string{
		"data:image/png,notbase64",                       // not declared base64
		"data:text/html;base64," + png,                   // a type we do not inline
		"data:image/png;base64,%3Cscript%3E",             // payload is not base64
		"data:image/png;base64,",                         // empty
		"data:image/png;base64," + png + "extra,garbage", // trailing junk after a second comma
		// Declared an image, and is not one. Inlining this would put a broken
		// image on every node of the catalog.
		"data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("nope")),
	} {
		if got := normalizeLogoData(src); got != "" {
			t.Errorf("%.40q: got %.40q…, want none", src, got)
		}
	}
}

// The link scan reads the head. A class name or a string in the body that
// happens to look like a link tag must not become a fetch.
func TestIconHrefs_ScansOnlyTheHead(t *testing.T) {
	page := []byte(`<html><head><link rel="icon" href="/head.png"></head>
		<body><link rel="icon" href="/body.png"></body></html>`)
	got := iconHrefs("https://example.com/", page)
	if len(got) != 1 || got[0] != "https://example.com/head.png" {
		t.Errorf("hrefs = %v, want only the one in the head", got)
	}
}

// Every operation of a catalog with a logo wears it; one without keeps the
// globe. This is the field the whole file exists to fill.
func TestSynthesizeManifest_CarriesTheLogo(t *testing.T) {
	desc := Descriptor{
		Tenant: "acme", Name: "orders", BaseURL: "https://api.example.com",
		Logo:       "data:image/png;base64,AAAA",
		Operations: []Operation{{ID: "get", Method: "GET", Path: "/x"}},
	}
	if got := synthesizeManifest(desc, desc.Operations[0]).BrandLogo; got != desc.Logo {
		t.Errorf("BrandLogo = %q, want the catalog's logo", got)
	}
	desc.Logo = ""
	man := synthesizeManifest(desc, desc.Operations[0])
	if man.BrandLogo != "" {
		t.Errorf("BrandLogo = %q for a catalog with no logo", man.BrandLogo)
	}
	if man.Icon != "globe" {
		t.Errorf("Icon = %q, want the fallback glyph", man.Icon)
	}
}

// An https URL on a descriptor would render as a broken image under the app's
// CSP, so the boundary that can say so does.
func TestValidate_RefusesANonInlineLogo(t *testing.T) {
	desc := Descriptor{
		Tenant: "acme", Name: "orders", BaseURL: "https://api.example.com",
		Logo:       "https://example.com/icon.png",
		Operations: []Operation{{ID: "get", Method: "GET", Path: "/x"}},
	}
	err := desc.Validate()
	if err == nil || !strings.Contains(err.Error(), "data:") {
		t.Fatalf("Validate() = %v, want a refusal naming data:", err)
	}
}

// No doer wired means no logo, not an unguarded request.
func TestResolveLogo_UnwiredDoer(t *testing.T) {
	SetDoer(nil)
	if got := ResolveLogo(context.Background(), "https://example.com"); got != "" {
		t.Errorf("logo = %.40q…, want none", got)
	}
}
