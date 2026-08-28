// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

const (
	maxCheckBytes  = 1 << 20 // enough to look for a phrase; a health page is small
	checkCursorPfx = "cursor.sitecheck."
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "site_check",
			Version:     "1.0",
			Label:       "Is it up?",
			Subtitle:    "Alert when a site breaks, and when it's back",
			Icon:        "activity",
			Category:    "network",
			Provider:    "internal",
			Tags:        []string{"uptime", "monitor", "health", "check", "down", "alert", "poll"},
			Summary:     "Check a site on a schedule and fire once when it goes down, once when it recovers.",
			Description: "Watch a site and hear about it only when something actually changes. Pair it with an Interval trigger: 'Went down' fires on the check where it stops answering properly, 'Came back' fires when it starts again, and nothing fires while the state is unchanged — so a site that's been down for an hour doesn't page you twelve times. A site that is already down on the very first check does fire, because that's news. Optionally require a phrase on the page, which catches the server that answers 200 with an error page.",
			Examples: []core.ParamsExample{
				{
					Title:  "Alert when the site stops answering",
					Params: json.RawMessage(`{"url":"https://example.com/"}`),
					Notes:  "Connect an Interval trigger in, and Went down / Came back to a notification step.",
				},
				{
					Title:  "Require a phrase, to catch a 200 that's really an error page",
					Params: json.RawMessage(`{"url":"https://example.com/health","expect_text":"ok"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "url", Label: "Address", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "on_down", Label: "Went down", MIME: []string{"text/plain"}},
				{Port: "on_up", Label: "Came back", MIME: []string{"text/plain"}},
				{Port: "up", Label: "Up?", MIME: []string{core.MIMEBool}},
				{Port: "status", Label: "Status code", MIME: []string{"text/plain"}},
				{Port: "detail", Label: "What happened", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","title":"Address","description":"The address to check. Can also be connected into the Address input."},
					"expect_status":{"type":"integer","title":"Expected status","minimum":100,"maximum":599,"description":"Require this exact response code. Leave blank to accept any success (200–299)."},
					"expect_text":{"type":"string","title":"Must contain","description":"Optional. Text the response must contain — catches a server that answers 200 with an error page."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"How long to wait before calling it down, in milliseconds."}
				},
				"required":["url"]
			}`),
			// The check advances the remembered up/down state, so two runs
			// aren't interchangeable.
			Idempotent: false,
			NodeState: &core.NodeState{
				Label:     "Remembered up/down state",
				ResetHint: "Forget whether the site was last seen up or down. The next check treats what it finds as the starting point (and still alerts if it finds it down).",
			},
		},
		Execute: executeSiteCheck,
	})
	engine.RegisterStateReset("site_check", func(flow, node string) []string {
		return []string{checkName(flow, node)}
	})
}

func checkName(flow, node string) string { return checkCursorPfx + flow + "." + node }

func executeSiteCheck(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	target, ok := params.TextInputOr(job, "url", params.StringDefault(job.Params, "url", ""))
	if !ok {
		return params.Err(job, "bad_input", "the 'Address' input must be text"), nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return params.Err(job, "bad_param", "'url' is required"), nil
	}
	if err := EgressAllowedFor(ctx, target); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	timeout := params.IntDefault(job.Params, "timeout_ms", 15000)
	status, body, _, err := Do(ctx, "GET", target, nil, nil, timeout, maxCheckBytes)

	// A failed check is the normal case for this step, not an error result:
	// the whole point is to report it downstream rather than fail the run.
	up, detail := true, ""
	switch {
	case err != nil:
		up, detail = false, "could not be reached: "+err.Error()
	default:
		want := params.IntDefault(job.Params, "expect_status", 0)
		switch {
		case want > 0 && status != want:
			up, detail = false, fmt.Sprintf("answered %d, expected %d", status, want)
		case want == 0 && (status < 200 || status >= 300):
			up, detail = false, fmt.Sprintf("answered %d", status)
		}
		if up {
			if phrase := strings.TrimSpace(params.StringDefault(job.Params, "expect_text", "")); phrase != "" &&
				!strings.Contains(string(body), phrase) {
				up, detail = false, fmt.Sprintf("answered %d but the page didn't contain %q", status, phrase)
			}
		}
	}
	if up {
		detail = fmt.Sprintf("answered %d", status)
	}

	name := checkName(job.GraphID, job.NodeID)
	prevUp, known := readCheckState(ctx, job.Tenant, name)
	writeCheckState(ctx, job.Tenant, name, up)

	// Fire only on a transition. A first check that finds the site DOWN does
	// fire — a site that is down right now is news, however long we've been
	// watching — but a first check that finds it up is just the baseline.
	wentDown := !up && (!known || prevUp)
	cameBack := up && known && !prevUp

	switch {
	case wentDown:
		params.EmitProgress(progress, job, 1, "down: "+detail)
	case cameBack:
		params.EmitProgress(progress, job, 1, "back up: "+detail)
	case up:
		params.EmitProgress(progress, job, 1, "still up ("+detail+")")
	default:
		params.EmitProgress(progress, job, 1, "still down ("+detail+")")
	}

	statusText := ""
	if status > 0 {
		statusText = fmt.Sprint(status)
	}
	out := map[string]core.Ref{
		"up":     {MIME: core.MIMEBool, Inline: up},
		"status": {MIME: "text/plain", Inline: statusText},
		"detail": {MIME: "text/plain", Inline: detail},
	}
	if wentDown {
		out["on_down"] = core.Ref{MIME: "text/plain", Inline: target + " is down — " + detail}
	}
	if cameBack {
		out["on_up"] = core.Ref{MIME: "text/plain", Inline: target + " is back up — " + detail}
	}
	return core.Result{JobID: job.ID, Status: core.StatusOK, Output: out}, nil
}

// readCheckState reads the remembered up/down flag. known=false means this is
// the first check (or the state was reset).
func readCheckState(ctx context.Context, tenant, name string) (up bool, known bool) {
	cacheMu.RLock()
	r := cacheReader
	cacheMu.RUnlock()
	if r == nil {
		return false, false
	}
	raw, err := r(ctx, tenant, name)
	if err != nil {
		return false, false
	}
	switch strings.TrimSpace(raw) {
	case "up":
		return true, true
	case "down":
		return false, true
	}
	return false, false
}

func writeCheckState(ctx context.Context, tenant, name string, up bool) {
	cacheMu.RLock()
	w := cacheWriter
	cacheMu.RUnlock()
	if w == nil {
		return
	}
	v := "down"
	if up {
		v = "up"
	}
	_ = w(ctx, tenant, name, v) // best effort: a failed write re-baselines
}
