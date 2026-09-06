// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// The Request/Reply pair: a flow that ANSWERS its caller, as opposed to the
// webhook trigger, whose callers get an immediate acknowledgement and nothing
// else. Kept here rather than in webhook.go because the two endpoints promise
// different things and should not drift into each other.
const (
	// RequestInputModule starts a flow from a call that waits for an answer.
	RequestInputModule = "request_input"
	// ReplyModule is the step whose value is sent back to that caller.
	ReplyModule = "reply"
	// ReplyDefaultStatus is the HTTP status a Reply sends when the author
	// sets none.
	ReplyDefaultStatus = 200
)

// GraphRequestSecrets returns every bearer key across the graph's
// request_input nodes — the full set the /call endpoint accepts. Same
// multi-key rotation story as GraphWebhookSecrets.
func GraphRequestSecrets(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == RequestInputModule {
			out = append(out, WebhookSecrets(n.Params)...)
		}
	}
	return out
}

// ReplyNodeIDs returns the graph's Reply steps, in node order. The /call
// handler watches all of them: whichever finishes first answers the caller.
func ReplyNodeIDs(g Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Module == ReplyModule {
			out = append(out, n.ID)
		}
	}
	return out
}

// ReplyStatusCode reads the HTTP status a Reply node sends. Out-of-range
// values fall back to the default — executeReply already fails the step on
// one, so this only guards a record written by an older/hand-edited graph.
func ReplyStatusCode(params map[string]any) int {
	code, ok := paramInt(params, "status_code")
	if !ok || code < 200 || code > 599 {
		return ReplyDefaultStatus
	}
	return code
}
