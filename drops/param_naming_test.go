package drops_test

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestParamNaming_Conventions is the param-name lint: it walks every
// registered drop's params schema and enforces the canonical names that keep
// the manifest metadata discoverable to an LLM (the whole point of the schema
// surface). Non-canonical names drift the vocabulary — "limit" vs
// "max_results", "max_bytes" vs "max_output_bytes" — so a model can't learn
// one convention and apply it everywhere.
//
// Two kinds of rule:
//
//   - Canonical-name rules flag a non-canonical spelling of a known concept.
//     Renaming an existing param is a BREAKING change (a saved graph stores
//     params by name, so a rename silently drops the saved value), so the
//     drift that already shipped is grandfathered in `legacyParamNames` with a
//     comment. The lint's job from here is to stop NEW drift: add a param with
//     a non-canonical name and this fails until it's renamed or, if it's a
//     genuine new exception, added to the allowlist with justification.
//   - The timeout_ms rule has no grandfathering: every timeout_ms param must
//     carry a description (they're all fixed to), so a new one without a
//     description fails immediately.
type schemaProps struct {
	Properties map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"properties"`
}

// canonical maps a non-canonical param name to the name that should be used
// instead. The lint flags any property whose name is a key here unless the
// (drop, name) pair is grandfathered below.
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
	// list-size: standardise on "limit"; these predate it.
	"gmail_search_messages.max_results": true,
	"gmail_get_message.max_results":     true,
	"github_list_issues.max_results":    true,
	// result caps: standardise on "max_bytes"; these predate it.
	"http_request.max_body_bytes":  true,
	"http_download.max_body_bytes": true,
	"shell.max_output_bytes":       true,
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
