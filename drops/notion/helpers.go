// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notion hosts the native Notion connectors (notion_query_database,
// notion_create_page), migrated from the scripted TS drops. Token
// resolution mirrors the other connectors: an explicit `token` param wins
// (covers ${secret.NOTION_TOKEN} templating and a pasted integration
// token), otherwise the daemon's OAuth registry resolves the "notion"
// provider via the SetTokenLookup hook wired at startup.
package notion

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/oauthtok"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 64 << 20 // 64 MiB

const notionVersion = "2022-06-28"

// richTextLimit is Notion's per-rich-text-object content cap.
const richTextLimit = 2000

// tokenHook holds the daemon's per-account Notion OAuth lookup plus the resolve
// sequence shared with the other OAuth connectors (drops/internal/oauthtok).
var tokenHook = oauthtok.New("Notion", "notion", "notion")

func SetTokenLookup(fn oauthtok.Lookup) { tokenHook.Set(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return tokenHook.Resolve(ctx, job)
}

var httpBase = apibase.New("https://api.notion.com/v1")

// SetHTTPBase swaps the Notion API root (tests point it at httptest).
func SetHTTPBase(base string) { httpBase.Set(base) }

func currentHTTPBase() string { return httpBase.Get() }

// notionDo runs one authenticated Notion API call. Returns status + body;
// the caller maps non-2xx via notionError.
func notionDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{
		"Authorization":  "Bearer " + token,
		"Notion-Version": notionVersion,
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the bearer token may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, body, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// notionError pulls the {code,message} out of a Notion error body.
func notionError(status int, body []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		if e.Code != "" {
			return fmt.Sprintf("Notion %s: %s", e.Code, e.Message)
		}
		return e.Message
	}
	trunc := body
	if len(trunc) > 512 {
		trunc = trunc[:512]
	}
	return fmt.Sprintf("Notion returned %d: %s", status, string(trunc))
}
