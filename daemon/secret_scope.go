package daemon

import (
	"context"
	"fmt"
	"strings"
)

// Secret scoping is layered via reserved name prefixes inside the per-tenant
// store — the same technique the conn./oauth. connection secrets already use,
// so no schema change is needed. A bare name is tenant-scoped (shared by every
// flow in the tenant); "ws.<workspace>.<name>" is workspace-scoped; and
// "flow.<flowID>.<name>" is flow-scoped (only that flow can resolve it). The
// per-tenant encryption DEK is unchanged — the prefix is addressing, not a
// crypto boundary.
//
// ${secret.NAME} resolves with flow → workspace → tenant precedence (nearest
// wins, so a flow can override a shared secret of the same name). The explicit
// ${secret.NAME} / ${secret.NAME} schemes pin one scope with no fallback —
// use them when isolation must be guaranteed: a flow-scoped secret is keyed by
// the running flow's ID, so one flow can never resolve another flow's secret.

// SecretScope is the storage scope a secret lives at.
type SecretScope string

const (
	ScopeTenant    SecretScope = "tenant"
	ScopeWorkspace SecretScope = "workspace"
	ScopeFlow      SecretScope = "flow"

	secretWorkspacePrefix = "ws."
	secretFlowPrefix      = "flow."
)

// scopedSecretName maps (scope, workspace, flow, name) to the storage name.
// Tenant scope is the bare name (back-compatible with every existing secret).
func scopedSecretName(scope SecretScope, workspace, flow, name string) (string, error) {
	switch scope {
	case ScopeTenant, "":
		return name, nil
	case ScopeWorkspace:
		if workspace == "" {
			return "", fmt.Errorf("workspace-scoped secret %q needs a workspace", name)
		}
		return secretWorkspacePrefix + workspace + "." + name, nil
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
// namespace (scope prefixes, connection/OAuth secrets, or config rows) rather
// than a user's tenant-scoped secret. The tenant-scope listing hides these.
func isReservedSecretName(name string) bool {
	return strings.HasPrefix(name, secretWorkspacePrefix) ||
		strings.HasPrefix(name, secretFlowPrefix) ||
		strings.HasPrefix(name, "conn.") ||
		strings.HasPrefix(name, "oauth.") ||
		strings.HasPrefix(name, "cfg:")
}

// There is exactly one secret reference scheme — "secret" — implemented by
// EncryptedSecrets itself (Scheme() == "secret", Get() == the cascade). Scope
// is a write-time concept only (PutScoped / ListScoped); ${secret.NAME}
// resolves flow → workspace → tenant, GitHub-Actions style. There are no
// per-scope reference schemes.

// PutScoped writes a secret at the given scope. Workspace/flow are required
// for the workspace/flow scopes respectively.
func (e *EncryptedSecrets) PutScoped(ctx context.Context, tenant, workspace, flow string, scope SecretScope, name, value string) error {
	storageName, err := scopedSecretName(scope, workspace, flow, name)
	if err != nil {
		return err
	}
	return e.Put(ctx, tenant, storageName, value)
}

// DeleteScoped removes a secret at the given scope.
func (e *EncryptedSecrets) DeleteScoped(ctx context.Context, tenant, workspace, flow string, scope SecretScope, name string) error {
	storageName, err := scopedSecretName(scope, workspace, flow, name)
	if err != nil {
		return err
	}
	return e.Delete(ctx, tenant, storageName)
}

// ListScoped returns the user-visible secret names at one scope, with the
// scope prefix stripped. Tenant scope returns bare names, excluding every
// reserved namespace (scope prefixes, conn./oauth., cfg:).
func (e *EncryptedSecrets) ListScoped(ctx context.Context, tenant, workspace, flow string, scope SecretScope) ([]string, error) {
	all, err := e.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var prefix string
	switch scope {
	case ScopeTenant, "":
		out := make([]string, 0, len(all))
		for _, n := range all {
			if !isReservedSecretName(n) {
				out = append(out, n)
			}
		}
		return out, nil
	case ScopeWorkspace:
		if workspace == "" {
			return nil, fmt.Errorf("listing workspace secrets needs a workspace")
		}
		prefix = secretWorkspacePrefix + workspace + "."
	case ScopeFlow:
		if flow == "" {
			return nil, fmt.Errorf("listing flow secrets needs a flow")
		}
		prefix = secretFlowPrefix + flow + "."
	default:
		return nil, fmt.Errorf("unknown secret scope %q", scope)
	}
	out := make([]string, 0)
	for _, n := range all {
		if strings.HasPrefix(n, prefix) {
			out = append(out, strings.TrimPrefix(n, prefix))
		}
	}
	return out, nil
}
