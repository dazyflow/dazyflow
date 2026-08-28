// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A described API's logo, borrowed from the service's own favicon.
//
// A catalog is the one step source that arrives with no artwork. A built-in
// drop names a file in /brands, an MCP tool can declare icons in its manifest,
// and a described API declares nothing — so every operation an org imports
// wears the same grey globe, and a flow calling three of the org's own services
// looks like three copies of one step.
//
// The service does publish a mark, though: the favicon its own site serves.
// Reading that is a GUESS, and a guess is the right trade here — decoration
// that is usually right beats a glyph that is never informative, and the
// fallback when it is missing or wrong is exactly today's globe.
//
// Three constraints shape the file, and all three are borrowed rather than
// invented:
//
//   - The fetch goes through the injected Doer (doer.go), the same guarded
//     caller a step uses. So a favicon request gets the SSRF dial guard, the
//     tenant's egress allowlist, the per-(tenant, host) rate limit and a
//     response cap — and this package still owns no http.Client, which is the
//     rule doer.go exists to keep.
//   - The bytes are INLINED as a data: URI, never stored as a URL. The app's
//     CSP is `img-src 'self' data: blob:` (daemon/httporigin.go), so a third
//     party's https URL would not render even if we kept it, and would tell
//     that third party who opened the flow. Same constraint and same answer as
//     engine/mcp/icons.go.
//   - Everything fails soft and returns "". A logo is decoration: a service
//     with no favicon, a slow host, or a body that is not an image still gets
//     its catalog saved and its steps registered.
const (
	// maxLogoBytes bounds one fetched image, and it is deliberately tight.
	//
	// The cap is not really on one image: the mark lands on the manifest of
	// EVERY operation in the catalog, and the catalog response the editor loads
	// carries each manifest in full and is not compressed on the wire — so this
	// number multiplies by up to maxWebAPIOperations. Sixteen kibibytes
	// comfortably fits the marks worth having (an SVG logo, a 180px PNG) and
	// refuses the one that bloats: a multi-size favicon.ico, which is also the
	// source least likely to look good at the ~32px a node card draws.
	maxLogoBytes = 16 << 10
	// maxHTMLBytes bounds the page we read to find declared icons. The doer
	// REFUSES an over-cap body rather than truncating it (drops/net.Do), so
	// this is a "give up on this page" threshold and not a read window — hence
	// generous, while the scan below still only looks at the head.
	maxHTMLBytes = 512 << 10
	// maxHeadScanBytes bounds the markup the link scan reads. <link> belongs in
	// the head and every real page puts it there; a body-sized scan would only
	// buy regex time over inline scripts and page copy.
	maxHeadScanBytes = 128 << 10
	// maxIconLinks bounds how many declared icons one page may cost us. Pages
	// routinely declare six or more — one per apple-touch size — and the sort
	// below is what puts the useful ones first.
	maxIconLinks = 3
	// logoRequestTimeoutMS bounds ONE request.
	logoRequestTimeoutMS = 1500
	// logoBudget bounds the whole resolution. An admin pressing Save waits for
	// this in the worst case, which is why it is small, and why the phase is
	// skipped altogether unless the base URL is actually new — see
	// daemon/webapis.go.
	logoBudget = 3 * time.Second
)

// logoMIMEs is what we are willing to inline.
//
// Both icon spellings are here because both are in use: image/x-icon is what
// servers send, image/vnd.microsoft.icon is what IANA registered. SVG is
// included for the same reason engine/mcp includes it — the only place this
// renders is an <img> src, where script inside an SVG does not execute and
// external references do not load. It is never inlined into the DOM.
var logoMIMEs = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/webp":               true,
	"image/gif":                true,
	"image/svg+xml":            true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

// ResolveLogo returns a data: URI for the service's favicon, or "" when there
// is nothing usable to be had. It never returns an error — see the note above.
//
// No doer wired means no logo, which is the same answer this package gives for
// a step: nothing here is worth an unguarded request.
func ResolveLogo(ctx context.Context, baseURL string) string {
	do, ok := currentDoer()
	if !ok {
		return ""
	}
	return resolveLogo(ctx, do, baseURL)
}

// resolveLogo is ResolveLogo with the caller passed in, which is how the tests
// reach an httptest server without installing a process-wide doer.
func resolveLogo(ctx context.Context, do Doer, baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		// Cleartext is refused at the same boundary a step's base URL is, and
		// for a stronger reason: an inlined image is never mixed content, so an
		// http fetch would put "which org runs what" on the wire in exchange
		// for a decoration.
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, logoBudget)
	defer cancel()
	for _, origin := range originsFor(u) {
		if data := logoFromOrigin(ctx, do, origin); data != "" {
			return data
		}
	}
	return ""
}

// originsFor lists the origins to ask, nearest first.
//
// The second one is the whole point. A catalog's base URL is an API host
// (api.example.com/v1), and an API host is precisely the host least likely to
// serve a homepage or a favicon; the mark lives on the site one label up.
//
// "One label up" is a heuristic, not a public-suffix lookup — this package has
// no PSL and adding one for a decoration is not a trade worth making. It can
// therefore land on a shared parent (foo.github.io -> github.io) and borrow
// that platform's logo: a wrong-but-plausible icon in a corner case against a
// right one in the common case, still bounded by the tenant's egress allowlist.
func originsFor(u *url.URL) []string {
	origins := []string{"https://" + u.Host}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) >= 3 {
		origins = append(origins, "https://"+strings.Join(labels[1:], "."))
	}
	return origins
}

// logoFromOrigin tries one origin's DECLARED icons, then the convention.
//
// The page goes first even though it costs a request an API host will often
// 404, because what a page declares is chosen artwork at a chosen size, while
// /favicon.ico is whatever was dropped in the web root years ago — frequently a
// 16x16 that renders as four grey pixels on the node card this feeds.
func logoFromOrigin(ctx context.Context, do Doer, origin string) string {
	for _, href := range declaredIcons(ctx, do, origin) {
		if data := fetchLogo(ctx, do, href); data != "" {
			return data
		}
	}
	return fetchLogo(ctx, do, origin+"/favicon.ico")
}

// declaredIcons fetches an origin's root page and returns its icon hrefs.
func declaredIcons(ctx context.Context, do Doer, origin string) []string {
	page := origin + "/"
	status, body, _, err := do(ctx, http.MethodGet, page,
		map[string]string{"Accept": "text/html"}, nil, logoRequestTimeoutMS, maxHTMLBytes)
	if err != nil || status != http.StatusOK || len(body) == 0 {
		return nil
	}
	return iconHrefs(page, body)
}

var (
	linkTagRE   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	relAttrRE   = regexp.MustCompile(`(?is)\brel\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+))`)
	hrefAttrRE  = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+))`)
	sizesAttrRE = regexp.MustCompile(`(?is)\bsizes\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+))`)
	typeAttrRE  = regexp.MustCompile(`(?is)\btype\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>]+))`)
)

// iconHrefs pulls the icon <link> hrefs out of a page, best first.
//
// A regex over the markup rather than a parser: the alternative is growing an
// HTML dependency to read four attributes, and the failure mode of missing an
// icon is the globe we already have.
func iconHrefs(pageURL string, html []byte) []string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	head := html
	if cut := headEnd(head); cut >= 0 {
		head = head[:cut]
	}
	if len(head) > maxHeadScanBytes {
		head = head[:maxHeadScanBytes]
	}

	type candidate struct {
		href  string
		score int
		order int
	}
	var found []candidate
	seen := map[string]bool{}
	for _, tag := range linkTagRE.FindAll(head, -1) {
		rel := strings.Fields(strings.ToLower(attr(relAttrRE, tag)))
		if !hasIconRel(rel) {
			continue
		}
		href := strings.TrimSpace(attr(hrefAttrRE, tag))
		if href == "" {
			continue
		}
		abs, err := base.Parse(href)
		if err != nil {
			continue
		}
		src := abs.String()
		if seen[src] {
			continue
		}
		seen[src] = true
		found = append(found, candidate{
			href:  src,
			score: iconScore(rel, attr(sizesAttrRE, tag), attr(typeAttrRE, tag), abs.Path),
			order: len(found),
		})
	}
	// Largest first, declaration order breaking ties: a page that offers a
	// 180x180 and a 16x16 should not have this feature pick the 16.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].order < found[j].order
	})
	out := make([]string, 0, maxIconLinks)
	for _, c := range found {
		if len(out) >= maxIconLinks {
			break
		}
		out = append(out, c.href)
	}
	return out
}

// headEnd is the offset of </head>, or -1. Case-insensitive, over at most the
// window the link scan reads anyway.
func headEnd(html []byte) int {
	limit := len(html)
	if limit > maxHeadScanBytes {
		limit = maxHeadScanBytes
	}
	return bytes.Index(bytes.ToLower(html[:limit]), []byte("</head"))
}

func attr(re *regexp.Regexp, tag []byte) string {
	m := re.FindSubmatch(tag)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

// hasIconRel reports whether this <link> is an icon we want.
//
// mask-icon is excluded on purpose: it is a monochrome path meant to be tinted
// by the browser, so rendered as an ordinary image it is a black silhouette —
// worse than the globe it would replace.
func hasIconRel(rel []string) bool {
	icon := false
	for _, r := range rel {
		switch r {
		case "mask-icon":
			return false
		case "icon", "apple-touch-icon", "apple-touch-icon-precomposed":
			icon = true
		}
	}
	// rel="shortcut icon" needs no case of its own: it carries "icon" too,
	// which is why the check is over the whole token list.
	return icon
}

// iconScore ranks a declared icon by how well it will render at the ~32px this
// feeds. Bigger is better, and an SVG beats every raster because it is the one
// that is right at any size.
func iconScore(rel []string, sizes, mime, path string) int {
	if strings.EqualFold(trimMIME(mime), "image/svg+xml") || strings.HasSuffix(strings.ToLower(path), ".svg") {
		return 1 << 20
	}
	best := 0
	for _, s := range strings.Fields(strings.ToLower(sizes)) {
		if s == "any" {
			// Declared by SVG favicons, and by nothing else that means it.
			return 1 << 20
		}
		w, _, ok := strings.Cut(s, "x")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(w); err == nil && n > best {
			best = n
		}
	}
	if best == 0 {
		// No declared size: fall back to what the rel implies. An
		// apple-touch-icon is 120px or more by convention; a bare "icon" is
		// usually the 16x16 favicon.
		for _, r := range rel {
			if strings.HasPrefix(r, "apple-touch-icon") {
				return 120
			}
		}
		return 16
	}
	return best
}

// fetchLogo turns one candidate source into a data: URI, or "".
func fetchLogo(ctx context.Context, do Doer, src string) string {
	lower := strings.ToLower(src)
	switch {
	case strings.HasPrefix(lower, "data:"):
		return normalizeLogoData(src)
	case strings.HasPrefix(lower, "https://"):
	default:
		return ""
	}
	status, body, header, err := do(ctx, http.MethodGet, src,
		map[string]string{"Accept": "image/*"}, nil, logoRequestTimeoutMS, maxLogoBytes)
	if err != nil || status != http.StatusOK || len(body) == 0 {
		return ""
	}
	mime, ok := logoMIME(header.Get("Content-Type"), body)
	if !ok {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body)
}

// logoMIME decides what we are about to inline, and asks the BYTES rather than
// the server's word.
//
// Both directions of that matter. favicon.ico is the most mis-typed asset on
// the web — application/octet-stream, text/plain and
// text/html-with-an-icon-body are all common — so a header-only check would
// drop most of the .ico files this feature exists to find. And a 404 page
// served as image/png is just as common, so a header-only check would inline it
// and put a broken image on every node of the catalog, which is strictly worse
// than the globe it replaced.
//
// SVG is the exception, because it is the one type Go's sniffer cannot name: it
// sees XML, or text. So it is taken on the server's word, corroborated by the
// markup actually opening an <svg> element.
func logoMIME(declared string, body []byte) (string, bool) {
	if m := trimMIME(http.DetectContentType(body)); logoMIMEs[m] {
		return m, true
	}
	if trimMIME(declared) == "image/svg+xml" && looksLikeSVG(body) {
		return "image/svg+xml", true
	}
	return "", false
}

// looksLikeSVG reports whether the head of a document opens an SVG element.
func looksLikeSVG(body []byte) bool {
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	return strings.Contains(strings.ToLower(string(head)), "<svg")
}

func trimMIME(v string) string {
	head, _, _ := strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(head))
}

// NormalizeLogo validates an inline icon and re-emits it in the one form this
// package will put on a manifest: base64, with a type we render.
//
// Re-encoding rather than passing the string through is the point. What comes
// back is built from bytes WE decoded, so a src carrying anything other than the
// image it claims — a percent-encoded payload, a second data: URI, markup after
// the comma — cannot survive the round trip. engine/mcp/icons.go normalizes for
// the same reason.
//
// Exported, and returning an error rather than "", because a logo does not only
// arrive from a fetch: an admin can choose one (daemon.WebAPIInput.Logo), and a
// choice that is refused needs to say what was wrong with it. The fetch path
// still wants the silence, and gets it from normalizeLogoData below.
func NormalizeLogo(src string) (string, error) {
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(strings.ToLower(src), "data:") {
		// The likeliest wrong answer, and worth naming precisely: a link would
		// not render at all under the app's CSP, so "paste a URL" is not a
		// smaller version of this feature, it is a broken image.
		return "", fmt.Errorf("an icon must be the image itself, not a link to one")
	}
	meta, payload, ok := strings.Cut(src[len("data:"):], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return "", fmt.Errorf("an icon must be a base64 data: URI")
	}
	declared := trimMIME(meta[:len(meta)-len(";base64")])
	if !logoMIMEs[declared] {
		// An early-out before spending a decode on a type we would refuse
		// anyway; the decoded bytes get the real say below.
		return "", fmt.Errorf("%s is not an image type we can show — use PNG, SVG, WebP, GIF or JPEG", declared)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return "", fmt.Errorf("the icon's data is not valid base64")
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("the icon is empty")
	}
	if len(raw) > maxLogoBytes {
		return "", fmt.Errorf("the icon is %d KiB, over the %d KiB limit — the mark is drawn at about 32px, so a small PNG or an SVG is enough",
			len(raw)>>10, maxLogoBytes>>10)
	}
	mime, ok := logoMIME(declared, raw)
	if !ok {
		return "", fmt.Errorf("this file says it is %s but its contents are not", declared)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// normalizeLogoData is NormalizeLogo for the fetch path, where a page's inline
// icon that fails any of those checks is simply not the icon we use.
func normalizeLogoData(src string) string {
	out, err := NormalizeLogo(src)
	if err != nil {
		return ""
	}
	return out
}
