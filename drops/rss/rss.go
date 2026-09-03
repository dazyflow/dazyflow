// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rss provides the RSS/Atom feed reader — a fetch-and-dedupe node that
// pairs with an Interval trigger to act on each NEW item published to a feed
// (blog posts, releases, podcast episodes, news). It follows the same
// cursor-watermark shape as gmail_search_messages: a per-(flow,node) memory of
// what it has already emitted, so a polling flow processes each item once.
package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
)

const (
	feedTimeout  = 20 * time.Second
	maxFeedBytes = 5 << 20 // 5 MiB — generous for a feed, bounds a hostile body
	maxSeenIDs   = 500     // rolling dedupe window, bounds cursor growth
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "rss",
			Version:     "1.0",
			Label:       "RSS / Atom feed",
			Subtitle:    "New items from a feed",
			Icon:        "rss",
			BrandLogo:   "/brands/rss.svg",
			Category:    "network",
			Provider:    "internal",
			Tags:        []string{"rss", "atom", "feed", "trigger", "poll", "news", "syndication", "subscribe"},
			Description: "Read an RSS 2.0 or Atom feed and emit its items as rows. Pair it with an Interval trigger to poll on a schedule: with dedupe on (the default) it remembers which items it has already emitted and fires only for NEW ones — so a blog, release feed, podcast, or news source drives the flow once per item. The first poll establishes a baseline and emits nothing (it starts watching from now, not the whole backlog). Both dialects normalize to the same columns: id, title, link, published (RFC3339 when the feed's date parses), author, summary, content. Turn dedupe off to just parse the current feed into rows every run.",
			Summary:     "Poll an RSS/Atom feed and emit each new item as a row (cursor-deduped).",
			Examples: []core.ParamsExample{
				{
					Title:  "Fire on each new post",
					Params: json.RawMessage(`{"url":"https://example.com/blog/feed.xml"}`),
					Notes:  "Connect an Interval trigger into this step. Dedupe is on, so only new items flow.",
				},
				{
					Title:  "Just parse the current feed (no dedupe)",
					Params: json.RawMessage(`{"url":"https://example.com/feed.xml","dedupe":false}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "url", Label: "Feed URL", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "items", Label: "Items", MIME: []string{"application/json"}, Example: json.RawMessage(`[{"id":"https://dazyflow.app/blog/0-31-2","title":"Dazyflow 0.31.2","link":"https://dazyflow.app/blog/0-31-2","published":"2026-02-12T08:00:00Z","author":"Angels Ware","summary":"Bug fixes and better Swedish coverage.","content":"<p>Bug fixes and better Swedish coverage.</p>"}]`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","title":"Feed URL","description":"The RSS or Atom feed URL. Can also be connected into the 'url' input."},
					"dedupe":{"type":"boolean","default":true,"title":"Only new items","description":"On: remember what's been emitted and fire only for new items (the first poll baselines silently). Off: emit the whole current feed every run."}
				},
				"required":["url"]
			}`),
			// Stateful: a run advances the dedupe cursor, so it isn't a pure
			// function of its inputs (matches gmail_search_messages).
			Idempotent: false,
			// The dedupe cursor is exactly the kind of hidden per-node memory
			// the editor's "Reset state" affordance exists for: clearing it
			// makes the next run baseline afresh.
			NodeState: &core.NodeState{
				Label:     "Remembered items",
				ResetHint: "Forget which items it has already emitted. The next run baselines again (recording current items, emitting none), then fires only for items published after that.",
			},
		},
		Execute: executeRSS,
	})
	// Tell the daemon which reserved key holds this node's dedupe cursor, so a
	// user "Reset state" can clear it. Same key format as dedupeAndEmit — via
	// the shared cursorName helper so the two can't drift.
	engine.RegisterStateReset("rss", func(flow, node string) []string {
		return []string{cursorName(flow, node)}
	})
}

// cursorName is the reserved secret key holding the per-(flow,node) dedupe
// window. Single source of truth for both the runtime read/write and the
// state-reset key-builder.
func cursorName(flow, node string) string {
	return fmt.Sprintf("cursor.rss.%s.%s", flow, node)
}

func executeRSS(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	url := resolveURL(job)
	if url == "" {
		return params.Err(job, "bad_param", "url is required: connect the URL input or set the url param"), nil
	}
	// Operator egress allowlist, then the guarded (SSRF-blocking) client —
	// the same defense-in-depth http_request uses.
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return params.Err(job, "bad_param", "invalid URL: "+err.Error()), nil
	}
	req.Header.Set("User-Agent", "dazyflow-rss/1.0")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")

	resp, err := hfnet.SafeHTTPClient(feedTimeout, false).Do(req)
	if err != nil {
		if hfnet.IsSSRFError(err) {
			return params.Err(job, "ssrf_blocked", "the feed URL resolves to a private/internal address"), nil
		}
		return params.Err(job, "network", "couldn't fetch the feed: "+err.Error()), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return params.Err(job, "remote_error", fmt.Sprintf("feed responded with HTTP %d", resp.StatusCode)), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes))
	if err != nil {
		return params.Err(job, "network", "couldn't read the feed: "+err.Error()), nil
	}
	items, err := parseFeed(body)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	if !params.BoolDefault(job.Params, "dedupe", true) {
		// Dedupe off: the whole feed every run. Say what we got, so an empty
		// feed reads as "fetched fine, 0 items" rather than "nothing happened".
		params.EmitProgress(progress, job, 1, fmt.Sprintf("dedupe off: emitting all %d feed item(s)", len(items)))
		return emitRows(job, allRows(items)), nil
	}
	return dedupeAndEmit(ctx, job, items, progress), nil
}

// resolveURL prefers a wired 'url' input over the param, so the feed can be
// computed upstream (e.g. a Text drop) or set inline on the node.
func resolveURL(job core.Job) string {
	if ref, ok := job.Input["url"]; ok {
		if s, ok := ref.Inline.(string); ok && s != "" {
			return s
		}
	}
	return params.StringDefault(job.Params, "url", "")
}

// dedupeAndEmit applies the per-(flow,node) cursor: emit only items not seen
// before, then fold the current feed's ids into the stored window. The first
// run baselines silently (emits nothing) so the flow watches from "now"
// forward — mirrors gmail_search_messages / google_form_trigger.
func dedupeAndEmit(ctx context.Context, job core.Job, items []feedItem, progress chan<- core.Progress) core.Result {
	name := cursorName(job.GraphID, job.NodeID)
	raw := cursor.Read(ctx, job.Tenant, name)
	first := raw == ""
	prev := decodeIDs(raw)
	seen := make(map[string]bool, len(prev))
	for _, id := range prev {
		seen[id] = true
	}

	current := make([]string, 0, len(items))
	fresh := make([]map[string]any, 0, len(items))
	for _, it := range items {
		current = append(current, it.ID)
		if first {
			continue // baseline: record ids below, emit nothing
		}
		if !seen[it.ID] {
			fresh = append(fresh, it.row())
		}
	}

	// New window: current feed ids (newest) ahead of the prior window, deduped
	// and capped. Best-effort write — a failed write re-emits next run at worst.
	newWindow := capIDs(dedupeIDs(append(append([]string{}, current...), prev...)), maxSeenIDs)
	_ = cursor.Write(ctx, job.Tenant, name, encodeIDs(newWindow))

	// Explain the outcome in the run log, so an empty Items output reads as a
	// deliberate non-event (baseline / nothing new) rather than a silent
	// failure — the case that's indistinguishable from "broken" on the canvas.
	switch {
	case first:
		params.EmitProgress(progress, job, 1, fmt.Sprintf("baseline: recorded %d feed item(s), emitting none — now watching for new items", len(items)))
	case len(fresh) == 0:
		params.EmitProgress(progress, job, 1, fmt.Sprintf("no new items (%d in feed, all already seen)", len(items)))
	default:
		params.EmitProgress(progress, job, 1, fmt.Sprintf("%d new item(s) (%d in feed)", len(fresh), len(items)))
	}

	if len(fresh) == 0 {
		return emitNone(job) // first run, or nothing new → downstream stays dormant
	}
	return emitRows(job, fresh)
}

func allRows(items []feedItem) []map[string]any {
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		rows = append(rows, it.row())
	}
	return rows
}

func emitRows(job core.Job, rows []map[string]any) core.Result {
	if len(rows) == 0 {
		return emitNone(job)
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"items": {MIME: "application/json", Inline: rows, Headers: itemHeaders},
		},
	}
}

// emitNone is an OK result with no output ports: downstream edges go dormant
// and the rest of the flow is skipped — an empty poll is a non-event.
func emitNone(job core.Job) core.Result {
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
}

// --- cursor id-window (de)serialization ---

func decodeIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

func encodeIDs(ids []string) string {
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func dedupeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func capIDs(ids []string, n int) []string {
	if len(ids) > n {
		return ids[:n]
	}
	return ids
}
