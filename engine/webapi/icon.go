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

// A described API's logo, borrowed from the service's own favicon: a catalog is
// the one step source arriving with no artwork, and a usually-right guess beats a
// globe that is never informative. The globe is the fallback.
//
//   - The fetch goes through the injected Doer, so it gets the SSRF dial guard,
//     the tenant's egress allowlist, the rate limit and a response cap, and this
//     package still owns no http.Client.
//   - The bytes are INLINED as a data: URI. The app's CSP is
//     `img-src 'self' data: blob:`, so a third party's URL would not render and
//     would tell that party who opened the flow.
//   - Everything fails soft and returns "": a logo is decoration.
const (
	// maxLogoBytes is deliberately tight, because the cap is not really on one
	// image: the mark lands on EVERY operation's manifest, and the catalog response
	// is not compressed on the wire, so this multiplies by maxWebAPIOperations. 16 KiB
	// fits the marks worth having and refuses a multi-size favicon.ico, which is also
	// the source least likely to look good at the ~32px a node card draws.
	maxLogoBytes = 16 << 10
	// maxHTMLBytes is a "give up on this page" threshold rather than a read window
	// — the doer REFUSES an over-cap body instead of truncating — hence generous,
	// while the scan below still only looks at the head.
	maxHTMLBytes = 512 << 10
	// maxHeadScanBytes: <link> belongs in the head and every real page puts it
	// there, so a body-sized scan would only buy regex time over inline scripts.
	maxHeadScanBytes     = 128 << 10
	maxIconLinks         = 3
	logoRequestTimeoutMS = 1500
	logoBudget           = 3 * time.Second
)

// logoMIMEs carries both icon spellings because both are in use: image/x-icon is
// what servers send, image/vnd.microsoft.icon is what IANA registered. SVG is
// included because the only place this renders is an <img> src, where script
// inside an SVG does not execute and external references do not load; it is never
// inlined into the DOM.
var logoMIMEs = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/webp":               true,
	"image/gif":                true,
	"image/svg+xml":            true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

// ResolveLogo never returns an error — see above. No doer wired means no logo,
// the same answer this package gives for a step: nothing here is worth an
// unguarded request.
func ResolveLogo(ctx context.Context, baseURL string) string {
	do, ok := currentDoer()
	if !ok {
		return ""
	}
	return resolveLogo(ctx, do, baseURL)
}

func resolveLogo(ctx context.Context, do Doer, baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		// Cleartext is refused for a stronger reason than a step's base URL is: an
		// inlined image is never mixed content, so an http fetch would put "which org
		// runs what" on the wire in exchange for a decoration.
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

// originsFor asks the base URL's host, then one label up — the whole point,
// since an API host (api.example.com) is precisely the host least likely to serve
// a favicon, and the mark lives on the site above it.
//
// "One label up" is a heuristic, not a public-suffix lookup: adding a PSL for a
// decoration is not a trade worth making. It can land on a shared parent and
// borrow that platform's logo — a wrong-but-plausible icon in a corner case
// against a right one in the common case, still bounded by the egress allowlist.
func originsFor(u *url.URL) []string {
	origins := []string{"https://" + u.Host}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) >= 3 {
		origins = append(origins, "https://"+strings.Join(labels[1:], "."))
	}
	return origins
}

// logoFromOrigin tries DECLARED icons before /favicon.ico, even though the page
// costs a request an API host often 404s, because a declaration is chosen artwork
// at a chosen size while /favicon.ico is whatever was dropped in the web root
// years ago — frequently a 16x16 that renders as four grey pixels.
func logoFromOrigin(ctx context.Context, do Doer, origin string) string {
	for _, href := range declaredIcons(ctx, do, origin) {
		if data := fetchLogo(ctx, do, href); data != "" {
			return data
		}
	}
	return fetchLogo(ctx, do, origin+"/favicon.ico")
}

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
	// Largest first, declaration order breaking ties: a page offering a 180x180 and
	// a 16x16 should not have this pick the 16.
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

// hasIconRel excludes mask-icon on purpose: it is a monochrome path meant to be
// tinted by the browser, so rendered as an ordinary image it is a black
// silhouette — worse than the globe it would replace.
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
	return icon
}

func iconScore(rel []string, sizes, mime, path string) int {
	if strings.EqualFold(trimMIME(mime), "image/svg+xml") || strings.HasSuffix(strings.ToLower(path), ".svg") {
		return 1 << 20
	}
	best := 0
	for _, s := range strings.Fields(strings.ToLower(sizes)) {
		if s == "any" {
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
		for _, r := range rel {
			if strings.HasPrefix(r, "apple-touch-icon") {
				return 120
			}
		}
		return 16
	}
	return best
}

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

// logoMIME asks the BYTES rather than the server's word, and both directions
// matter. favicon.ico is the most mis-typed asset on the web, so a header-only
// check would drop most of the .ico files this exists to find; and a 404 page
// served as image/png is just as common, so a header-only check would inline it
// and put a broken image on every node of the catalog.
//
// SVG is the exception, being the one type Go's sniffer cannot name: taken on the
// server's word, corroborated by the markup opening an <svg> element.
func logoMIME(declared string, body []byte) (string, bool) {
	if m := trimMIME(http.DetectContentType(body)); logoMIMEs[m] {
		return m, true
	}
	if trimMIME(declared) == "image/svg+xml" && looksLikeSVG(body) {
		return "image/svg+xml", true
	}
	return "", false
}

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

// NormalizeLogo re-encodes rather than passing the string through, which is the
// point: what comes back is built from bytes WE decoded, so a src carrying
// anything other than the image it claims cannot survive the round trip.
//
// Exported and returning an error rather than "", because a logo can also be
// chosen by an admin, and a refused choice needs to say what was wrong with it.
// The fetch path wants the silence and gets it from normalizeLogoData.
func NormalizeLogo(src string) (string, error) {
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(strings.ToLower(src), "data:") {
		// The likeliest wrong answer, worth naming precisely: a link would not render at
		// all under the app's CSP, so "paste a URL" is not a smaller version of this
		// feature, it is a broken image.
		return "", fmt.Errorf("an icon must be the image itself, not a link to one")
	}
	meta, payload, ok := strings.Cut(src[len("data:"):], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return "", fmt.Errorf("an icon must be a base64 data: URI")
	}
	declared := trimMIME(meta[:len(meta)-len(";base64")])
	if !logoMIMEs[declared] {
		// An early-out before decoding a type we would refuse anyway; the decoded bytes
		// get the real say below.
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

func normalizeLogoData(src string) string {
	out, err := NormalizeLogo(src)
	if err != nil {
		return ""
	}
	return out
}
