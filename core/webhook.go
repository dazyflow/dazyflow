// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "strings"

// The two inbound-delivery triggers that are NOT the answering pair: a
// webhook, which acknowledges and returns, and the hosted form, which a person
// fills in. One door each — see core/request.go for Request/Reply.
const (
	WebhookInputModule = "webhook_input"
	FormInputModule    = "form_input"
)

// WebhookSecrets returns every bearer key configured on a webhook_input
// node's params: the `secrets` list, each trimmed and non-empty, in order.
//
// Multiple keys are what make zero-downtime rotation possible — the
// /trigger endpoint accepts ANY of them, so an operator can add a new
// key, migrate callers at leisure, then revoke the old one without ever
// dropping a request.
func WebhookSecrets(params map[string]any) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	// Arrays arrive as []any from JSON-decoded graphs and may be
	// []string from Go-constructed ones (tests).
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
		if n.Module == WebhookInputModule {
			out = append(out, WebhookSecrets(n.Params)...)
		}
	}
	return out
}

// WebhookPublic reports whether a webhook_input node accepts calls that carry
// no key at all.
//
// Off by default, and a deliberate switch rather than an inference from "no
// keys set": a freshly added Webhook step has no keys either, and a step that
// opened a public endpoint the moment the flow was published — without anyone
// choosing that — is a footgun the author never sees. Turning it on is the
// choice; a key-less, non-public step stays inert.
//
// It exists because a sender that cannot present a key is common: plenty of
// services let you configure a delivery URL and nothing else. Prefer putting
// the key in the URL (the /trigger endpoint reads a `key` query parameter for
// exactly that case) and keep this for senders that can carry neither.
func WebhookPublic(params map[string]any) bool {
	b, _ := params["public"].(bool)
	return b
}

// GraphWebhookPublic reports whether any webhook_input node in the graph is
// public. The graph has ONE /trigger address shared by its webhook steps, so
// one step marked public opens that address — configured keys still work,
// they are simply no longer required.
func GraphWebhookPublic(g Graph) bool {
	for _, n := range g.Nodes {
		if n.Module == WebhookInputModule && WebhookPublic(n.Params) {
			return true
		}
	}
	return false
}
