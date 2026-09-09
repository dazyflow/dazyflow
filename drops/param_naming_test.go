// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestParamNaming_Conventions is the param-name lint: it walks every registered
// drop's params schema and enforces the canonical names that keep the manifest
// discoverable to an LLM. Drift — "limit" vs "max_results", "max_bytes" vs
// "max_output_bytes" — means a model cannot learn one convention and apply it
// everywhere.
//
// Renaming an existing param is a BREAKING change (a saved graph stores params
// by name, so a rename silently drops the value), so drift that already shipped
// is grandfathered in `legacyParamNames`; the lint's job is to stop NEW drift.
// The timeout_ms rule has no grandfathering: every timeout_ms param carries a
// description today, so a new one without fails immediately.
type schemaProps struct {
	Properties map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"properties"`
}

var canonical = map[string]string{
	"max_results":      "limit",
	"max_output_bytes": "max_bytes",
	"max_body_bytes":   "max_bytes",
}

// legacyParamNames grandfathers the non-canonical names that shipped before
// the convention. Renaming them would break saved graphs, so they're frozen
// here (debt, not license): key is "<dropID>.<param>". New entries should be
// rare and justified — prefer the canonical name.
var legacyParamNames = map[string]bool{
	"gmail_search_messages.max_results": true,
	"gmail_get_message.max_results":     true,
	"github_list_issues.max_results":    true,
	"http_request.max_body_bytes":       true,
	"http_download.max_body_bytes":      true,
	"shell.max_output_bytes":            true,
}

func TestParamNaming_Conventions(t *testing.T) {
	var nameViolations, timeoutViolations []string

	for _, d := range allDrops(t) {
		if len(d.manifest.ParamsSchema) == 0 {
			continue
		}
		var s schemaProps
		if err := json.Unmarshal(d.manifest.ParamsSchema, &s); err != nil {
			t.Errorf("%s: params_schema does not parse: %v", d.id, err)
			continue
		}
		for name, prop := range s.Properties {
			if want, bad := canonical[name]; bad && !legacyParamNames[d.id+"."+name] {
				nameViolations = append(nameViolations,
					d.id+"."+name+" → use "+want)
			}
			if name == "timeout_ms" && prop.Description == "" {
				timeoutViolations = append(timeoutViolations, d.id+".timeout_ms (no description)")
			}
		}
	}

	sort.Strings(nameViolations)
	sort.Strings(timeoutViolations)
	for _, v := range nameViolations {
		t.Errorf("non-canonical param name: %s (rename, or grandfather in legacyParamNames if it shipped already)", v)
	}
	for _, v := range timeoutViolations {
		t.Errorf("timeout_ms param needs a description: %s", v)
	}
}
