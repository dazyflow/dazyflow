// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// A flow that ANSWERS its caller, as against the webhook trigger, whose callers
// get an acknowledgement and nothing else. Kept out of webhook.go because the two
// endpoints promise different things and should not drift into each other.
const (
	RequestInputModule = "request_input"
	ReplyModule        = "reply"
	ReplyDefaultStatus = 200
)

func GraphRequestSecrets(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == RequestInputModule {
			out = append(out, WebhookSecrets(n.Params)...)
		}
	}
	return out
}

func GraphRequestPublic(g Graph) bool {
	for _, n := range g.Nodes {
		if n.Module == RequestInputModule && WebhookPublic(n.Params) {
			return true
		}
	}
	return false
}

func ReplyNodeIDs(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == ReplyModule {
			out = append(out, n.ID)
		}
	}
	return out
}

func ReplyStatusCode(params map[string]any) int {
	code, ok := paramInt(params, "status_code")
	if !ok || code < 200 || code > 599 {
		return ReplyDefaultStatus
	}
	return code
}
