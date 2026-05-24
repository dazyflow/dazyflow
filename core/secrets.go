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
