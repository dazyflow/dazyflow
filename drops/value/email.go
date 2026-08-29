// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// email.go is a typed source field, the address sibling of url.go and
// phone.go: the Text drop's inline-or-wire ergonomics, constrained to one
// email address and normalized. Like both it declares an input port (so the
// address can be computed upstream) and VALIDATES at run time, failing the
// node on a bad address (bad_param) rather than emitting a `valid` boolean —
// a malformed address is a mistake to surface at the field, not a value to
// thread onward.
//
// Parsing is net/mail.ParseAddress, NOT a regular expression. The RFC 5322
// grammar admits quoted local parts, escapes and comments, so every short
// email regex is wrong in both directions at once: it rejects addresses that
// work and accepts ones that don't. The stdlib parser is already what
// internal/smtputil and drops/notify/verify trust to decide whether an
// address is sendable, and a drop that disagreed with the step that actually
// sends the mail would be worse than no check at all.
//
// ParseAddress also accepts display-name form ("Ada <ada@acme.com>"), which
// is a feature here rather than a hazard: 'out' carries the bare address a
// later step needs, and the name lands on its own pin instead of being
// smuggled into the address.
//
// Two things it deliberately does NOT do. It does not look up MX records:
// deliverability is a network call with its own latency and failure modes,
// and "the domain resolves" is not the same claim as "this is an address".
// And it does not lowercase the local part — only the domain, which is
// case-insensitive by spec. Case in a local part is the mail server's
// business, and folding it is how you turn a working address into a bounce.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "email",
			Version:     "1.0",
			Label:       "Email",
			Color:       "#8b5cf6",
			Icon:        "mail",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"email", "address", "mail", "recipient", "validate", "normalize", "domain"},
			Description: "Hold an email address — type it inline or connect a string into the 'email' input — and emit it on 'out', but only after checking it parses as a real address. A malformed address fails the step up front instead of surfacing later as a bounce or a cryptic SMTP rejection. Display-name form is understood: \"Ada Lovelace <ada@acme.com>\" puts the bare address on 'out' and \"Ada Lovelace\" on 'name'. It also splits the address so you can act on its parts without string surgery: 'local' (ada) and 'domain' (acme.com, lower-cased) — so a flow can route everyone from one company down its own branch. Feed 'out' straight into the Send email or Gmail steps.",
			Summary:     "Validate an email address and emit it plus its local part, domain, and display name.",
			Examples: []core.ParamsExample{
				{
					Title:  "A plain address",
					Params: json.RawMessage(`{"email":"ada@Acme.COM"}`),
					Notes:  "'out' is \"ada@acme.com\" — the domain is lower-cased, the local part is left exactly as written. 'local' is \"ada\"; 'domain' is \"acme.com\".",
				},
				{
					Title:  "With a display name",
					Params: json.RawMessage(`{"email":"Ada Lovelace <ada@acme.com>"}`),
					Notes:  "'out' is the bare \"ada@acme.com\" and 'name' is \"Ada Lovelace\", so a later step gets a clean recipient rather than the whole string.",
				},
				{
					Title:  "Route by company",
					Params: json.RawMessage(`{"email":"someone@acme.com"}`),
					Notes:  "Wire 'domain' into an If step set to equals \"acme.com\" to send one company's mail down its own branch.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Not marked Required: the address may instead be typed into the
				// `email` param. The schema's required:["email"] + the editor's
				// config check (a wired input satisfies it) enforce "type it OR
				// wire it" — mirrors url / phone / rss / gmail_send_email.
				{Port: "email", Label: "Email", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Address", MIME: []string{"text/plain"}},
				{Port: "local", Label: "Local part", MIME: []string{"text/plain"}},
				{Port: "domain", Label: "Domain", MIME: []string{"text/plain"}},
				{Port: "name", Label: "Display name", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"email":{"type":"string","format":"email","title":"Email","description":"An email address — plain (ada@acme.com) or with a display name (Ada Lovelace <ada@acme.com>). Type it here, or connect a string into the 'email' input."}
				},
				"required":["email"]
			}`),
			Idempotent: true,
		},
		Execute: executeEmail,
	})
}

func executeEmail(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Wired 'email' input wins over the inline param (params.TextInputOr), so the
	// address can be computed upstream or set on the node.
	raw, ok := params.TextInputOr(job, "email", params.StringDefault(job.Params, "email", ""))
	if !ok {
		return params.Err(job, "bad_input", "the connected 'email' input must be text"), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return params.Err(job, "bad_param", "email is required: connect the 'email' input or set the email param"), nil
	}

	addr, err := mail.ParseAddress(raw)
	if err != nil {
		// ParseAddress's own error text is about RFC productions ("mail: missing
		// '@' or angle-addr") and means nothing to someone who mistyped a
		// colleague's address, so it is deliberately not passed through. A list
		// lands here too, which is the right answer: this drop holds ONE address.
		return params.Err(job, "bad_param", fmt.Sprintf("not a valid email address: %q", raw)), nil
	}

	// ParseAddress guarantees an @, but be explicit rather than trusting an
	// index into someone else's invariant.
	at := strings.LastIndex(addr.Address, "@")
	if at <= 0 || at == len(addr.Address)-1 {
		return params.Err(job, "bad_param", fmt.Sprintf("not a valid email address: %q", raw)), nil
	}
	local := addr.Address[:at]
	domain := strings.ToLower(addr.Address[at+1:])

	// The second gate, and phone.go's IsValidNumber is the precedent: parsing
	// says the shape is legal, not that the value is real. A dotless domain is
	// legal in the RFC (an intranet host) and is, in a flow that is about to
	// send mail to it, essentially always a typo for the real thing.
	if !strings.Contains(domain, ".") {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"not a deliverable email address: the domain %q has no dot. A bare host is legal on an internal network but is almost always a typo — did you mean %s.com?",
			domain, addr.Address)), nil
	}

	normalized := local + "@" + domain

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":    {MIME: "text/plain", Inline: normalized},
			"local":  {MIME: "text/plain", Inline: local},
			"domain": {MIME: "text/plain", Inline: domain},
			// Empty for a plain address rather than absent, so a template
			// referencing it renders nothing instead of failing.
			"name": {MIME: "text/plain", Inline: addr.Name},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"address": normalized,
				"local":   local,
				"domain":  domain,
				"name":    addr.Name,
			}},
		},
	}, nil
}
