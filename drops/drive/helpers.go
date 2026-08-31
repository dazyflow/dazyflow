// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package drive hosts the native Google Drive connectors (drive_list_files,
// drive_download, drive_upload). They authenticate with Google OAuth (the
// "google" provider) via the SetTokenLookup hook the daemon wires at startup —
// the same provider and token plumbing the gmail, sheets and gcal packages use,
// so connecting a Google account for Drive tops up the existing grant
// incrementally.
package drive

import (
	"context"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/google"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of a response (or a file being downloaded /
// uploaded) we buffer, so a hostile or buggy upstream can't OOM the daemon.
// Downloads and uploads larger than this are rejected rather than streamed —
// Drive's resumable protocol would be needed for unbounded sizes.
const maxResponseBytes = 64 << 20 // 64 MiB

const (
	driveAPIBase    = "https://www.googleapis.com/drive/v3"
	driveUploadBase = "https://www.googleapis.com/upload/drive/v3"
)

// SetTokenLookup wires the shared Google OAuth token resolver (one provider
// serves every Google connector — see drops/internal/google). Retained as a
// package entry point for tests.
func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

// Test seams: list/download hit the API root; upload hits the upload root.
var (
	apiBase    = apibase.New(driveAPIBase)
	uploadBase = apibase.New(driveUploadBase)
)

// SetHTTPBases swaps both Drive roots (tests point them at one httptest server).
func SetHTTPBases(api, upload string) {
	apiBase.Set(api)
	uploadBase.Set(upload)
}

func apiBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return apiBase.Get()
}

func uploadBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "upload_url"); b != "" {
		return b
	}
	return uploadBase.Get()
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

// driveErr pulls the human message out of a Google API error envelope, falling
// back to a bounded slice of the raw body.
func driveErr(body []byte) string { return google.ErrMessage(body, 512) }

// driveFile is the slice of the Drive file resource the drops surface.
type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	ModifiedTime string `json:"modifiedTime"`
	Size         string `json:"size"`
	WebViewLink  string `json:"webViewLink"`
}

func (f driveFile) normalize() map[string]any {
	return map[string]any{
		"id":            f.ID,
		"name":          f.Name,
		"mime_type":     f.MimeType,
		"modified_time": f.ModifiedTime,
		"size":          f.Size,
		"web_view_link": f.WebViewLink,
	}
}

// isGoogleNative reports whether a mimeType is a Google-editor document
// (Docs/Sheets/Slides…), which has no binary content to download via alt=media
// and must be exported to a concrete format instead.
func isGoogleNative(mimeType string) bool {
	return strings.HasPrefix(mimeType, "application/vnd.google-apps")
}

// exportFormat is a concrete target a Google-editor doc can be exported to.
type exportFormat struct {
	mime string
	ext  string
}

// exportFormats maps the user-facing 'format' keyword (the Download drop's
// "Export as" choice) to its Drive export MIME type and file extension.
var exportFormats = map[string]exportFormat{
	"pdf":  {"application/pdf", ".pdf"},
	"docx": {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"pptx": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"csv":  {"text/csv", ".csv"},
	"txt":  {"text/plain", ".txt"},
	"html": {"text/html", ".html"},
}

// nativeExportable maps each Google-editor type to the formats it can export
// to, in preference order — the first is the default when 'format' is blank.
// Types absent from this map (folders, Forms, Sites…) can't be exported at all.
var nativeExportable = map[string][]string{
	"application/vnd.google-apps.document":     {"pdf", "docx", "txt", "html"},
	"application/vnd.google-apps.spreadsheet":  {"pdf", "xlsx", "csv"},
	"application/vnd.google-apps.presentation": {"pdf", "pptx", "txt"},
	"application/vnd.google-apps.drawing":      {"pdf"},
}

// friendlyNative names a Google-editor type for error messages.
func friendlyNative(mime string) string {
	switch mime {
	case "application/vnd.google-apps.folder":
		return "Drive folder"
	case "application/vnd.google-apps.form":
		return "Google Form"
	case "application/vnd.google-apps.site":
		return "Google Site"
	case "application/vnd.google-apps.shortcut":
		return "Drive shortcut"
	default:
		return "Google-editor file (" + mime + ")"
	}
}

// quoteDriveValue escapes a value for interpolation into a Drive query string
// (q=), per the API grammar: backslashes and single quotes are backslash-escaped.
func quoteDriveValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return v
}
