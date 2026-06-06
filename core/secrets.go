package core

import "context"

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
// "env://STRIPE_KEY" or "vault://prod/db-password" inside Job.Params or
// Job.Env it routes the path (everything after "scheme://") to the
// matching provider.
//
// Provider implementations live outside core because they touch I/O or
// vendor SDKs. See daemon/secrets.go for the EnvProvider and
// BuiltinProvider that ship today.
type SecretProvider interface {
	// Scheme returns the URI scheme this provider handles, e.g. "env"
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
// pick the right per-tenant DEK). Global providers like env:// can
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
