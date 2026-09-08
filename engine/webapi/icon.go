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

// Borrowed from the service's own favicon and INLINED as a data: URI: the app's
// CSP is `img-src 'self' data: blob:`, so a third party's URL would not render
// and would tell that party who opened the flow. Everything fails soft.
const (
	// Tight, because the mark lands on EVERY operation's manifest and the catalog
	// response is uncompressed — so this multiplies by maxWebAPIOperations.
	maxLogoBytes = 16 << 10
	// A give-up threshold: the doer refuses an over-cap body rather than truncating.
	maxHTMLBytes         = 512 << 10
	maxHeadScanBytes     = 128 << 10
	maxIconLinks         = 3
	logoRequestTimeoutMS = 1500
	logoBudget           = 3 * time.Second
)

// Both icon spellings are in use. SVG is included because the only place this
// renders is an <img> src, where script does not execute; it is never inlined
// into the DOM.
var logoMIMEs = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/webp":               true,
	"image/gif":                true,
	"image/svg+xml":            true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

// No doer wired means no logo: nothing here is worth an unguarded request.
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
		// An inlined image is never mixed content, so an http fetch would put "which org
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

// Then one label up: an API host is precisely the host least likely to serve a
// favicon. A heuristic, not a public-suffix lookup, so it can land on a shared
// parent — bounded by the egress allowlist either way.
func originsFor(u *url.URL) []string {
	origins := []string{"https://" + u.Host}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) >= 3 {
		origins = append(origins, "https://"+strings.Join(labels[1:], "."))
	}
	return origins
}

// Declared icons first: /favicon.ico is whatever was dropped in the web root
// years ago, frequently a 16x16 that renders as four grey pixels.
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

// mask-icon is a monochrome path meant to be tinted; rendered plain it is a blob.
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

// The BYTES, not the server's word: favicon.ico is the most mis-typed asset on
// the web, and a 404 page served as image/png is just as common. SVG is the
// exception, being the one type Go's sniffer cannot name.
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

// Re-encodes rather than passing through: what comes back is built from bytes WE
// decoded, so a src carrying anything but the image it claims cannot survive.
func NormalizeLogo(src string) (string, error) {
	src = strings.TrimSpace(src)
	if !strings.HasPrefix(strings.ToLower(src), "data:") {
		// A link would not render at all under the app's CSP.
		return "", fmt.Errorf("an icon must be the image itself, not a link to one")
	}
	meta, payload, ok := strings.Cut(src[len("data:"):], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return "", fmt.Errorf("an icon must be a base64 data: URI")
	}
	declared := trimMIME(meta[:len(meta)-len(";base64")])
	if !logoMIMEs[declared] {
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
