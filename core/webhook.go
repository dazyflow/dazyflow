package core

import "strings"

// WebhookSecrets returns every bearer key configured on a webhook_input
// node's params: the canonical `secrets` list plus the legacy single
// `secret`, each trimmed and non-empty, in order (legacy first).
//
// Multiple keys are what make zero-downtime rotation possible — the
// /trigger endpoint accepts ANY of them, so an operator can add a new
// key, migrate callers at leisure, then revoke the old one without ever
// dropping a request. A graph saved before multi-key support still
// carries a lone `secret`; this folds it into the same list so every
// reader (trigger auth, reachability lint, flow status) sees one shape.
func WebhookSecrets(params map[string]any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if s, ok := params["secret"].(string); ok {
		add(s)
	}
	// Arrays arrive as []any from JSON-decoded graphs and may be
	// []string from Go-constructed ones (tests, migrations).
	switch raw := params["secrets"].(type) {
	case []any:
		for _, v := range raw {
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range raw {
			add(s)
		}
	}
	return out
}

// GraphWebhookSecrets returns every webhook key across all webhook_input
// nodes in the graph — the full set the /trigger endpoint will accept.
func GraphWebhookSecrets(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == "webhook_input" {
			out = append(out, WebhookSecrets(n.Params)...)
		}
	}
	return out
}
