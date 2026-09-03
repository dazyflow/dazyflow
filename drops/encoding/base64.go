// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package encoding hosts small, pure encode/decode/digest drops — the
// format-and-checksum toolkit a flow reaches for between an API and a
// downstream step: Base64 (encode a payload for a JSON field or a Basic-auth
// header, decode an incoming blob) and Hash (a checksum or a signed HMAC for
// webhook verification). No auth, no network — they transform one value.
package encoding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "base64",
			Version:     "1.0",
			Label:       "Base64",
			Subtitle:    "Encode or decode Base64",
			Icon:        "binary",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"base64", "encode", "decode", "transform", "bytes", "data-uri"},
			Description: "Encode text to Base64 or decode Base64 back to text. Reach for it to stuff a payload into a JSON field, build a Basic-auth header (base64 of \"user:pass\"), or read a Base64 blob an API handed back. 'mode' picks encode (default) or decode; 'variant' switches between standard Base64 and the URL-safe alphabet (- and _ instead of + and /). Connect the value into 'in'; the result comes out on 'out'.",
			Summary:     "Base64-encode text, or decode Base64 back to text (standard or URL-safe).",
			Examples: []core.ParamsExample{
				{
					Title:  "Encode text to Base64",
					Params: json.RawMessage(`{"mode":"encode"}`),
				},
				{
					Title:  "Decode a URL-safe Base64 token",
					Params: json.RawMessage(`{"mode":"decode","variant":"url"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Value", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result", MIME: []string{"text/plain"}, Example: json.RawMessage(`"RmFrdHVyYSA0NDcxIMOkciBiZXRhbGQu"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"mode":{"type":"string","enum":["encode","decode"],"enumNames":["Encode","Decode"],"default":"encode","title":"Mode","description":"encode text → Base64, or decode Base64 → text."},
					"variant":{"type":"string","enum":["standard","url"],"enumNames":["Standard","URL-safe"],"default":"standard","title":"Alphabet","description":"standard Base64, or the URL-safe alphabet (- and _ instead of + and /)."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeBase64,
	})
}

func executeBase64(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text, ok := stringInput(job.Input["in"])
	if !ok {
		return params.Err(job, "bad_input", "input port 'in' must be text"), nil
	}

	enc := base64.StdEncoding
	if params.StringDefault(job.Params, "variant", "standard") == "url" {
		enc = base64.URLEncoding
	}

	switch params.StringDefault(job.Params, "mode", "encode") {
	case "encode":
		return textResult(job, enc.EncodeToString([]byte(text))), nil
	case "decode":
		raw, err := enc.DecodeString(strings.TrimSpace(text))
		if err != nil {
			return params.Err(job, "bad_input", "input is not valid Base64: "+err.Error()), nil
		}
		return textResult(job, string(raw)), nil
	default:
		return params.Err(job, "bad_param", "mode must be \"encode\" or \"decode\""), nil
	}
}

// stringInput reads a text input, accepting a string or raw []byte (a file
// read may hand over bytes). Anything else is rejected so the drop errors
// cleanly instead of stringifying a map/number.
func stringInput(ref core.Ref) (string, bool) {
	switch v := ref.Inline.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	default:
		return "", false
	}
}

// textResult is the shared OK epilogue: one text/plain value on 'out'.
func textResult(job core.Job, s string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: s},
		},
	}
}
