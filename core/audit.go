// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// AuditEvent is one recorded administrative action — who did what to which
// resource, when. Deliberately small and append-only: an audit trail for
// graph saves, run submissions, secret/key changes, and approvals.
//
// Detail carries optional non-sensitive context (e.g. a run ID or a
// decision). It MUST NOT contain secret values — only names/identifiers.
type AuditEvent struct {
	Time   time.Time `json:"time"`
	Tenant string    `json:"tenant"`
	Actor  string    `json:"actor"`  // principal subject
	Action string    `json:"action"` // e.g. "graph.save", "secret.delete"
	Target string    `json:"target"` // resource id/name the action touched
	Detail string    `json:"detail,omitempty"`
}

// AuditQuery scopes a List. Tenant is required (the audit trail is
// per-tenant); Limit/Offset paginate, newest first.
type AuditQuery struct {
	Tenant string
	Limit  int
	Offset int
}

// AuditLog is the append-only store behind the admin audit trail. Append
// is best-effort from the caller's view (a failed write must never fail
// the user action being audited); List powers the admin UI.
type AuditLog interface {
	Append(ctx context.Context, e AuditEvent) error
	List(ctx context.Context, q AuditQuery) ([]AuditEvent, error)
}
