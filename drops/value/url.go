// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// url.go is a typed source field: the Text drop's inline-or-wire ergonomics,
// but constrained to a web address. Unlike Text (a pure ValueSource with no
// input), it declares a `url` input port so the address can be computed
// upstream — so it renders as a normal node, not the value-source card. It
// VALIDATES at run time and fails the node on a bad address (bad_param),
// rather than emitting a `valid` boolean: a malformed URL is a mistake to
// surface at the field, not a value to thread onward (same reasoning as a
// missing required param blocking a Run). Beyond echoing the URL it decodes
// the query string into a map for downstream branching/templating.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "url",
			Version:     "1.0",
			Label:       "URL",
			Color:       "#3b82f6",
			Icon:        "globe",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"url", "link", "uri", "address", "query", "validate"},
			Description: "Hold a web address — type it inline or connect a string into the 'url' input — and emit it on 'out' only after checking it's a real http(s) URL. A malformed address (no scheme, no host, or a non-http scheme) fails the step up front instead of breaking a later step. It also decodes the address so you can act on its parts without string surgery: 'host' (example.com), 'path' (/blog/post), and 'query' as a map (?a=1&b=2 → {a:\"1\",b:\"2\"}, values URL-decoded, first value wins on a repeated key) — so you can branch on the path, template with a param, or rebuild a URL directly.",
			Summary:     "Validate a URL (http/https) and emit it plus its host, path, and query params.",
			Examples: []core.ParamsExample{
				{
					Title:  "A link with a path and query params",
					Params: json.RawMessage(`{"url":"https://example.com/search?q=hello&page=2"}`),
					Notes:  "'out' is the URL; 'host' is \"example.com\"; 'path' is \"/search\"; 'query' is {\"q\":\"hello\",\"page\":\"2\"}.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Not marked Required: the address may instead be typed into the
				// `url` param. The schema's required:["url"] + the editor's
				// config check (a wired input satisfies it) enforce "type it OR
				// wire it" — mirrors rss / gmail_send_email.
				{Port: "url", Label: "URL", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "URL", MIME: []string{"text/plain"}},
				{Port: "host", Label: "Host", MIME: []string{"text/plain"}},
				{Port: "path", Label: "Path", MIME: []string{"text/plain"}},
				{Port: "query", Label: "Query params", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"url":{"type":"string","format":"uri","title":"URL","description":"A web address starting with http:// or https://. Type it here, or connect a string into the 'url' input."}
				},
				"required":["url"]
			}`),
			Idempotent: true,
		},
		Execute: executeURL,
	})
}

func executeURL(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Wired 'url' input wins over the inline param (params.TextInputOr), so the
	// address can be computed upstream or set on the node.
	raw, ok := params.TextInputOr(job, "url", params.StringDefault(job.Params, "url", ""))
	if !ok {
		return params.Err(job, "bad_input", "the connected 'url' input must be text"), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return params.Err(job, "bad_param", "url is required: connect the 'url' input or set the url param"), nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return params.Err(job, "bad_param", "not a valid URL: "+err.Error()), nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return params.Err(job, "bad_param", "URL must start with http:// or https://"), nil
	}
	if u.Host == "" {
		return params.Err(job, "bad_param", "URL is missing a host (e.g. https://example.com/path)"), nil
	}

	// url.Query() percent-decodes keys/values and drops malformed pairs
	// silently (never panics). Flatten to a single-valued map — first value
	// wins on a repeated key, matching url.Values.Get.
	q := make(map[string]any)
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			q[k] = vs[0]
		} else {
			q[k] = ""
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: raw},
			// Decomposed parts, from the same parse — host (u.Host keeps any
			// :port) and the decoded path, so downstream can branch/template on
			// them without re-parsing. Path is "" for a bare host (no trailing
			// slash), matching net/url.
			"host":  {MIME: "text/plain", Inline: u.Host},
			"path":  {MIME: "text/plain", Inline: u.Path},
			"query": {MIME: "application/json", Inline: q},
		},
	}, nil
}
