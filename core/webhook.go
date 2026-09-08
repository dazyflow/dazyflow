// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "strings"

const (
	WebhookInputModule = "webhook_input"
	FormInputModule    = "form_input"
)

func WebhookSecrets(params map[string]any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
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

func GraphWebhookSecrets(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == WebhookInputModule {
			out = append(out, WebhookSecrets(n.Params)...)
		}
	}
	return out
}

// A deliberate switch rather than an inference from "no keys set": a freshly
// added Webhook step has no keys either, and one that opened a public endpoint the
// moment the flow was published would be a footgun the author never sees. Prefer
// the `key` query parameter /trigger reads.
func WebhookPublic(params map[string]any) bool {
	b, _ := params["public"].(bool)
	return b
}

func GraphWebhookPublic(g Graph) bool {
	for _, n := range g.Nodes {
		if n.Module == WebhookInputModule && WebhookPublic(n.Params) {
			return true
		}
	}
	return false
}
