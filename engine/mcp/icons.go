// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Tool icons, turned into something the palette can render.
//
// The whole file exists because of one constraint: the app's CSP is
// `img-src 'self' data: blob:` (daemon/httporigin.go), so a third party's
// https URL would not load in a browser even if we passed it through. That is
// the right default and we keep it — a remote <img> on an admin page is a
// third party learning who looked at what, and a dead icon host is a page that
// hangs on someone else's outage.
//
// So an icon is fetched HERE, through the same guarded client that dials the
// MCP endpoint itself, and inlined as a data: URI on the manifest. The bytes
// then travel with the catalog and render from our own origin.
//
// Everything in this file fails soft. An icon is decoration: a server whose
// icon host is down, slow, or serving something that is not an image still
// connects, and its steps still work wearing the category glyph.

const (
	// maxIconBytes bounds one icon. Comfortably above a 96×96 PNG and far
	// below anything that belongs in a JSON catalog response.
	maxIconBytes = 32 << 10
	// maxIconsPerServer bounds how many DISTINCT sources one handshake will
	// fetch. Most servers use one icon for every tool, which dedupes to a
	// single fetch; this is the ceiling for one that does not.
	maxIconsPerServer = 8
	// iconFetchBudget bounds the whole icon phase of a handshake. An admin
	// pressing "Save and connect" waits for this in the worst case, so it is
	// short: icons are the last thing worth delaying that answer for.
	iconFetchBudget = 3 * time.Second
)

// iconMimeTypes is what we will inline.
//
// SVG is included because the only place a logo is rendered is an <img> tag
// (web/src/pages/Apps.tsx), where script inside an SVG does not execute and
// external references do not load. It is never inlined into the DOM — an
// inline SVG from a third party would be an XSS surface, and this is not one.
var iconMimeTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/webp":    true,
	"image/gif":     true,
	"image/svg+xml": true,
}

// resolveToolIcons turns each tool's icon list into a data: URI, keyed by tool
// name. Tools with no usable icon are absent from the result.
//
// Sources are deduplicated before fetching: a server that gives all forty of
// its tools the same logo costs one request, not forty.
//
// A nil client means "build the guarded one", which is what production wants;
// a test passes its own to reach an httptest server.
func resolveToolIcons(ctx context.Context, client *http.Client, tools []Tool) map[string]string {
	wanted := map[string][]string{} // src -> tool names wanting it
	order := []string{}
	for _, tool := range tools {
		icon, ok := pickIcon(tool.Icons)
		if !ok {
			continue
		}
		if _, seen := wanted[icon.Src]; !seen {
			if len(order) >= maxIconsPerServer {
				continue
			}
			order = append(order, icon.Src)
		}
		wanted[icon.Src] = append(wanted[icon.Src], tool.Name)
	}
	if len(order) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, iconFetchBudget)
	defer cancel()
	if client == nil {
		client = buildHTTPClient(iconFetchBudget)
	}

	// Concurrent, because the budget is for the phase and not per source: two
	// slow hosts should not add up.
	var mu sync.Mutex
	resolved := map[string]string{}
	var wg sync.WaitGroup
	for _, src := range order {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			data, err := resolveIcon(ctx, client, src)
			if err != nil {
				return
			}
			mu.Lock()
			resolved[src] = data
			mu.Unlock()
		}(src)
	}
	wg.Wait()

	out := map[string]string{}
	for src, data := range resolved {
		for _, tool := range wanted[src] {
			out[tool] = data
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pickIcon chooses one icon from what a tool offers.
//
// A manifest carries a single logo while the app renders in light AND dark, so
// an icon that declares no theme beats one that does. Beyond that the first
// usable entry wins: the palette draws one small square, and choosing on
// `sizes` would be precision the render does not use.
func pickIcon(icons []Icon) (Icon, bool) {
	var themed Icon
	var haveThemed bool
	for _, icon := range icons {
		if strings.TrimSpace(icon.Src) == "" {
			continue
		}
		// A declared type we cannot render is a reason to skip before
		// spending a request on it. An absent one is fine — the response
		// decides.
		if icon.MimeType != "" && !iconMimeTypes[strings.ToLower(icon.MimeType)] {
			continue
		}
		if icon.Theme == "" {
			return icon, true
		}
		if !haveThemed {
			themed, haveThemed = icon, true
		}
	}
	return themed, haveThemed
}

// resolveIcon turns one icon source into a data: URI, or fails.
//
// Only two schemes are accepted. A data: URI is already inline and is merely
// checked and normalised. An https URL is fetched through the guarded client,
// which applies the same post-DNS SSRF control as an MCP call and refuses
// redirects — a redirecting icon host yields no icon rather than a dial to
// somewhere unvetted. Cleartext http is refused outright: it would be mixed
// content in the browser even if we did inline it.
func resolveIcon(ctx context.Context, client *http.Client, src string) (string, error) {
	src = strings.TrimSpace(src)
	switch {
	case strings.HasPrefix(strings.ToLower(src), "data:"):
		return normalizeDataIcon(src)
	case strings.HasPrefix(strings.ToLower(src), "https://"):
	default:
		return "", fmt.Errorf("icon source must be an https URL or a data URI")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxIconBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("icon fetch: HTTP %d", resp.StatusCode)
	}
	mime := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !iconMimeTypes[mime] {
		return "", fmt.Errorf("icon content-type %q is not an image we inline", mime)
	}
	// One byte over the cap is read on purpose: it is how a body that is
	// exactly at the limit is told apart from one that was truncated.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxIconBytes {
		return "", fmt.Errorf("icon is larger than %d bytes", maxIconBytes)
	}
	if len(body) == 0 {
		return "", fmt.Errorf("icon is empty")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

// normalizeDataIcon validates an inline icon and re-emits it in the one form
// we are willing to put on a manifest: base64, with a type we render.
//
// Re-encoding rather than passing the string through is the point. What comes
// back is built from bytes WE decoded, so a src carrying anything other than
// the image it claims — a percent-encoded payload, a second data: URI, markup
// after the comma — cannot survive the round trip.
func normalizeDataIcon(src string) (string, error) {
	rest := src[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", fmt.Errorf("data: icon has no payload")
	}
	meta, payload := rest[:comma], rest[comma+1:]
	if !strings.HasSuffix(strings.ToLower(meta), ";base64") {
		return "", fmt.Errorf("data: icon is not base64")
	}
	mime := strings.ToLower(strings.TrimSpace(meta[:len(meta)-len(";base64")]))
	if !iconMimeTypes[mime] {
		return "", fmt.Errorf("data: icon type %q is not an image we inline", mime)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return "", fmt.Errorf("data: icon payload is not valid base64: %w", err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("data: icon is empty")
	}
	if len(raw) > maxIconBytes {
		return "", fmt.Errorf("data: icon is larger than %d bytes", maxIconBytes)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}
