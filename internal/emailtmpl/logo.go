package emailtmpl

import (
	"encoding/base64"
	"strings"
)

// NormalizeLogo turns an org-profile icon into something usable as an
// <img src> in an email shell ({{.Logo}}). The icon may be:
//   - a URL or path (data:, http(s)://, /path) — used as-is;
//   - raw SVG markup (<svg…> or an <?xml…?> preamble) — wrapped into a
//     base64 data: URL so it renders as an image instead of leaking its
//     source text into the email;
//   - anything else (e.g. a logical icon name) — returned unchanged.
//
// Raw SVG is the case that bit us: dropped into src="…" it's a broken image,
// and dropped into {{.Logo}} outside an <img> html/template escapes it, so the
// markup shows as text. Encoding to a data: URL fixes both.
func NormalizeLogo(icon string) string {
	s := strings.TrimSpace(icon)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "data:"),
		strings.HasPrefix(lower, "http://"),
		strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(s, "/"):
		return s
	case strings.HasPrefix(lower, "<svg"), strings.HasPrefix(lower, "<?xml"):
		return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(s))
	default:
		return s
	}
}
