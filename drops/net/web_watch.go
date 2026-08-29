// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

const (
	maxWatchBytes  = 5 << 20 // 5 MiB of page is plenty to compare
	maxStoredValue = 4 << 10 // how much of the watched text we keep to show as "before"
	watchCursorPfx = "cursor.webwatch."
	// RE2 has no backreferences, so each element gets its own alternative.
	watchStripScript = `(?is)<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>|<head\b[^>]*>.*?</head\s*>|<!--.*?-->`
)

var (
	watchScriptRe  = regexp.MustCompile(watchStripScript)
	watchTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	watchSpaceRe   = regexp.MustCompile(`[ \t\r\f\v]+`)
	watchNewlineRe = regexp.MustCompile(`\n{2,}`)
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "web_watch",
			Version:     "1.0",
			Label:       "Watch a page",
			Subtitle:    "Tell me when it changes",
			Icon:        "eye",
			Category:    "network",
			Provider:    "internal",
			Tags:        []string{"watch", "monitor", "change", "poll", "scrape", "price", "diff", "alert"},
			Summary:     "Fetch a page on a schedule and fire only when what it says has changed.",
			Description: "Keep an eye on a web page and let the flow run only when it actually changes — a price, a status page, a tender list, a job board. Pair it with an Interval trigger. The first check quietly records what the page says today; from then on, every check compares. Steps connected to 'On change' stay dormant while nothing changes, so an alert only goes out when there's something to say. By default it compares the words on the page, not the HTML behind them, which keeps invisible markup churn from crying wolf. To watch one number rather than the whole page, give a 'Watch just this' pattern. One exception worth knowing: the pass-through pin carries its value on every check, changed or not — so wire from 'On change' when you mean \"only when it changed\".",
			Examples: []core.ParamsExample{
				{
					Title:  "Alert when a page changes at all",
					Params: json.RawMessage(`{"url":"https://example.com/tenders"}`),
					Notes:  "Connect an Interval trigger in, and On change → a Slack or ntfy step.",
				},
				{
					Title:  "Watch one price on the page",
					Params: json.RawMessage(`{"url":"https://example.com/product/123","pattern":"Price:\\s*([0-9]+(?:[.,][0-9]{2})?)"}`),
					Notes:  "The first bracketed group is what's compared, so unrelated edits to the page are ignored.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "url", Label: "Page address", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				// Emitted ONLY when something changed, so the engine's
				// skip-cascade keeps the alert dormant on a quiet check —
				// same shape as Branch's unused port.
				{Port: "on_change", Label: "On change", MIME: []string{"text/plain"}},
				{Port: "value", Label: "What it says now", MIME: []string{"text/plain"}},
				{Port: "previous", Label: "What it said before", MIME: []string{"text/plain"}},
				{Port: "changed", Label: "Changed?", MIME: []string{core.MIMEBool}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","title":"Page address","description":"The page to watch. Can also be connected into the Page address input."},
					"pattern":{"type":"string","title":"Watch just this","examples":["Price:\\s*([0-9]+)"],"description":"Optional. A pattern picking out the one part of the page to compare — the first bracketed group, or the whole match if there are no brackets. Leave blank to compare the whole page."},
					"text_only":{"type":"boolean","title":"Compare the words, not the code","default":true,"description":"On: strip the HTML and compare the visible text, so invisible markup churn doesn't count as a change. Off: compare the raw response exactly as it arrives (right for JSON or a plain-text endpoint)."},
					"timeout_ms":{"type":"integer","default":20000,"minimum":1,"description":"Hard deadline for the fetch, in milliseconds."}
				},
				"required":["url"]
			}`),
			// A check advances the remembered value, so two runs of the same
			// step are not interchangeable — same reason rss isn't idempotent.
			Idempotent: false,
			// The remembered value is hidden per-node memory: surface it so a
			// user can clear it and start watching afresh.
			NodeState: &core.NodeState{
				Label:     "What the page last said",
				ResetHint: "Forget what the page said last time. The next check records the page as it is then, and only later changes fire.",
			},
		},
		Execute: executeWebWatch,
	})
	// Let "Reset state" clear the remembered value — same key the runtime
	// reads, via the shared helper so the two can't drift.
	engine.RegisterStateReset("web_watch", func(flow, node string) []string {
		return []string{watchName(flow, node)}
	})
}

func watchName(flow, node string) string {
	return watchCursorPfx + flow + "." + node
}

// watchState is what we remember between checks: a hash of the full watched
// text (cheap, bounded) plus a truncated copy to show the reader what it used
// to say.
type watchState struct {
	Hash    string `json:"hash"`
	Preview string `json:"preview"`
}

func executeWebWatch(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	target, ok := params.TextInputOr(job, "url", params.StringDefault(job.Params, "url", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Page address' input must be text"), nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return params.Err(job, "bad_param", "'url' is required"), nil
	}
	if err := EgressAllowedFor(ctx, target); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	timeout := params.IntDefault(job.Params, "timeout_ms", 20000)
	status, body, _, err := Do(ctx, "GET", target, nil, nil, timeout, maxWatchBytes)
	if err != nil {
		if IsSSRFError(err) {
			return params.Err(job, "ssrf_blocked", err.Error()), nil
		}
		return params.Err(job, "http", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "unexpected_status", fmt.Sprintf("the page answered %d", status)), nil
	}

	text := string(body)
	if params.BoolDefault(job.Params, "text_only", true) {
		text = visibleText(text)
	}
	if pat := strings.TrimSpace(params.StringDefault(job.Params, "pattern", "")); pat != "" {
		re, cerr := regexp.Compile(pat)
		if cerr != nil {
			return params.Err(job, "bad_param", fmt.Sprintf("'Watch just this' isn't a valid pattern: %v", cerr)), nil
		}
		m := re.FindStringSubmatch(text)
		if m == nil {
			return params.Err(job, "not_found", "the page doesn't contain anything matching 'Watch just this' — the page may have changed shape, or the pattern needs adjusting"), nil
		}
		text = m[len(m)-1]
		if len(m) > 1 {
			text = m[1]
		}
	}
	text = strings.TrimSpace(text)

	sum := sha256.Sum256([]byte(text))
	now := watchState{Hash: hex.EncodeToString(sum[:]), Preview: truncate(text, maxStoredValue)}

	name := watchName(job.GraphID, job.NodeID)
	prev, hadPrev := readWatchState(ctx, job.Tenant, name)
	writeWatchState(ctx, job.Tenant, name, now)

	changed := hadPrev && prev.Hash != now.Hash
	switch {
	case !hadPrev:
		params.EmitProgress(progress, job, 1, "first check: recorded what the page says now — watching for changes from here")
	case changed:
		params.EmitProgress(progress, job, 1, "the page changed")
	default:
		params.EmitProgress(progress, job, 1, "no change since the last check")
	}

	out := map[string]core.Ref{
		"value":    {MIME: "text/plain", Inline: now.Preview},
		"previous": {MIME: "text/plain", Inline: prev.Preview},
		"changed":  {MIME: core.MIMEBool, Inline: changed},
	}
	if changed {
		out["on_change"] = core.Ref{MIME: "text/plain", Inline: now.Preview}
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}, nil
}

// visibleText reduces an HTML document to the words a reader would see, so a
// rotating CSRF token or a changed asset hash doesn't read as "the page
// changed". Deliberately crude — this is a comparison key, not a renderer.
func visibleText(s string) string {
	s = watchScriptRe.ReplaceAllString(s, " ")
	s = watchTagRe.ReplaceAllString(s, "\n")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = watchSpaceRe.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return watchNewlineRe.ReplaceAllString(b.String(), "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Don't split a multi-byte character.
	cut := n
	for cut > 0 && !isUTF8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// readWatchState reads the remembered value through the same store the HTTP
// cache uses — the daemon backs both with the encrypted secret store, and the
// key sits under the already-reserved "cursor." namespace.
func readWatchState(ctx context.Context, tenant, name string) (watchState, bool) {
	cacheMu.RLock()
	r := cacheReader
	cacheMu.RUnlock()
	if r == nil {
		return watchState{}, false
	}
	raw, err := r(ctx, tenant, name)
	if err != nil || strings.TrimSpace(raw) == "" {
		return watchState{}, false
	}
	var st watchState
	if json.Unmarshal([]byte(raw), &st) != nil || st.Hash == "" {
		return watchState{}, false
	}
	return st, true
}

func writeWatchState(ctx context.Context, tenant, name string, st watchState) {
	cacheMu.RLock()
	w := cacheWriter
	cacheMu.RUnlock()
	if w == nil {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = w(ctx, tenant, name, string(b)) // best effort: a failed write re-baselines
}
