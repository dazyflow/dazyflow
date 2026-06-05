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

// workspaceCtxKey / flowCtxKey carry the running flow's workspace and
// flow (graph) ID through to secret providers so they can resolve
// layered secrets: a ${secret.NAME} reference cascades flow → workspace
// → tenant, and ${secret.NAME} / ${secret.NAME} pin a single scope. Both
// are optional — an empty value just means that scope isn't available
// (e.g. the in-process Engine.Run path with no workspace), and the
// cascade degrades to the tenant level.
type workspaceCtxKey struct{}
type flowCtxKey struct{}

// WithWorkspace returns a derived context carrying the workspace. The
// engine wraps the job's ctx with this before secret resolution so
// workspace-scoped lookups can read it.
func WithWorkspace(ctx context.Context, workspace string) context.Context {
	if workspace == "" {
		return ctx
	}
	return context.WithValue(ctx, workspaceCtxKey{}, workspace)
}

// WorkspaceFromContext returns the workspace carried by WithWorkspace,
// or ("", false) when none was set.
func WorkspaceFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(workspaceCtxKey{}).(string)
	return v, ok && v != ""
}

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
