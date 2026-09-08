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
// The file exists because of one constraint: the app's CSP is
// `img-src 'self' data: blob:`, so a third party's https URL would not load even
// if passed through. That is the right default — a remote <img> on an admin page
// is a third party learning who looked at what, and a dead icon host is a page
// that hangs on someone else's outage.
//
// So an icon is fetched here through the same guarded client that dials the MCP
// endpoint, and inlined as a data: URI. Everything fails soft: a server whose
// icon host is down still connects, wearing the category glyph.

const (
	maxIconBytes = 32 << 10
	// maxIconsPerServer: most servers use one icon for every tool, which dedupes to
	// a single fetch; this is the ceiling for one that does not.
	maxIconsPerServer = 8
	iconFetchBudget   = 3 * time.Second
)

// iconMimeTypes includes SVG because the only place a logo renders is an <img>
// tag, where script inside an SVG does not execute and external references do not
// load. It is never inlined into the DOM, which would be an XSS surface.
var iconMimeTypes = map[string]bool{
	"image/png":     true,
	"image/jpeg":    true,
	"image/webp":    true,
	"image/gif":     true,
	"image/svg+xml": true,
}

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

	// Concurrent, because the budget is for the phase rather than per source: two
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

// pickIcon prefers an icon declaring no theme, a manifest carrying a single logo
// while the app renders light AND dark. Beyond that the first usable entry wins:
// choosing on `sizes` would be precision the render does not use.
func pickIcon(icons []Icon) (Icon, bool) {
	var themed Icon
	var haveThemed bool
	for _, icon := range icons {
		if strings.TrimSpace(icon.Src) == "" {
			continue
		}
		// A declared type we cannot render is worth skipping before spending a request.
		// An absent one is fine — the response decides.
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

// resolveIcon accepts only two schemes. A data: URI is checked and normalised.
// An https URL is fetched through the guarded client, which applies the same
// post-DNS SSRF control as an MCP call and refuses redirects. Cleartext http is
// refused outright: it would be mixed content even if inlined.
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

// normalizeDataIcon re-encodes rather than passing the string through, which is
// the point: what comes back is built from bytes WE decoded, so a src carrying
// anything other than the image it claims cannot survive the round trip.
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
