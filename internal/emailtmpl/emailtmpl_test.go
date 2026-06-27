// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailtmpl

import (
	"strings"
	"testing"
)

func TestWrapBodyInjectsBodyAsRawHTML(t *testing.T) {
	shell := `<div class="wrap">{{.Body}}</div>`
	out, err := WrapBody(shell, `<p>hello <b>world</b></p>`, "", "")
	if err != nil {
		t.Fatalf("WrapBody: %v", err)
	}
	if !strings.Contains(out, `<div class="wrap"><p>hello <b>world</b></p></div>`) {
		t.Fatalf("body not injected as raw HTML: %q", out)
	}
}

func TestWrapBodyEscapesSubject(t *testing.T) {
	out, err := WrapBody(`<title>{{.Subject}}</title>{{.Body}}`, "x", `<script>alert(1)</script>`, "")
	if err != nil {
		t.Fatalf("WrapBody: %v", err)
	}
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatalf("subject was not escaped: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped subject, got: %q", out)
	}
}

func TestWrapBodyLogoRendersAsURL(t *testing.T) {
	out, err := WrapBody(`<img src="{{safeURL .Logo}}">{{.Body}}`, "b", "", "https://example.com/logo.png")
	if err != nil {
		t.Fatalf("WrapBody: %v", err)
	}
	if !strings.Contains(out, `src="https://example.com/logo.png"`) {
		t.Fatalf("logo URL not rendered: %q", out)
	}
}

func TestWrapBodyParseError(t *testing.T) {
	if _, err := WrapBody(`{{.Body`, "b", "", ""); err == nil {
		t.Fatal("expected parse error for malformed template")
	}
}

func TestHasBodyPlaceholder(t *testing.T) {
	cases := []struct {
		html string
		want bool
	}{
		{`<div>{{.Body}}</div>`, true},
		{`<div>{{ .Body }}</div>`, true},
		{`<div>{{.Subject}}</div>`, false},
		{`<div>no placeholder</div>`, false},
		{`{{.Body`, false}, // unparseable
	}
	for _, c := range cases {
		if got := HasBodyPlaceholder(c.html); got != c.want {
			t.Errorf("HasBodyPlaceholder(%q) = %v, want %v", c.html, got, c.want)
		}
	}
}

func TestBuiltinsHaveBodyPlaceholderAndAreNamespaced(t *testing.T) {
	all := BuiltinTemplates()
	if len(all) == 0 {
		t.Fatal("expected at least one built-in template")
	}
	for _, b := range all {
		if !IsBuiltinID(b.ID) {
			t.Errorf("built-in %q is not namespaced with %q", b.ID, BuiltinPrefix)
		}
		if !HasBodyPlaceholder(b.HTML) {
			t.Errorf("built-in %q is missing the {{.Body}} placeholder", b.ID)
		}
		// Every shell must render with a sample body.
		if _, err := WrapBody(b.HTML, "<p>x</p>", "s", "https://e/l.png"); err != nil {
			t.Errorf("built-in %q failed to render: %v", b.ID, err)
		}
		got, ok := Builtin(b.ID)
		if !ok || got.ID != b.ID {
			t.Errorf("Builtin(%q) lookup failed", b.ID)
		}
	}
}

func TestNormalizeLogo(t *testing.T) {
	cases := []struct {
		in       string
		wantData bool   // expect a base64 SVG data URL
		want     string // exact expected (when wantData is false); "" skips
	}{
		{"", false, ""},
		{"https://example.com/logo.png", false, "https://example.com/logo.png"},
		{"data:image/png;base64,AAAA", false, "data:image/png;base64,AAAA"},
		{"/assets/logo.svg", false, "/assets/logo.svg"},
		{`<svg xmlns="http://www.w3.org/2000/svg"><circle/></svg>`, true, ""},
		{`  <SVG><rect/></SVG>  `, true, ""},
		{"acme-icon", false, "acme-icon"},
	}
	for _, c := range cases {
		got := NormalizeLogo(c.in)
		if c.wantData {
			if !strings.HasPrefix(got, "data:image/svg+xml;base64,") {
				t.Errorf("NormalizeLogo(%q) = %q, want an svg data URL", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeLogo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// A normalized SVG must actually render as an <img src> (no escaped markup).
	svg := NormalizeLogo(`<svg><rect/></svg>`)
	out, err := WrapBody(`<img src="{{safeURL .Logo}}">{{.Body}}`, "b", "", svg)
	if err != nil {
		t.Fatalf("WrapBody: %v", err)
	}
	// No escaped SVG markup leaks into the output, and the src is a data URL.
	// (html/template entity-encodes '+' as &#43; in the attribute — the client
	// decodes it back, so match on the stable "data:image/svg" prefix.)
	if strings.Contains(out, "&lt;svg") || !strings.Contains(out, `src="data:image/svg`) {
		t.Errorf("normalized SVG logo did not render as an image src: %q", out)
	}
}

func TestIsBuiltinID(t *testing.T) {
	if !IsBuiltinID("builtin:plain") {
		t.Error("builtin:plain should be a built-in id")
	}
	if IsBuiltinID("welcome") {
		t.Error("welcome should not be a built-in id")
	}
	if _, ok := Builtin("nope"); ok {
		t.Error("unknown built-in id should not resolve")
	}
}
