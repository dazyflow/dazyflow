// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// onePixelPNG is the smallest real PNG: enough that a test asserts on bytes
// that actually decode rather than on a placeholder string.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
	0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func pngDataURI() string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG)
}

// TestResolveToolIcons_InlinesAFetchedIcon is the whole point of the feature:
// what reaches a manifest is bytes from our own origin, never the third
// party's URL — the app's CSP would refuse to load that.
func TestResolveToolIcons_InlinesAFetchedIcon(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	}))
	defer srv.Close()

	tools := []Tool{
		{Name: "a", Icons: []Icon{{Src: srv.URL + "/icon.png"}}},
		// Same source: one fetch serves both.
		{Name: "b", Icons: []Icon{{Src: srv.URL + "/icon.png"}}},
		{Name: "c"},
	}
	got := resolveWithClient(t, srv.Client(), tools)

	want := pngDataURI()
	if got["a"] != want {
		t.Errorf("tool a icon = %.40q…, want the inlined PNG", got["a"])
	}
	if got["b"] != want {
		t.Errorf("tool b did not share the icon: %.40q", got["b"])
	}
	if _, ok := got["c"]; ok {
		t.Error("a tool with no icons got one")
	}
	if hits != 1 {
		t.Errorf("fetched %d times, want the source deduplicated to 1", hits)
	}
}

// TestResolveToolIcons_RefusesWhatIsNotAnImage covers the response we are
// handed being something other than what an <img> should ever receive.
func TestResolveToolIcons_RefusesWhatIsNotAnImage(t *testing.T) {
	cases := map[string]struct {
		contentType string
		body        []byte
	}{
		"html":       {"text/html", []byte("<script>alert(1)</script>")},
		"json":       {"application/json", []byte(`{"nope":true}`)},
		"no type":    {"", onePixelPNG},
		"oversized":  {"image/png", make([]byte, maxIconBytes+1)},
		"empty body": {"image/png", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.contentType != "" {
					w.Header().Set("Content-Type", c.contentType)
				} else {
					// Force an empty Content-Type rather than letting net/http
					// sniff one from the body.
					w.Header()["Content-Type"] = nil
				}
				_, _ = w.Write(c.body)
			}))
			defer srv.Close()

			got := resolveWithClient(t, srv.Client(),
				[]Tool{{Name: "a", Icons: []Icon{{Src: srv.URL + "/x"}}}})
			if _, ok := got["a"]; ok {
				t.Errorf("accepted %s as an icon: %.60q", name, got["a"])
			}
		})
	}
}

// TestResolveIcon_SchemesRefused: only https and data: are ever dialed or
// inlined. http would be mixed content, and the rest are not fetchable.
func TestResolveIcon_SchemesRefused(t *testing.T) {
	for _, src := range []string{
		"http://example.test/icon.png",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"//example.test/icon.png",
		"",
	} {
		if _, err := resolveIcon(context.Background(), http.DefaultClient, src); err == nil {
			t.Errorf("resolveIcon(%q) succeeded, want a refusal", src)
		}
	}
}

// TestNormalizeDataIcon covers the inline case, where the bytes are already
// here and the only question is whether they are what they claim.
func TestNormalizeDataIcon(t *testing.T) {
	ok, err := normalizeDataIcon(pngDataURI())
	if err != nil {
		t.Fatalf("a valid PNG data URI was refused: %v", err)
	}
	if ok != pngDataURI() {
		t.Errorf("normalised to %.40q, want it re-emitted unchanged", ok)
	}

	bad := []string{
		// Not base64 — a percent-encoded payload cannot survive.
		"data:image/png,%3Cscript%3Ealert(1)%3C/script%3E",
		// A type we do not render.
		"data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte("<b>hi</b>")),
		// Claims PNG, is not valid base64.
		"data:image/png;base64,!!!!",
		// No payload at all.
		"data:image/png;base64",
		"data:image/png;base64,",
		// Over the cap.
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, maxIconBytes+1)),
	}
	for _, src := range bad {
		if got, err := normalizeDataIcon(src); err == nil {
			t.Errorf("normalizeDataIcon(%.40q) = %.40q, want a refusal", src, got)
		}
	}
}

// TestPickIcon: a manifest carries one logo and the app renders light AND
// dark, so a themeless icon wins.
func TestPickIcon(t *testing.T) {
	got, ok := pickIcon([]Icon{
		{Src: "a", Theme: "dark"},
		{Src: "b"},
		{Src: "c", Theme: "light"},
	})
	if !ok || got.Src != "b" {
		t.Errorf("picked %+v, want the themeless icon", got)
	}

	// All themed: take one rather than none.
	got, ok = pickIcon([]Icon{{Src: "a", Theme: "dark"}, {Src: "c", Theme: "light"}})
	if !ok || got.Src != "a" {
		t.Errorf("picked %+v, want the first themed icon", got)
	}

	// A declared type we cannot render is skipped without spending a request.
	got, ok = pickIcon([]Icon{{Src: "a", MimeType: "application/pdf"}, {Src: "b", MimeType: "image/png"}})
	if !ok || got.Src != "b" {
		t.Errorf("picked %+v, want the renderable one", got)
	}
	if _, ok := pickIcon([]Icon{{Src: "  "}}); ok {
		t.Error("an empty src was accepted")
	}
	if _, ok := pickIcon(nil); ok {
		t.Error("no icons yielded one")
	}
}

// TestResolveToolIcons_BoundsDistinctSources stops a server with hundreds of
// separately-iconed tools from turning one handshake into hundreds of requests.
func TestResolveToolIcons_BoundsDistinctSources(t *testing.T) {
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	}))
	defer srv.Close()

	var tools []Tool
	for i := 0; i < maxIconsPerServer*3; i++ {
		tools = append(tools, Tool{
			Name:  string(rune('a' + i)),
			Icons: []Icon{{Src: srv.URL + "/" + strings.Repeat("x", i+1) + ".png"}},
		})
	}
	got := resolveWithClient(t, srv.Client(), tools)
	if hits > maxIconsPerServer {
		t.Errorf("fetched %d sources, want at most %d", hits, maxIconsPerServer)
	}
	if len(got) > maxIconsPerServer {
		t.Errorf("resolved %d icons, want at most %d", len(got), maxIconsPerServer)
	}
}

// resolveWithClient drives the resolver with a client that can reach the
// test's TLS server.
func resolveWithClient(t *testing.T, client *http.Client, tools []Tool) map[string]string {
	t.Helper()
	return resolveToolIcons(context.Background(), client, tools)
}
