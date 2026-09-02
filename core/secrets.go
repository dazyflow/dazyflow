// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
)

// ApprovalSigner builds the URL an external approver hits to resume a
// paused await_approval node. Production implementations sign the URL
// with an HMAC so the link can be embedded in an outbound email or
// Slack message without exposing the daemon's job IDs alone as the
// shared secret. Tests can supply a deterministic stub.
type ApprovalSigner interface {
	SignApprovalURL(graphRunID, nodeID string) string
}

// SecretProvider resolves a single secret-reference scheme. The engine
// keeps a registry keyed by Scheme; when it encounters a string like
// "secret://STRIPE_KEY" or "vault://prod/db-password" inside Job.Params or
// Job.Env it routes the path (everything after "scheme://") to the
// matching provider.
//
// Provider implementations live outside core because they touch I/O or
// vendor SDKs. See daemon/secrets.go for the encrypted secret store
// (secret://) and BuiltinProvider that ship today.
type SecretProvider interface {
	// Scheme returns the URI scheme this provider handles, e.g. "secret"
	// or "vault". Must be lowercase and not contain "://".
	Scheme() string

	// Get returns the resolved value for the supplied path (the part
	// after "scheme://"). An error here propagates as a node-level
	// failure: the workflow never sees an unresolved or partially
	// resolved secret.
	Get(ctx context.Context, path string) (string, error)
}

// tenantCtxKey carries the principal's tenant through to providers
// that need it for scoping (the encrypted built-in store reads it to
// pick the right per-tenant DEK). Tenant-agnostic providers can
// ignore the value entirely.
type tenantCtxKey struct{}

// WithTenant returns a derived context carrying the tenant ID. The
// engine wraps the job's ctx with this before invoking the secret
// substituter so tenant-scoped providers can read it during Get().
func WithTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantCtxKey{}, tenant)
}

// TenantFromContext returns the tenant carried by WithTenant. The
// second return is false when no tenant was set — providers that
// require one return an error in that case so an empty tenant
// never silently lands on the global namespace.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(string)
	return v, ok && v != ""
}

// flowCtxKey carries the running flow's (graph) ID through to the secret
// provider so it can resolve ${secret.NAME} by precedence: flow →
// organization. It's optional — an empty value (e.g. the in-process
// Engine.Run path) just degrades the cascade to the organization level.
type flowCtxKey struct{}

// WithFlow returns a derived context carrying the flow (graph) ID.
func WithFlow(ctx context.Context, flow string) context.Context {
	if flow == "" {
		return ctx
	}
	return context.WithValue(ctx, flowCtxKey{}, flow)
}

// FlowFromContext returns the flow ID carried by WithFlow, or
// ("", false) when none was set.
func FlowFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(flowCtxKey{}).(string)
	return v, ok && v != ""
}

// ValidSecretName accepts 1..128 chars of [A-Za-z0-9_.-]. It keeps path-like
// names and shell specials out of the secret store.
func ValidSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("name too long (max 128)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("name may only contain [A-Za-z0-9_.-]")
		}
	}
	return nil
}
