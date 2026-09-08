// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

type AccountResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Where a secret is an opaque stored value, a resource points at external
// content a ResourceProvider fetches live on resolution. Stored as JSON under the
// reserved "res." namespace.
type ResourceDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Never holds a secret: the OAuth token resolves through the account's connection.
	Config map[string]any `json:"config"`
}

type ResourceProvider interface {
	Scheme() string
	// The error becomes a node-level failure with code "resource", distinct from a
	// missing secret so the run UI and on_error edges can tell them apart.
	Resolve(ctx context.Context, name string) (any, error)
}
