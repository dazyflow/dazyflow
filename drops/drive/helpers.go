// Package drive hosts the native Google Drive connectors (drive_list_files,
// drive_download, drive_upload). They authenticate with Google OAuth (the
// "google" provider) via the SetTokenLookup hook the daemon wires at startup —
// the same provider and token plumbing the gmail, sheets and gcal packages use,
// so connecting a Google account for Drive tops up the existing grant
// incrementally.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
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

type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup wires the daemon's per-account Google token resolver. Mirrors
// sheets.SetTokenLookup / gcal.SetTokenLookup — bound to the "google" provider.
func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	// `token` is not a user-facing param, but the engine honors it when present:
	// it's the injection seam the integration tests use to stand in for a
	// connected account. The UI path always goes through the account lookup.
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Google token: connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

// Test seams: list/download hit the API root; upload hits the upload root.
var (
	baseMu     sync.RWMutex
	apiBase    = driveAPIBase
	uploadBase = driveUploadBase
)

// SetHTTPBases swaps both Drive roots (tests point them at one httptest server).
func SetHTTPBases(api, upload string) {
	baseMu.Lock()
	defer baseMu.Unlock()
	apiBase, uploadBase = api, upload
}

func apiBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return apiBase
}

func uploadBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "upload_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return uploadBase
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Guard the dial: base_url can still arrive via the API/test params, so the
	// SSRF client blocks loopback/private/link-local targets and the egress
	// allowlist (when set) bounds which public hosts the bearer token may reach.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("google response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
}

// driveErr pulls the human message out of a Google API error envelope, falling
// back to a bounded slice of the raw body.
func driveErr(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512])
	}
	return string(body)
}

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

// quoteDriveValue escapes a value for interpolation into a Drive query string
// (q=), per the API grammar: backslashes and single quotes are backslash-escaped.
func quoteDriveValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return v
}
