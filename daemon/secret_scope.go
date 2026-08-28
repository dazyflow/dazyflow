// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"strings"
)

// Secret scoping has two levels: organization (the tenant, shared by every
// flow) and flow (only that flow can resolve it). It's layered via a reserved
// name prefix inside the per-tenant store — the same technique the conn./oauth.
// connection secrets use, so no schema change is needed. A bare name is the
// organization scope; "flow.<flowID>.<name>" is flow-scoped. The per-tenant
// encryption DEK is unchanged — the prefix is addressing, not a crypto boundary.
//
// ${secret.NAME} resolves with flow → organization precedence (nearest wins,
// so a flow can override an organization secret of the same name). A flow's
// secret is keyed by the running flow's ID, so one flow can never resolve
// another flow's secret.

// SecretScope is the storage scope a secret lives at.
type SecretScope string

const (
	ScopeTenant SecretScope = "tenant" // organization scope (shared by every flow)
	ScopeFlow   SecretScope = "flow"

	secretFlowPrefix = "flow."
	secretConnPrefix = "conn."
	// secretResourcePrefix namespaces flow-resource definitions (Phase 4:
	// ${resource.NAME}). A flow-scoped resource is stored as
	// "flow.<flowID>.res.<name>"; an org one as "res.<name>". The values
	// are config (a sheet pointer), not credentials, but they live in the
	// same store and are hidden from the Credentials listing.
	secretResourcePrefix = "res."
	// secretEmailTmplPrefix namespaces org-created email templates (the HTML
	// layout shells the email drops wrap a body in). Stored as
	// "emailtmpl.<name>" at organization scope — templates are tenant-wide,
	// with no flow tier. The HTML is not a credential but lives in the same
	// store and is hidden from the Credentials listing.
	secretEmailTmplPrefix = "emailtmpl."
)

// scopedSecretName maps (scope, flow, name) to the storage name. Organization
// (tenant) scope is the bare name; flow scope is prefixed by the flow ID.
func scopedSecretName(scope SecretScope, flow, name string) (string, error) {
	switch scope {
	case ScopeTenant, "":
		return name, nil
	case ScopeFlow:
		if flow == "" {
			return "", fmt.Errorf("flow-scoped secret %q needs a flow", name)
		}
		return secretFlowPrefix + flow + "." + name, nil
	default:
		return "", fmt.Errorf("unknown secret scope %q", scope)
	}
}

// isReservedSecretName reports whether a stored name belongs to a reserved
// namespace (the flow prefix, connection/OAuth secrets, or config rows) rather
// than a user's organization secret. The organization listing hides these.
func isReservedSecretName(name string) bool {
	return strings.HasPrefix(name, secretFlowPrefix) ||
		strings.HasPrefix(name, secretConnPrefix) ||
		strings.HasPrefix(name, "oauth.") ||
		strings.HasPrefix(name, "cursor.") ||
		strings.HasPrefix(name, reconnectNeededPrefix) ||
		strings.HasPrefix(name, "pollstate.") ||
		strings.HasPrefix(name, "httpcache.") ||
		strings.HasPrefix(name, secretGitCredPrefix) ||
		strings.HasPrefix(name, secretResourcePrefix) ||
		strings.HasPrefix(name, secretEmailTmplPrefix) ||
		strings.HasPrefix(name, "cfg:")
}

// orgAuthoritativeSecretName reports whether a name lives in a namespace that
// must resolve ONLY at organization scope: connection (conn.) and OAuth
// (oauth.) credentials. The ${secret.} flow→organization cascade is skipped for
// these in EncryptedSecrets.Get so a flow-scoped value can never shadow — and so
// silently override — the org's authoritative credential. Distinct from
// isReservedSecretName (which also covers flow-scopable namespaces like res./
// emailtmpl. and daemon bookkeeping that legitimately read at their own scope).
func orgAuthoritativeSecretName(name string) bool {
	return strings.HasPrefix(name, secretConnPrefix) ||
		strings.HasPrefix(name, "oauth.")
}

// ListConnectionNames returns the connection secret names (the
// conn.<slug>.<key> namespace) for a tenant, with the conn. prefix intact.
// ListScoped hides these from the organization listing so the Credentials page
// stays clean, but the Apps page needs them to tell which integrations are
// connected — see the ?include=conn path in listSecrets.
func (e *EncryptedSecrets) ListConnectionNames(ctx context.Context, tenant string) ([]string, error) {
	all, err := e.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, n := range all {
		if strings.HasPrefix(n, secretConnPrefix) {
			out = append(out, n)
		}
	}
	return out, nil
}

// There is exactly one secret reference scheme — "secret" — implemented by
// EncryptedSecrets itself (Scheme() == "secret", Get() == the cascade). Scope
// is a write-time concept only (PutScoped / ListScoped); ${secret.NAME}
// resolves flow → organization, GitHub-Actions style. There are no per-scope
// reference schemes.

// PutScoped writes a secret at the given scope. flow is required for the flow
// scope.
func (e *EncryptedSecrets) PutScoped(ctx context.Context, tenant, flow string, scope SecretScope, name, value string) error {
	storageName, err := scopedSecretName(scope, flow, name)
	if err != nil {
		return err
	}
	return e.Put(ctx, tenant, storageName, value)
}

// DeleteScoped removes a secret at the given scope.
func (e *EncryptedSecrets) DeleteScoped(ctx context.Context, tenant, flow string, scope SecretScope, name string) error {
	storageName, err := scopedSecretName(scope, flow, name)
	if err != nil {
		return err
	}
	return e.Delete(ctx, tenant, storageName)
}

// ListScoped returns the user-visible secret names at one scope, with the flow
// prefix stripped. Organization scope returns bare names, excluding every
// reserved namespace (flow prefix, conn./oauth., cfg:).
func (e *EncryptedSecrets) ListScoped(ctx context.Context, tenant, flow string, scope SecretScope) ([]string, error) {
	all, err := e.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	switch scope {
	case ScopeTenant, "":
		out := make([]string, 0, len(all))
		for _, n := range all {
			if !isReservedSecretName(n) {
				out = append(out, n)
			}
		}
		return out, nil
	case ScopeFlow:
		if flow == "" {
			return nil, fmt.Errorf("listing flow secrets needs a flow")
		}
		prefix := secretFlowPrefix + flow + "."
		out := make([]string, 0)
		for _, n := range all {
			if !strings.HasPrefix(n, prefix) {
				continue
			}
			name := strings.TrimPrefix(n, prefix)
			// A flow-scoped value can itself sit in a reserved namespace —
			// notably resource defs stored as flow.<flow>.res.<name>. Those
			// aren't user secrets, so keep them out of the Credentials list.
			if isReservedSecretName(name) {
				continue
			}
			out = append(out, name)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown secret scope %q", scope)
	}
}
