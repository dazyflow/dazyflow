package core

import "context"

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
