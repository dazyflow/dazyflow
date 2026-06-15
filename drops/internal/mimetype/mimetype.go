// Package mimetype centralizes the "is this content text?" decision the
// drops use to choose between emitting a Go string (text) or a []byte
// (binary) on an output port. It previously lived as two slightly
// divergent copies in drops/io and drops/net — the io copy missed the
// charset-parameter trim and a few textual application/* types — which
// meant the same Content-Type could be classified differently depending
// on which drop produced it. One definition keeps that consistent.
package mimetype

import "strings"

// IsText reports whether a MIME type denotes textual content. It strips
// any parameters ("text/plain; charset=utf-8" → "text/plain"), treats
// the whole text/* family as text, and allows the common textual
// application/* types (JSON, XML, CSV, JavaScript, YAML).
func IsText(mime string) bool {
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	mime = strings.TrimSpace(mime)
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml",
		"application/csv", "application/javascript",
		"application/x-yaml", "application/yaml":
		return true
	}
	return false
}
