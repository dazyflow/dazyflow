// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

// AccountResource is one selectable thing inside a connected account — a
// Google Form, a spreadsheet, a Slack channel — surfaced by the resource
// pickers so a user chooses from a dropdown instead of pasting an opaque
// ID. ID is what gets stored in the param; Name is the human label. Kept
// here (not in daemon) so connector packages can return it from their
// listers without importing daemon. Distinct from ResourceDef, which is a
// stored ${resource.NAME} definition — different concept, similar word.
type AccountResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ResourceDef is a named, flow/organization-scoped configuration a user
// sets up once and references in node params as ${resource.NAME}. Unlike a
// secret (an opaque stored value), a resource points at external content —
// e.g. a specific Google Sheet — that a ResourceProvider fetches live when
// the reference is resolved. Stored as JSON under the reserved "res."
// namespace of the encrypted secret store.
type ResourceDef struct {
	Name string `json:"name"`
	// Type selects the fetcher, e.g. "google_sheet". Pluggable: a new
	// type is one provider-side fetcher plus an enumeration entry.
	Type string `json:"type"`
	// Config is the type-specific pointer (for google_sheet:
	// spreadsheet_id, range, account). Never holds a secret — the OAuth
	// token is resolved separately via the account's connection.
	Config map[string]any `json:"config"`
}

// ResourceProvider resolves the "resource" reference scheme. Unlike
// SecretProvider (whose Get returns a string), Resolve returns a structured
// value so a whole-string ${resource.NAME.rows} can hand a real array to a
// node rather than a JSON blob. The engine owns sub-path traversal (the
// ".rows" part); the provider only fetches NAME's root content. tenant and
// flow ride on ctx (WithTenant / WithFlow), set by the engine before
// resolution, so the provider can load the right flow/org-scoped def.
type ResourceProvider interface {
	// Scheme is always "resource" — present for symmetry with
	// SecretProvider and so the engine can key providers uniformly.
	Scheme() string
	// Resolve fetches the named resource's content. An error becomes a
	// node-level failure (code "resource"), distinct from a missing
	// secret, so the run UI and on_error edges can tell them apart.
	Resolve(ctx context.Context, name string) (any, error)
}
