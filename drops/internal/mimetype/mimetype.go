// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mimetype

import (
	"path/filepath"
	"strings"
)

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
