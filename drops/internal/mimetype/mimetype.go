// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mimetype centralizes the "is this content text?" decision the
// drops use to choose between emitting a Go string (text) or a []byte
// (binary) on an output port. It previously lived as two slightly
// divergent copies in drops/io and drops/net — the io copy missed the
// charset-parameter trim and a few textual application/* types — which
// meant the same Content-Type could be classified differently depending
// on which drop produced it. One definition keeps that consistent.
package mimetype

import (
	"path/filepath"
	"strings"
)

// GuessByExt maps a filename's extension to a MIME type, covering the file
// types the workspace catalogue actually deals with — spreadsheets, CSVs,
// JSON, common text and images. It deliberately avoids the stdlib mime
// package, whose mapping is OS-dependent (it reads /etc/mime.types on Linux)
// — reproducibility noise we don't need. Unknown extensions fall back to
// application/octet-stream, matching what file_read settles on when no MIME
// is supplied.
func GuessByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".jsonl", ".ndjson":
		return "application/x-ndjson"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".sqlite", ".db":
		return "application/vnd.sqlite3"
	}
	return "application/octet-stream"
}

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
