// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"fmt"
)

// Production implementations HMAC-sign the URL, so the link can ride an outbound
// email without the daemon's job IDs alone being the secret.
type ApprovalSigner interface {
	SignApprovalURL(graphRunID, nodeID string) string
}

// The engine keys a registry by Scheme and routes the path after "scheme://" to
// the match. Implementations live outside core because they touch I/O.
type SecretProvider interface {
	Scheme() string

	// An error becomes a node-level failure, so a flow never sees a partial secret.
	Get(ctx context.Context, path string) (string, error)
}

type tenantCtxKey struct{}

func WithTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantCtxKey{}, tenant)
}

// False when none was set; a provider requiring one then errors, so an empty
// tenant never lands on the global namespace.
func TenantFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(tenantCtxKey{}).(string)
	return v, ok && v != ""
}

// Resolves ${secret.NAME} flow-before-organization; empty degrades to the org.
type flowCtxKey struct{}

func WithFlow(ctx context.Context, flow string) context.Context {
	if flow == "" {
		return ctx
	}
	return context.WithValue(ctx, flowCtxKey{}, flow)
}

func FlowFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(flowCtxKey{}).(string)
	return v, ok && v != ""
}

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
