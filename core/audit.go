// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// One marker across every surface that pseudonymises rather than deletes. In core
// because both auth and daemon write it, and a marker that drifted between
// packages would split one person's erasure into two shapes.
const ErasedIdentity = "[erased]"

// Detail MUST NOT contain secret values — only names and identifiers.
type AuditEvent struct {
	Time   time.Time `json:"time"`
	Tenant string    `json:"tenant"`
	Actor  string    `json:"actor"`  // principal subject
	Action string    `json:"action"` // e.g. "graph.save", "secret.delete"
	Target string    `json:"target"` // resource id/name the action touched
	Detail string    `json:"detail,omitempty"`
}

type AuditQuery struct {
	Tenant string
	Actor  string
	Limit  int
	Offset int
}

// Append is best-effort: a failed write must never fail the action being audited.
type AuditLog interface {
	Append(ctx context.Context, e AuditEvent) error
	List(ctx context.Context, q AuditQuery) ([]AuditEvent, error)
}
