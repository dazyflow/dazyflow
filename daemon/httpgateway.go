// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// HTTPGateway exposes Service over JSON/HTTP so browsers and other
// non-gRPC clients can drive Dazyflow. The endpoint surface is small
// on purpose — just enough to power a visual editor:
//
//	GET    /api/v1/drops                                      — list drop manifests
//	GET    /api/v1/modules                                    — alias of /drops (legacy)
//	GET    /api/v1/graphs?tenant=X&workspace=Y                — list graph IDs
//	GET    /api/v1/graphs/{tenant}/{workspace}/{id}           — load graph (head)
//	PUT    /api/v1/graphs/{tenant}/{workspace}/{id}           — save graph
//	POST   /api/v1/graphs/{tenant}/{workspace}/{id}/run       — submit run
//	GET    /api/v1/jobs/{jobID}                               — job-record snapshot
//	GET    /api/v1/jobs/{jobID}/events                        — SSE stream of bus events
//	GET    /healthz                                           — liveness
//
// Auth is bearer token in `Authorization: Bearer <api-key>` — the same
// API-key chain the gRPC server uses. CORS is permissive in V1; tighten
// per-deployment via AllowedOrigins.
type HTTPGateway struct {
	svc            *Service
	logger         *log.Logger
	AllowedOrigins []string // empty = "*"

	// LogTail, when set, captures the daemon's log stream for the
	// platform-admin "System log" viewer (GET /api/v1/admin/system/log).
	// Nil leaves that endpoint returning 501 — the feature is simply off.
	// cmd/dzd installs it as a tee behind the standard logger at startup.
	LogTail *LogTail

	// WildcardDomain, when set (e.g. "dazyflow.app"), treats every
	// subdomain "<org>.dazyflow.app" as an allowed browser origin for
	// the CORS allowlist + the CSRF origin check, on top of the exact
	// entries in AllowedOrigins. It also lets the sign-in page derive
	// the target org from the host and powers the OAuth sign-in handoff
	// (Option B: the apex callback bounces the session to the org
	// subdomain via a one-time token so cookies stay host-only — see
	// authHandoff). Empty disables all per-org-subdomain behaviour, so
	// single-host deployments are unaffected. Wired from
	// $DAZYFLOW_WILDCARD_DOMAIN.
	//
	// Must have at least two labels (e.g. "dazyflow.app"). A single-label
	// value like "com" would suffix-match every ".com" origin and is
	// rejected: cmd/dzd refuses to boot on one, and originAllowed ignores it
	// (trusts nobody) as a defense-in-depth backstop. See IsValidWildcardDomain.
	WildcardDomain string

	// Sessions and Users power the email+password sign-in flow. Both
	// are optional — leaving them nil disables the /auth endpoints
	// without breaking API-key auth.
	Sessions auth.SessionStore
	Users    auth.UserStore
	// SessionTTL is the sliding idle window: a session is issued for this
	// long and, while in use, slid forward by it on each request (see
	// maybeRenewSession), so an active user is never forced to re-sign-in.
	// Defaults to 7d when zero.
	SessionTTL time.Duration
	// MaxSessionAge caps how long a session can live from CreatedAt even
	// under continuous use, forcing a periodic fresh sign-in regardless of
	// activity. Zero disables the cap (sessions can slide forever); the
	// daemon wires a 30d default.
	MaxSessionAge time.Duration

	// TOTPKey is the 32-byte AES key that encrypts users' TOTP secrets
	// at rest (decoded from DAZYFLOW_TOTP_KEY by cmd/dzd). When it's not
	// the required 32 bytes, 2FA is considered un-configured at the
	// install level: the /me/totp + /auth/totp endpoints return 503 and
	// sign-in never asks for a second factor.
	TOTPKey []byte
	// TOTPChallenges bridges the password step and the second-factor
	// step during sign-in. An in-memory store is fine — challenges live
	// five minutes and losing them on restart just re-prompts for the
	// password. Nil with a configured TOTPKey disables the challenge leg
	// (sign-in would fail closed for enrolled users), so cmd/dzd wires
	// both together.
	TOTPChallenges auth.TOTPChallengeStore

	// EncryptedSecrets powers the /api/v1/secrets CRUD endpoints. Nil
	// means the encrypted store isn't configured for this deployment;
	// the routes return 501 in that case so callers can detect the
	// feature is off.
	EncryptedSecrets *EncryptedSecrets

	// OAuth powers the /api/v1/oauth/{provider}/authorize and
	// /callback endpoints. Nil = OAuth flows disabled (returns 501).
	// Setting it implies EncryptedSecrets is also set, since OAuth
	// writes tokens into the encrypted store.
	OAuth *OAuthRegistry

	// EnableSignup opens POST /api/v1/auth/signup for self-serve
	// account creation. Defaults to false — production deployments
	// often want admin-invite-only signup. The dzd binary opts in
	// via --signup or $DAZYFLOW_ENABLE_SIGNUP.
	EnableSignup bool

	// PlatformAdmins is the lowercased email allowlist that bootstraps the
	// platform:admin super-admin role (e.g. to reach /admin/oauth). It's the
	// immutable layer that breaks the chicken-and-egg: matching users get the
	// role stamped onto their session at sign-in/signup/SSO (see
	// elevatePlatformAdmin), so it can't be edited without a restart. Wired
	// from $DAZYFLOW_PLATFORM_ADMINS. The mutable layer is PlatformAdminGrants.
	PlatformAdmins []string

	// PlatformAdminGrants is the runtime grant store — the mutable counterpart
	// to the env allowlist, letting a platform admin grant/revoke the role from
	// the UI without a redeploy. Feeds the same elevatePlatformAdmin chokepoint.
	// Nil leaves grant/revoke endpoints returning 501 (env allowlist still
	// works). See platformadmin.go.
	PlatformAdminGrants PlatformAdminStore

	// platformAdminGranted remembers which allowlisted emails have already
	// had a "platform_admin.granted" audit event emitted this process, so the
	// escalation is recorded on first apply rather than on every session
	// issue (elevatePlatformAdmin runs at each sign-in/SSO). Keyed by
	// lowercased email → struct{}.
	platformAdminGranted sync.Map

	// Support feature (see TODO-support-tickets.md). SupportAgents is the
	// runtime grant store deciding who gets core.SupportAgentRole at session
	// issue; Grants persists AccessGrants (the consented, time-boxed views);
	// Bundles persists redacted SupportBundleRecords. All nil-safe: nil leaves
	// the support endpoints returning 501/empty, and no agent is elevated.
	SupportAgents SupportAgentStore
	Grants        core.GrantStore
	Bundles       core.BundleStore
	// SupportGrantTTL is how long an approved AccessGrant stays valid. Zero
	// falls back to defaultSupportGrantTTL (4h).
	SupportGrantTTL time.Duration
	// supportAgentGranted mirrors platformAdminGranted: once-per-email audit of
	// the support-agent elevation.
	supportAgentGranted sync.Map
	// supportNow is the clock for grant expiry/decision timestamps; nil = time.Now.
	// Set in tests for deterministic expiry.
	supportNow func() time.Time

	// UpdateURL is the canonical deployment's public service descriptor
	// (GET /api/v1), whose build.version the admin System section reads as
	// "the latest released version" to compare against this build. No auth
	// — it's a public endpoint. Empty disables the check (the page still
	// loads). Wired from $DAZYFLOW_UPDATE_URL, defaulting to the project's
	// production origin.
	UpdateURL string

	// EnableMetrics mounts an unauthenticated GET /metrics Prometheus
	// endpoint. Off by default: it exposes tenant names + disk usage, so
	// operators opt in via --metrics and restrict scrape access at the
	// network/proxy layer. Wired by dzd.
	EnableMetrics bool

	// Audit, when set, records administrative actions (graph save/run,
	// secret + API-key changes, approvals, cancels) and powers
	// GET /api/v1/admin/audit. Nil disables auditing + that endpoint.
	Audit core.AuditLog

	// SlackEvents handles Slack Events API POSTs (app_mention etc.).
	// Nil = the route returns 501. Wired by dzd when the Slack
	// signing-secret flag/env is set.
	SlackEvents *SlackEventsHandler

	// GitHubEvents handles GitHub webhook POSTs (push, pull_request).
	// Nil = the route returns 501. Wired by dzd when the GitHub
	// webhook-secret flag/env is set.
	GitHubEvents *GitHubEventsHandler

	// StripeEvents handles tenant Stripe webhook POSTs (payment
	// events for stripe_on_payment triggers — distinct from Billing's
	// platform webhook). Nil = the route returns 501. Wired by dzd
	// when the encrypted secret store is configured; auth is the
	// per-tenant STRIPE_WEBHOOK_SECRET signing secret.
	StripeEvents *StripeEventsHandler

	// Billing holds the Stripe wiring (Checkout/portal client + webhook
	// secret). Nil = checkout/portal/webhook routes return 501; the
	// read-only GET /me/billing still works so the UI can render the
	// plan state on deployments without Stripe.
	Billing *BillingHandler

	// Memberships powers the multi-org model: one user can be a member
	// of many orgs (in addition to the "home" org their User record
	// names). Nil means the deployment is still single-org per user —
	// the switcher hides and the invite flow returns 501.
	Memberships auth.MembershipStore

	// Invitations is the pending-invites store backing the admin
	// invite flow and the /invite/<token> acceptance handler. Nil
	// disables both endpoints (501).
	Invitations auth.InvitationStore

	// OrgAuth stores per-org SSO config (today: Google Workspace
	// client_id/secret/domain). Nil disables the "Sign in with Google"
	// option and the admin SSO settings endpoints.
	OrgAuth auth.OrgAuthStore

	// Profiles stores per-org human-facing identity (display name; will
	// hold logo / billing email / etc. later). Distinct from the
	// immutable tenant ID so users can rename their org. Nil means the
	// raw tenant ID surfaces everywhere the UI would otherwise show a
	// display name.
	Profiles auth.OrgProfileStore

	// Blocklist is the platform-admin ban list. The signup path checks it
	// to refuse a banned email/domain re-registering, and the platform
	// admin endpoints manage it. Nil = nothing is banned (and bans can't
	// be created — dev deployments without the Postgres backend).
	Blocklist auth.BlocklistStore

	// DropSwitches is the platform-admin drop killswitch store. The
	// platform admin endpoints list + toggle it here; the engine resolver
	// (wired separately) is what actually blocks a disabled drop from
	// running. Nil disables the management endpoints (501).
	DropSwitches DropSwitchStore

	// Webhook serves /trigger/ and /form/ on the main HTTP listener so
	// a default HTTP-only deploy can fire webhook-triggered flows and
	// serve hosted intake forms without a separate listener. The
	// gateway auto-constructs one if left nil.
	Webhook *WebhookListener

	// Approval serves the HMAC-verified POST /approve/{run}/{node}
	// endpoint for unauthenticated email/Slack approvers. Set by
	// cmd/dzd when DAZYFLOW_APPROVAL_HMAC_SECRET is configured. Nil
	// leaves the route unregistered so the gateway 404s on /approve/*.
	Approval *ApprovalListener

	// AuthRateLimit, when set, throttles the sign-in / sign-up
	// endpoints per client IP. Nil disables throttling (dev default).
	AuthRateLimit *ipRateLimiter

	// WebhookRateLimit throttles the unauthenticated inbound surfaces
	// (/trigger and the Slack/GitHub/Stripe event endpoints) per client
	// IP. These take a credential/HMAC check, but the check runs work
	// (workspace + graph loads, large-body HMAC) before it can reject —
	// so without a throttle they invite secret brute-forcing and CPU-burn
	// floods. Lazily defaulted in mountRoutes when left nil.
	WebhookRateLimit *ipRateLimiter

	// TrustProxyHeaders makes the gateway honor X-Forwarded-Proto when
	// deciding whether a request arrived over HTTPS (for the Secure
	// cookie flag + HSTS). Enable ONLY when dzd sits behind a trusted
	// TLS-terminating reverse proxy — otherwise a direct client could
	// spoof the header. Off by default.
	TrustProxyHeaders bool

	// Metrics, when set, accumulates HTTP RED (rate/errors/duration) and
	// per-node execution-latency series for the /metrics endpoint. Nil
	// leaves those series unreported (the gauges in metrics.go still
	// work). Shared with the workers so node latencies land here too.
	Metrics *Metrics

	// DBPool, when set, is the shared Postgres pool. The metrics
	// endpoint reads its Stat() for connection-pool saturation gauges
	// (the earliest warning that the pool is undersized). Nil = no pool
	// metrics (in-memory dev deployments).
	DBPool *pgxpool.Pool

	// ReadyCheck, when set, is invoked by GET /readyz to verify
	// dependencies (e.g. a Postgres ping) before reporting ready. Nil
	// means "ready == alive" (the dev/in-memory deployment has no
	// external dep to gate on).
	ReadyCheck func(ctx context.Context) error

	// WebDist, when non-empty, points at a directory containing the
	// built React frontend (e.g. web/dist). Unknown GET paths under
	// / are served from this directory, with index.html as the SPA
	// fallback so client-side routes resolve. Empty = no static
	// serving — the gateway is API-only and unknown paths 404.
	WebDist string

	// idempotency caches successful responses keyed by
	// Idempotency-Key for /me/flows/{id}/run, /me/runs/{id}/cancel,
	// and any other mutating spec-aligned route wired through
	// idempotencyMiddleware. Lazily initialized in NewHTTPGateway.
	idempotency *idempotencyStore

	// LandingDir, when non-empty, points at a directory of static
	// marketing-site files (the separate dazyflow-landing repo:
	// landing.html, style.css, /pricing, /privacy, /terms, assets).
	// When set alongside WebDist, GET / is auth-gated: a signed-in
	// browser (valid session cookie) gets the SPA shell, an anonymous
	// visitor gets landing.html. Marketing pages/assets that resolve
	// to a real file under LandingDir serve publicly; everything else
	// falls through to the SPA. Empty = no landing; / serves the SPA
	// for everyone (the historical behaviour).
	LandingDir string
}

func NewHTTPGateway(svc *Service) *HTTPGateway {
	return &HTTPGateway{
		svc:         svc,
		logger:      log.New(log.Writer(), "http-api: ", log.LstdFlags),
		idempotency: newIdempotencyStore(),
	}
}

// ServeListener serves on an already-bound listener and blocks until
// ctx is cancelled. Lets the daemon bind on the main goroutine (so a
// bind failure can fail-loud at startup) and hand the live listener
// here to serve in the background.
func (h *HTTPGateway) ServeListener(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	srv := &http.Server{
		Handler:           h.withCORSAndLogging(h.verifyCookieOrigin(h.limitRequestBody(jsonErrors(mux)))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout — SSE responses are long-lived. Per-request
		// deadlines come from the client's connection (and context).
		IdleTimeout: 60 * time.Second,
	}
	errC := make(chan error, 1)
	go func() {
		h.logger.Printf("listening on %s", ln.Addr())
		errC <- srv.Serve(ln)
	}()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errC:
		return err
	}
}

func (h *HTTPGateway) mountRoutes(mux *http.ServeMux) {
	// Default the unauthenticated-surface limiter when the operator hasn't
	// set one. These endpoints (trigger + provider events) are reachable by
	// strangers, so they must always be throttled — a generous per-IP
	// allowance that legitimate webhook senders won't hit but that caps a
	// brute-force/flood.
	if h.WebhookRateLimit == nil {
		h.WebhookRateLimit = newIPRateLimiter(defaultWebhookRatePerMin, defaultWebhookRateBurst)
	}
	// Liveness: the process is up and serving. Never touches deps.
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write([]byte("ok")); err != nil {
			h.logger.Printf("healthz: write response: %v", err)
		}
	})
	// Readiness: can the process actually serve requests (deps reachable)?
	// ReadyCheck is nil for the dev/in-memory deployment — then ready ==
	// alive. With Postgres, cmd/dzd wires a pool ping so orchestration
	// holds traffic until the DB is reachable.
	mux.HandleFunc("GET /readyz", func(rw http.ResponseWriter, r *http.Request) {
		if h.ReadyCheck != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := h.ReadyCheck(ctx); err != nil {
				writeJSONError(rw, http.StatusServiceUnavailable, "not ready: "+err.Error())
				return
			}
		}
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write([]byte("ready")); err != nil {
			h.logger.Printf("readyz: write response: %v", err)
		}
	})
	// Prometheus scrape endpoint, opt-in (exposes tenant names + usage).
	if h.EnableMetrics {
		mux.HandleFunc("GET /metrics", h.metrics)
	}
	mux.HandleFunc("POST /api/v1/auth/signin", h.rateLimitAuth(h.signIn))
	mux.HandleFunc("POST /api/v1/auth/signup", h.rateLimitAuth(h.signUp))
	mux.HandleFunc("POST /api/v1/auth/verify-email", h.rateLimitAuth(h.verifyEmail))
	// Password reset (forgot → email link → set new password). Both
	// unauthenticated and rate-limited; the token in reset-password is
	// the credential. See password_reset.go.
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.rateLimitAuth(h.requestPasswordReset))
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.rateLimitAuth(h.resetPassword))
	// Rate-limited like the other auth routes: resend mints+sends a fresh
	// token, so an authenticated client must not be able to hammer it to spam
	// email or churn tokens. IP limiter wraps the auth check.
	mux.HandleFunc("POST /api/v1/me/verification/resend", h.rateLimitAuth(h.requireAuth(h.resendVerification)))
	mux.HandleFunc("POST /api/v1/auth/signout", h.signOut)
	// Leg 2 of sign-in for TOTP-enrolled users. Unauthenticated (the
	// challenge token is the principal) and rate-limited like the rest
	// of the auth surface.
	mux.HandleFunc("POST /api/v1/auth/totp", h.rateLimitAuth(h.totpVerify))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.uploadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/list", h.requireAuth(h.listWorkspaceFiles))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/download", h.requireAuth(h.downloadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/usage", h.requireAuth(h.workspaceFileUsage))
	mux.HandleFunc("DELETE /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.deleteWorkspaceFile))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/mkdir", h.requireAuth(h.mkdirWorkspaceDir))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/rename", h.requireAuth(h.renameWorkspaceFile))
	mux.HandleFunc("GET /api/v1/secrets", h.requireAuth(h.listSecrets))
	mux.HandleFunc("PUT /api/v1/secrets/{name}", h.requireAuth(h.putSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", h.requireAuth(h.deleteSecret))
	mux.HandleFunc("GET /api/v1/resources", h.requireAuth(h.listResources))
	mux.HandleFunc("PUT /api/v1/resources/{name}", h.requireAuth(h.putResource))
	mux.HandleFunc("DELETE /api/v1/resources/{name}", h.requireAuth(h.deleteResource))
	mux.HandleFunc("GET /api/v1/email-templates", h.requireAuth(h.listEmailTemplates))
	mux.HandleFunc("PUT /api/v1/email-templates/{name}", h.requireAuth(h.putEmailTemplate))
	mux.HandleFunc("DELETE /api/v1/email-templates/{name}", h.requireAuth(h.deleteEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/preview", h.requireAuth(h.previewEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/send-test", h.requireAuth(h.sendTestEmail))

	// Bring-your-own secret manager (OpenBao/Vault): per-tenant connection
	// config behind the same secret-permission gate. Flows then resolve
	// ${vault.PATH#FIELD} against the tenant's own manager.
	mux.HandleFunc("GET /api/v1/secret-manager", h.requireAuth(h.getSecretManager))
	mux.HandleFunc("PUT /api/v1/secret-manager", h.requireAuth(h.putSecretManager))
	mux.HandleFunc("DELETE /api/v1/secret-manager", h.requireAuth(h.deleteSecretManager))
	mux.HandleFunc("GET /api/v1/secret-manager/aws", h.requireAuth(h.getSecretManagerAws))
	mux.HandleFunc("PUT /api/v1/secret-manager/aws", h.requireAuth(h.putSecretManagerAws))
	mux.HandleFunc("DELETE /api/v1/secret-manager/aws", h.requireAuth(h.deleteSecretManagerAws))
	mux.HandleFunc("GET /api/v1/secret-manager/gcp", h.requireAuth(h.getSecretManagerGcp))
	mux.HandleFunc("PUT /api/v1/secret-manager/gcp", h.requireAuth(h.putSecretManagerGcp))
	mux.HandleFunc("DELETE /api/v1/secret-manager/gcp", h.requireAuth(h.deleteSecretManagerGcp))
	mux.HandleFunc("GET /api/v1/oauth/providers", h.requireAuth(h.oauthListProviders))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/accounts", h.requireAuth(h.oauthListAccounts))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/resources", h.requireAuth(h.listAccountResources))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/authorize", h.requireAuth(h.oauthAuthorize))
	// Callback is UNAUTHENTICATED — the OAuth provider redirects the
	// user's browser back here without a Bearer token. State-token
	// validation in the handler is what binds the callback to the
	// original principal.
	mux.HandleFunc("GET /api/v1/oauth/{provider}/callback", h.oauthCallback)
	mux.HandleFunc("GET /api/v1/drops", h.requireAuth(h.listModules))
	// Legacy alias — dzctl and older proxies still hit /modules. Keep
	// it pointing at the same handler so we can deprecate at our pace.
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(h.listModules))

	// Self-describing catalog surface (see daemon/catalog.go +
	// daemon/openapi.yaml). These are additive — they coexist with
	// /api/v1/drops above and will become the canonical surface once
	// the rename PR lands. Discovery + openapi are public so an LLM
	// client can read the spec before it has a token.
	mux.HandleFunc("GET /api/v1", h.serviceDescriptor)
	mux.HandleFunc("GET /api/v1/openapi.json", h.openAPISpec)
	mux.HandleFunc("GET /api/v1/catalog", h.requireAuth(h.catalogSummary))
	mux.HandleFunc("GET /api/v1/catalog/integrations", h.requireAuth(h.listIntegrationsHandler))
	mux.HandleFunc("GET /api/v1/catalog/integrations/{id}", h.requireAuth(h.getIntegrationHandler))
	// Connection verify-before-save (PUT) + re-test of a stored connection
	// (POST verify). secret:write-gated inside the handlers.
	mux.HandleFunc("PUT /api/v1/catalog/integrations/{id}/connection", h.requireAuth(h.putIntegrationConnection))
	mux.HandleFunc("POST /api/v1/catalog/integrations/{id}/verify", h.requireAuth(h.verifyIntegrationConnection))
	mux.HandleFunc("GET /api/v1/catalog/drops", h.requireAuth(h.listDropsHandler))
	mux.HandleFunc("GET /api/v1/catalog/drops/{id}", h.requireAuth(h.getDropHandler))
	mux.HandleFunc("GET /api/v1/catalog/trigger-kinds", h.requireAuth(h.triggerKindsHandler))

	// /me surface. /me is the new alias for /whoami; /me/api-keys is
	// new (self-issue + own-key list/revoke). Unlike /admin/api-keys
	// these don't require organization:admin — any authenticated principal
	// can derive sub-scopes of their own permissions.
	mux.HandleFunc("GET /api/v1/me", h.requireAuth(h.meHandler))
	// /me/totp — the caller manages their own 2FA. Status is readable
	// by anyone signed in; the mutating routes 503 when the install
	// hasn't configured a TOTP key.
	mux.HandleFunc("GET /api/v1/me/totp", h.requireAuth(h.totpStatus))
	mux.HandleFunc("POST /api/v1/me/totp/setup", h.requireAuth(h.totpSetup))
	mux.HandleFunc("POST /api/v1/me/totp/confirm", h.requireAuth(h.totpConfirm))
	mux.HandleFunc("POST /api/v1/me/totp/disable", h.requireAuth(h.totpDisable))
	mux.HandleFunc("POST /api/v1/me/totp/recovery-codes", h.requireAuth(h.totpRegenerate))
	// /me/preferences — the caller's own operational notification
	// settings (e.g. flow-failure email). Always available when password
	// auth is configured; unknown (API-key) principals read defaults.
	mux.HandleFunc("GET /api/v1/me/preferences", h.requireAuth(h.getPreferences))
	mux.HandleFunc("PUT /api/v1/me/preferences", h.requireAuth(h.putPreferences))
	mux.HandleFunc("GET /api/v1/me/api-keys", h.requireAuth(h.listMyAPIKeysHandler))
	mux.HandleFunc("POST /api/v1/me/api-keys",
		h.requireAuth(h.idempotencyMiddleware("/me/api-keys", h.issueMyAPIKeyHandler)))
	mux.HandleFunc("DELETE /api/v1/me/api-keys/{id}", h.requireAuth(h.revokeMyAPIKeyHandler))

	// GDPR data-subject rights. Export (Art. 15/20) downloads a complete
	// machine-readable copy; account deletion (Art. 17) erases the caller's
	// own account (confirmation-guarded). Handlers in gdpr_export.go /
	// gdpr_http.go.
	mux.HandleFunc("GET /api/v1/me/export", h.requireAuth(h.exportHandler))
	mux.HandleFunc("DELETE /api/v1/me/account", h.requireAuth(h.deleteMyAccountHandler))
	// Support feature (see TODO-support-tickets.md): a support agent requests a
	// scoped, read-only grant; an org admin approves/denies/revokes; the agent
	// reads the redacted bundle. All gated + audited into the org's log.
	mux.HandleFunc("POST /api/v1/support/grants", h.requireAuth(h.requestGrant))
	mux.HandleFunc("GET /api/v1/support/grants", h.requireAuth(h.listGrants))
	mux.HandleFunc("GET /api/v1/support/grants/mine", h.requireAuth(h.listMyGrants))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/decide", h.requireAuth(h.decideGrant))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/revoke", h.requireAuth(h.revokeGrant))
	mux.HandleFunc("GET /api/v1/support/flows/{tenant}/{workspace}/{flow_id}", h.requireAuth(h.supportView))
	// Self-service rectification (Art. 16): change own password / email.
	mux.HandleFunc("POST /api/v1/me/password", h.requireAuth(h.changePasswordHandler))
	mux.HandleFunc("POST /api/v1/me/email", h.requireAuth(h.changeEmailHandler))

	// /me/flows and /me/runs — the new spec-aligned routes. flow_id is
	// a percent-encoded composite of tenant/workspace/id; run_id is
	// the existing jobID verbatim. Handlers in daemon/me_routes.go
	// translate to the legacy graph + job service methods. Mutating
	// routes honor Idempotency-Key for replay-safe retries.
	mux.HandleFunc("GET /api/v1/me/usage", h.requireAuth(h.usageMe))
	mux.HandleFunc("GET /api/v1/me/billing", h.requireAuth(h.billingMe))
	mux.HandleFunc("GET /api/v1/me/plans", h.requireAuth(h.plansMe))
	mux.HandleFunc("POST /api/v1/me/billing/checkout", h.requireAuth(h.billingCheckout))
	mux.HandleFunc("POST /api/v1/me/billing/portal", h.requireAuth(h.billingPortal))
	mux.HandleFunc("GET /api/v1/me/flows", h.requireAuth(h.listFlowsMe))
	// Literal "suggestions" segment outranks the {flow_id} wildcard below
	// in Go's ServeMux precedence, so this need not be ordered carefully.
	mux.HandleFunc("GET /api/v1/me/flows/suggestions", h.requireAuth(h.suggestionsMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}", h.requireAuth(h.loadFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/history", h.requireAuth(h.historyFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/published", h.requireAuth(h.publishedFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/publish",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/publish", h.publishFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/unpublish",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/unpublish", h.unpublishFlowMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/watch", h.requireAuth(h.watchFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/references", h.requireAuth(h.listReferences))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/input-fields", h.requireAuth(h.listInputFields))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/restore",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/restore", h.restoreFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/duplicate",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/duplicate", h.duplicateFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/label",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/label", h.labelRevisionMe)))
	mux.HandleFunc("PUT /api/v1/me/flows/{flow_id}",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}", h.saveFlowMe)))
	mux.HandleFunc("PATCH /api/v1/me/flows/{flow_id}",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}", h.patchFlowMe)))
	mux.HandleFunc("DELETE /api/v1/me/flows/{flow_id}", h.requireAuth(h.deleteFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/enable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/enable", h.enableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/disable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/disable", h.disableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/run",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/run", h.runFlowMe)))
	// Clear one node's persisted per-node state (dedupe cursor / watermark /
	// cache) — the editor's "Reset state" action. See resetNodeStateMe.
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/reset-state",
		h.requireAuth(h.resetNodeStateMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/validate", h.requireAuth(h.validateFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/test-trigger",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/test-trigger", h.testTriggerFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/sample",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/nodes/{node_id}/sample", h.sampleFlowNodeMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/runs", h.requireAuth(h.listFlowRunsMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/enable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/enable", h.enableTriggerMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/disable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/disable", h.disableTriggerMe)))

	mux.HandleFunc("GET /api/v1/me/schedules", h.requireAuth(h.listSchedulesMe))

	// /me/boards — the Results surface. A "board" is a user table in the
	// workspace's Collections store; these read it back (read-only) and clear
	// it. Scoped to (tenant, workspace) in the handler, mirroring /me/flows.
	mux.HandleFunc("GET /api/v1/me/boards", h.requireAuth(h.listBoardsMe))
	mux.HandleFunc("GET /api/v1/me/boards/{name}", h.requireAuth(h.getBoardMe))
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}", h.requireAuth(h.clearBoardMe))
	// Delete a single row by its rowid (the handle getBoardMe returns per row).
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}/rows/{rowid}", h.requireAuth(h.deleteBoardRowMe))

	// Git credentials: named, per-org auth bundles (SSH key and/or HTTPS
	// PAT) a git_checkout node picks by `account` — the OAuth-account
	// pattern, for raw git credentials.
	mux.HandleFunc("GET /api/v1/git/credentials", h.requireAuth(h.listGitCredsMe))
	mux.HandleFunc("PUT /api/v1/git/credentials/{account}", h.requireAuth(h.putGitCredMe))
	mux.HandleFunc("DELETE /api/v1/git/credentials/{account}", h.requireAuth(h.deleteGitCredMe))

	mux.HandleFunc("GET /api/v1/me/runs", h.requireAuth(h.listRunsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}", h.requireAuth(h.getRunMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes", h.requireAuth(h.listRunNodesMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/logs", h.requireAuth(h.listRunLogsMe))
	mux.HandleFunc("DELETE /api/v1/me/runs/{run_id}/logs", h.requireAuth(h.deleteRunLogsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes/{node_id}", h.requireAuth(h.getRunNodeMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/events", h.requireAuth(h.runEventsMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/cancel",
		h.requireAuth(h.idempotencyMiddleware("/me/runs/{run_id}/cancel", h.cancelRunMe)))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/resume", h.requireAuth(h.resumeRunMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/retry",
		h.requireAuth(h.idempotencyMiddleware("/me/runs/{run_id}/retry", h.retryRunMe)))

	// /me/share — the per-workspace public overview (TV-dashboard) link.
	// GET reads the current link, POST mints/rotates it, DELETE revokes it.
	// The public read of the snapshot is the unauthenticated route below.
	mux.HandleFunc("GET /api/v1/me/share", h.requireAuth(h.getShareMe))
	mux.HandleFunc("POST /api/v1/me/share", h.requireAuth(h.createShareMe))
	mux.HandleFunc("DELETE /api/v1/me/share", h.requireAuth(h.deleteShareMe))

	// /me/connections — LLM-friendly OAuth surface. List returns
	// provider catalog + which accounts the caller has linked. Authorize
	// returns JSON {authorize_url} (no 302) so an MCP/CLI client can
	// hand the URL to the user. Callback path stays the same.
	mux.HandleFunc("GET /api/v1/me/connections", h.requireAuth(h.listConnectionsMe))
	mux.HandleFunc("POST /api/v1/me/connections/{provider}/authorize",
		h.requireAuth(h.idempotencyMiddleware("/me/connections/{provider}/authorize", h.startConnectionMe)))
	mux.HandleFunc("DELETE /api/v1/me/connections/{provider}", h.requireAuth(h.disconnectConnectionMe))
	mux.HandleFunc("POST /api/v1/validate/cron", h.requireAuth(h.validateCron))
	// Live preview for the render_template step's editor — renders {template,
	// data} through the same engine the drop uses. See render_preview.go.
	mux.HandleFunc("POST /api/v1/tools/render-template/preview", h.requireAuth(h.renderTemplatePreview))
	// Live preview for the render_text step's editor — renders sample rows
	// through the same engine the drop uses. See render_text_preview.go.
	mux.HandleFunc("POST /api/v1/tools/render-text/preview", h.requireAuth(h.renderTextPreview))
	// The Expression drop's formula linter — compiles a CEL expression and
	// returns any problem inline. See validateExpression.
	mux.HandleFunc("POST /api/v1/tools/expression/validate", h.requireAuth(h.validateExpression))
	// AI assist: plain-English description → HTML email template, using the
	// tenant's connected Claude/ChatGPT key. See render_assist.go.
	mux.HandleFunc("POST /api/v1/tools/render-template/assist", h.requireAuth(h.renderTemplateAssist))
	mux.HandleFunc("GET /api/v1/tools/llm-providers", h.requireAuth(h.renderTemplateLLMProviders))
	// Flagship: describe a flow in plain English → a DRAFT flow graph
	// (grounded on the catalog, structured output, validate-and-repair).
	// Never saves or runs — the editor opens it for review. See flowgen.go.
	mux.HandleFunc("POST /api/v1/tools/flow/generate", h.requireAuth(h.renderFlowGenerate))
	mux.HandleFunc("POST /api/v1/tools/flow/generate/stream", h.requireAuth(h.renderFlowGenerateStream))
	// validate/graph lints a Graph JSON literal without saving — for
	// LLMs that compose a graph in chat and want a dry-run before
	// committing. Distinct from /me/flows/{id}/validate which lints
	// the saved HEAD.
	mux.HandleFunc("POST /api/v1/validate/graph", h.requireAuth(h.validateGraphLiteral))
	// Slack Events API endpoint. NOT under requireAuth — Slack POSTs
	// as a stranger; the HMAC signature is the auth.
	mux.HandleFunc("POST /api/v1/events/slack/{tenant}", h.rateLimitWebhook(h.slackEvents))
	mux.HandleFunc("POST /api/v1/events/github/{tenant}", h.rateLimitWebhook(h.githubEvents))
	mux.HandleFunc("POST /api/v1/events/stripe", h.rateLimitWebhook(h.stripeEvents))
	mux.HandleFunc("POST /api/v1/events/stripe/{tenant}", h.rateLimitWebhook(h.stripeTenantEvents))
	mux.HandleFunc("GET /api/v1/approvals/pending", h.requireAuth(h.listPendingApprovals))
	mux.HandleFunc("POST /api/v1/approvals/{runID}/{nodeID}", h.requireAuth(h.approveAuthed))
	mux.HandleFunc("GET /api/v1/admin/api-keys", h.requireAuth(h.listAPIKeys))
	mux.HandleFunc("POST /api/v1/admin/api-keys", h.requireAuth(h.issueAPIKey))
	mux.HandleFunc("DELETE /api/v1/admin/api-keys/{id}", h.requireAuth(h.revokeAPIKey))
	mux.HandleFunc("GET /api/v1/admin/users", h.requireAuth(h.listUsers))
	mux.HandleFunc("GET /api/v1/admin/tenants", h.requireAuth(h.listTenants))
	mux.HandleFunc("GET /api/v1/admin/audit", h.requireAuth(h.listAudit))
	mux.HandleFunc("GET /api/v1/admin/limits", h.requireAuth(h.workspaceLimits))
	// Version self-check for the System section of the admin page: compares
	// the running build against the newest upstream release tag. Platform
	// admins only (the answer is instance-wide, not per-org).
	mux.HandleFunc("GET /api/v1/admin/version", h.requireAuth(h.adminVersion))
	// Live tail of the daemon's own log stream as SSE — the System-section
	// "System log" viewer. platform:admin only; see admin_systemlog.go.
	mux.HandleFunc("GET /api/v1/admin/system/log", h.requireAuth(h.systemLogTail))
	// OAuth provider configuration: paste client_id + client_secret in
	// the admin UI instead of DAZYFLOW_OAUTH_*_CLIENT_ID env vars + a
	// restart. Persisted creds win over env on the next boot.
	mux.HandleFunc("GET /api/v1/admin/oauth-providers", h.requireAuth(h.listAdminOAuthProviders))
	mux.HandleFunc("PUT /api/v1/admin/oauth-providers/{name}", h.requireAuth(h.upsertAdminOAuthProvider))
	mux.HandleFunc("DELETE /api/v1/admin/oauth-providers/{name}", h.requireAuth(h.deleteAdminOAuthProvider))
	// Multi-org membership: a user can belong to many tenants (the
	// "home" tenant minted at signup + any they've been invited to).
	// switch-org re-issues the session against a different tenant the
	// caller has membership in. Memberships listing falls out of
	// whoami; no separate GET is needed.
	mux.HandleFunc("POST /api/v1/auth/switch-org", h.requireAuth(h.switchOrg))
	// Self-serve organization creation: any verified user can mint a new org
	// (they become its admin). Distinct from invitations, which add people to
	// an existing org.
	mux.HandleFunc("POST /api/v1/me/orgs", h.requireAuth(h.createOrg))

	// Invitations: admin creates / lists / revokes pending invites.
	// The accept side is split: GET /api/v1/invitations/{token} is
	// the public detail endpoint (no auth — the token IS the
	// credential at that step), and POST .../accept requires the
	// caller to be signed in so we know which user to bind the new
	// membership to.
	mux.HandleFunc("POST /api/v1/admin/invitations", h.requireAuth(h.createInvitation))
	mux.HandleFunc("GET /api/v1/admin/invitations", h.requireAuth(h.listInvitations))
	mux.HandleFunc("DELETE /api/v1/admin/invitations/{token}", h.requireAuth(h.revokeInvitation))
	// Platform signup-invites: a platform owner invites a specific email
	// to create its own account on a signup-disabled deployment. Distinct
	// from org invitations above — the recipient gets their own tenant,
	// not a membership. The token is consumed by the signUp gate, not an
	// accept endpoint. See signup_invite.go.
	mux.HandleFunc("POST /api/v1/admin/signup-invites", h.requireAuth(h.createSignupInvite))
	mux.HandleFunc("GET /api/v1/admin/signup-invites", h.requireAuth(h.listSignupInvites))
	mux.HandleFunc("DELETE /api/v1/admin/signup-invites/{token}", h.requireAuth(h.revokeSignupInvite))
	// Platform-admin SMTP smoke test — send one message through the
	// transactional Mailer to confirm it actually delivers. See admin_smtp.go.
	mux.HandleFunc("POST /api/v1/admin/smtp-test", h.requireAuth(h.smtpTest))
	mux.HandleFunc("GET /api/v1/admin/members", h.requireAuth(h.listMembers))
	mux.HandleFunc("PATCH /api/v1/admin/members/{email}", h.requireAuth(h.updateMemberRoles))
	mux.HandleFunc("DELETE /api/v1/admin/members/{email}", h.requireAuth(h.removeMember))
	// GDPR erasure (Art. 17): erase a whole account (platform admin) or
	// delete an entire org/tenant (platform admin, or org admin of that
	// tenant). Both confirmation-guarded; see gdpr_http.go.
	mux.HandleFunc("DELETE /api/v1/admin/users/{email}", h.requireAuth(h.adminDeleteUserHandler))
	mux.HandleFunc("GET /api/v1/admin/orgs/{tenant}/export", h.requireAuth(h.exportOrgHandler))
	mux.HandleFunc("DELETE /api/v1/admin/orgs/{tenant}", h.requireAuth(h.adminDeleteOrgHandler))

	// Platform-admin moderation surface (platform:admin only; see
	// admin_platform.go). User/org suspend-ban-list, and the drop
	// killswitch. Delete reuses the GDPR erase routes above.
	mux.HandleFunc("GET /api/v1/admin/platform/users", h.requireAuth(h.platformListUsers))
	mux.HandleFunc("GET /api/v1/admin/platform/users/{email}", h.requireAuth(h.platformGetUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/suspend", h.requireAuth(h.platformSuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/unsuspend", h.requireAuth(h.platformUnsuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/verify", h.requireAuth(h.platformVerifyUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/ban", h.requireAuth(h.platformBanUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(h.platformGrantAdmin))
	mux.HandleFunc("DELETE /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(h.platformRevokeAdmin))
	// Support-agent management (cross-tenant vendor staff → support:agent role).
	mux.HandleFunc("GET /api/v1/admin/platform/support-agents", h.requireAuth(h.listSupportAgents))
	mux.HandleFunc("POST /api/v1/admin/platform/support-agents", h.requireAuth(h.grantSupportAgent))
	mux.HandleFunc("DELETE /api/v1/admin/platform/support-agents/{email}", h.requireAuth(h.revokeSupportAgent))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs", h.requireAuth(h.platformListOrgs))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}", h.requireAuth(h.platformGetOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/suspend", h.requireAuth(h.platformSuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/unsuspend", h.requireAuth(h.platformUnsuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/ban", h.requireAuth(h.platformBanOrg))
	mux.HandleFunc("GET /api/v1/admin/platform/drops", h.requireAuth(h.platformListDrops))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/disable", h.requireAuth(h.platformDisableDrop))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/enable", h.requireAuth(h.platformEnableDrop))
	// Tiers (reusable limit bundles) + per-org entitlement (tier + plan
	// grant + limit overrides) + cross-tenant member invite.
	mux.HandleFunc("GET /api/v1/admin/platform/tiers", h.requireAuth(h.platformListTiers))
	mux.HandleFunc("POST /api/v1/admin/platform/tiers", h.requireAuth(h.platformPutTier))
	mux.HandleFunc("PUT /api/v1/admin/platform/tiers/{id}", h.requireAuth(h.platformPutTier))
	mux.HandleFunc("DELETE /api/v1/admin/platform/tiers/{id}", h.requireAuth(h.platformDeleteTier))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(h.platformGetEntitlement))
	mux.HandleFunc("PUT /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(h.platformPutEntitlement))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/invite", h.requireAuth(h.platformInviteMember))

	// Unauthenticated, DB-touching public reads get the per-IP webhook limiter
	// so a stranger can't flood them into Postgres/connection-pool pressure.
	mux.HandleFunc("GET /api/v1/invitations/{token}", h.rateLimitWebhook(h.viewInvitation))
	mux.HandleFunc("POST /api/v1/invitations/{token}/accept", h.requireAuth(h.acceptInvitation))

	// Per-org SSO settings + Google sign-in round-trip.
	mux.HandleFunc("GET /api/v1/admin/org/auth-config", h.requireAuth(h.getOrgAuthConfig))
	mux.HandleFunc("PUT /api/v1/admin/org/auth-config", h.requireAuth(h.putOrgAuthConfig))
	mux.HandleFunc("DELETE /api/v1/admin/org/auth-config", h.requireAuth(h.deleteOrgAuthConfig))
	mux.HandleFunc("GET /api/v1/auth/sso/{tenant}", h.rateLimitWebhook(h.getPublicSSOStatus))
	mux.HandleFunc("GET /api/v1/auth/config", h.rateLimitWebhook(h.getPublicAuthConfig))
	// Public (pre-auth): map a wildcard host label to a tenant for sign-in.
	mux.HandleFunc("GET /api/v1/auth/resolve-subdomain", h.rateLimitWebhook(h.resolveSubdomain))
	// Public (pre-auth): Caddy on-demand-TLS authorization for org subdomains.
	mux.HandleFunc("GET /api/v1/auth/tls-allow", h.rateLimitWebhook(h.tlsAllow))
	mux.HandleFunc("GET /api/v1/admin/org/profile", h.requireAuth(h.getOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/profile", h.requireAuth(h.putOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/subdomain", h.requireAuth(h.putOrgSubdomain))
	mux.HandleFunc("GET /api/v1/admin/org/subdomain/available", h.requireAuth(h.orgSubdomainAvailable))
	mux.HandleFunc("GET /api/v1/auth/google/start", h.rateLimitWebhook(h.googleSignInStart))
	mux.HandleFunc("GET /api/v1/auth/google/callback", h.rateLimitWebhook(h.googleSignInCallback))
	// One-time handoff: the apex SSO callback bounces the session to the
	// org subdomain here so the cookie is set host-only on that origin
	// (per-org subdomains, Option B). Unauthenticated — the one-time code
	// in the query is the credential.
	mux.HandleFunc("GET /api/v1/auth/handoff", h.rateLimitWebhook(h.authHandoff))

	// Webhook trigger + hosted-form endpoints. Authenticated per-graph
	// (per-trigger bearer secret for /trigger; opt-in public_form for
	// /form) rather than via the daemon's API-key chain, so they sit
	// outside requireAuth. Mounting on the main HTTP gateway means a
	// default --http-only deploy serves these routes without the
	// operator having to spin up the optional standalone --webhook
	// listener too. cmd/dzd's --webhook flag still binds a separate
	// listener for operators who want port separation.
	//
	// Methods are listed explicitly because Go 1.22's ServeMux rejects
	// a method-any pattern that coexists with the GET / catch-all
	// (conflict: GET / matches fewer methods but a more general path).
	// The standalone WebhookListener registers method-any patterns
	// because it has no GET / catch-all to collide with; this mux does.
	if h.Webhook == nil {
		h.Webhook = NewWebhookListener(h.svc)
	}
	mux.HandleFunc("POST /trigger/", h.rateLimitWebhook(h.Webhook.handleTrigger))
	// /form/ is unauthenticated (public forms submit real runs), so it needs the
	// same per-IP throttle as /trigger/ to bound a flood of submissions.
	mux.HandleFunc("GET /form/", h.rateLimitWebhook(h.Webhook.handleForm))
	mux.HandleFunc("POST /form/", h.rateLimitWebhook(h.Webhook.handleForm))

	// Public workspace-overview snapshot for the TV-dashboard page. The
	// {token} in the path is the only credential — no requireAuth — so it's
	// throttled by the same per-IP webhook limiter as the other anonymous
	// surfaces. The token resolves to a (tenant, workspace) and returns a
	// sanitized, sensitive-data-free status snapshot.
	mux.HandleFunc("GET /api/v1/public/overview/{token}", h.rateLimitWebhook(h.publicOverview))

	// HMAC-verified approval endpoint for email/Slack approvers. The
	// token in the URL is the auth here, so no requireAuth wrapper —
	// the route 404s when Approval is nil (i.e. the operator hasn't
	// configured DAZYFLOW_APPROVAL_HMAC_SECRET + PUBLIC_BASE_URL).
	if h.Approval != nil {
		mux.HandleFunc("POST /approve/", h.Approval.handle)
	}

	// Static frontend bundle. Registered LAST so all explicit API
	// routes above match first; `GET /` is a catch-all for any
	// unknown GET path. Empty WebDist = no static serving.
	if h.WebDist != "" {
		if h.LandingDir != "" {
			mux.Handle("GET /", h.landingDistHandler(h.LandingDir, h.WebDist))
		} else {
			mux.Handle("GET /", webDistHandler(h.WebDist))
		}
	}
}

// landingDistHandler serves an optional static marketing site
// (landingDir) alongside the SPA (webDir) on the same origin. The
// root is auth-gated — a signed-in browser gets the app shell, an
// anonymous visitor gets the landing page — so a logged-out user who
// would previously have hit the SPA's sign-in screen at / now lands on
// marketing copy instead, while signed-in users keep their dashboard
// at /. Marketing pages and assets that resolve to a real file under
// landingDir (/style.css, /pricing, /privacy, /terms, /shots/*, …)
// serve publicly; everything else (the SPA's own assets and
// client-side routes) falls through to the SPA handler.
func (h *HTTPGateway) landingDistHandler(landingDir, webDir string) http.Handler {
	spa := webDistHandler(webDir)
	landingFS := http.FileServer(http.Dir(landingDir))
	landingIndex := filepath.Join(landingDir, "landing.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// /api/* never falls through to static; defense in depth in
		// case a route was missed in mountRoutes.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		// Root is auth-gated: the session cookie rides along on a
		// top-level navigation (SameSite=Lax), so a valid one means a
		// signed-in user who wants the app, not the marketing page.
		//
		// Marketing lives ONLY on the apex. On an org subdomain
		// (klahr.dazyflow.app) the org's app is the front door, so an
		// anonymous visitor gets the SPA — which routes them to sign-in with
		// their org preselected — instead of the apex marketing page.
		if r.URL.Path == "/" {
			if h.hasValidSession(r) || h.isOrgSubdomainHost(r.Host) {
				spa.ServeHTTP(rw, r)
				return
			}
			http.ServeFile(rw, r, landingIndex)
			return
		}
		// Public marketing pages/assets: anything that resolves to a
		// real file under landingDir. The SPA's routes and hashed
		// assets don't exist there, so they fall through below.
		if landingHas(landingDir, r.URL.Path) {
			landingFS.ServeHTTP(rw, r)
			return
		}
		spa.ServeHTTP(rw, r)
	})
}

// landingHas reports whether urlPath resolves to a servable file under
// dir — either a regular file, or a directory holding an index.html
// (so /pricing maps to pricing/index.html, matching the FileServer's
// directory-index behaviour). Used to decide landing-vs-SPA dispatch.
func landingHas(dir, urlPath string) bool {
	clean := filepath.Clean(urlPath)
	if clean == "/" || strings.HasPrefix(clean, "..") {
		return false
	}
	p := filepath.Join(dir, clean)
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	idx, err := os.Stat(filepath.Join(p, "index.html"))
	return err == nil && !idx.IsDir()
}

// hasValidSession reports whether the request carries a credential
// (session cookie or Bearer token) that authenticates successfully.
// Used to gate the marketing landing at / — it must not error the
// request, only classify it.
func (h *HTTPGateway) hasValidSession(r *http.Request) bool {
	token := credentialFromRequest(r)
	if token == "" {
		return false
	}
	_, err := h.svc.Authenticate(r.Context(), token)
	return err == nil
}

// webDistHandler serves files from root with SPA fallback to
// index.html for any path that doesn't resolve to an actual file —
// matches what `nginx try_files $uri /index.html` does. The fallback
// is the load-bearing piece for client-side routing (React Router's
// /flows/foo and /runs/bar paths aren't real files on disk).
//
// We intentionally do NOT serve under /api/* — those paths belong to
// the API and an unregistered /api/something should 404 as an API
// error, not return the index.html (which would mislead a client
// that's looking for JSON).
func webDistHandler(root string) http.Handler {
	fileServer := http.FileServer(http.Dir(root))
	indexPath := filepath.Join(root, "index.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// /api/* never falls through to static; defense in depth in
		// case a route was missed in mountRoutes.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		// filepath.Clean + Join already strip "..", but the request
		// path is URL-formatted; FileServer handles the full
		// translation when we hand off below. Here we only need the
		// existence check.
		clean := filepath.Clean(r.URL.Path)
		p := filepath.Join(root, clean)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(rw, r)
			return
		}
		// File doesn't exist. If the path looks like an asset
		// request (has a file extension), 404 so a missing JS/CSS
		// surfaces as a build-broken error rather than masquerading
		// as HTML. Path-style requests (no extension, e.g. /flows,
		// /runs/abc) fall through to index.html so React Router
		// resolves them client-side.
		if ext := filepath.Ext(clean); ext != "" && ext != "." {
			http.NotFound(rw, r)
			return
		}
		http.ServeFile(rw, r, indexPath)
	})
}

// ServeForTest exposes the mux without binding a port — analogous to
// ServeWebhookForTest. Tests build a Gateway, attach it to httptest.
func ServeForTest(h *HTTPGateway, rw http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	h.withCORSAndLogging(h.verifyCookieOrigin(h.limitRequestBody(jsonErrors(mux)))).ServeHTTP(rw, r)
}

// verifyCookieOrigin is the CSRF defense layer for cookie-authenticated
// requests. The session cookie already has SameSite=Lax + HttpOnly, which
// in modern browsers blocks the classical fetch-driven CSRF attack on its
// own. This middleware adds belt-and-suspenders defense for older browsers
// and for the small set of fetch shapes Lax doesn't cover (e.g. some
// top-level POST navigations in older Safari): every cookie-auth POST /
// PUT / PATCH / DELETE must carry an Origin header that matches one of
// the configured AllowedOrigins.
//
// Behaviour:
//   - GET / HEAD / OPTIONS pass through (they shouldn't mutate state; the
//     CORS preflight needs to land first anyway).
//   - Requests with no session cookie pass through (Bearer-auth clients
//     have no cookies attached, so there's no CSRF surface).
//   - Cookie-auth state-changing requests must have an Origin header
//     present and matching AllowedOrigins. Missing or mismatched Origin
//     returns 403.
//
// When AllowedOrigins is empty (single-tenant dev mode without web-origin
// configured), cookie-auth state-changing requests fall back to refusing —
// "no allowed origins" implies no browser-served origin should be
// performing writes; the deployment hasn't opted into browser auth.
func (h *HTTPGateway) verifyCookieOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(rw, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			// No session cookie → not a CSRF target.
			next.ServeHTTP(rw, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			writeJSONError(rw, http.StatusForbidden,
				"cookie-authenticated state-changing requests require an Origin header (CSRF defense)")
			return
		}
		if !h.originAllowed(origin) {
			h.logger.Printf("CSRF reject: Origin=%q not in allowed=%v wildcard=%q (host=%q)", origin, h.AllowedOrigins, h.WildcardDomain, r.Host)
			writeJSONError(rw, http.StatusForbidden,
				fmt.Sprintf("cookie-authenticated request from disallowed origin %q (CSRF defense)", origin))
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (h *HTTPGateway) requireAuth(next func(rw http.ResponseWriter, r *http.Request, p core.Principal)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		token := credentialFromRequest(r)
		if token == "" {
			writeJSONError(rw, http.StatusUnauthorized, "missing Authorization: Bearer <token> or session cookie")
			return
		}
		p, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			// A valid credential behind a suspended user/org is a lockout,
			// not an identity failure — answer 403 so the web client can
			// show "your account is suspended" rather than bouncing to
			// sign-in (where the credential would just be rejected again).
			if errors.Is(err, auth.ErrAccountSuspended) {
				writeJSONError(rw, http.StatusForbidden, "account suspended")
				return
			}
			writeJSONError(rw, http.StatusUnauthorized, fmt.Sprintf("auth: %v", err))
			return
		}
		// Slide the session forward on activity so an active user isn't
		// bounced at the idle-TTL boundary. Must run before next() writes
		// the response body, since it sets a Set-Cookie header.
		h.maybeRenewSession(rw, r, token)
		next(rw, r, p)
	}
}

const sessionCookieName = "dazyflow_session"

// sessionTTL is the sliding idle window, defaulting to 7d when SessionTTL
// is unset (or non-positive). Centralizes the `ttl := h.SessionTTL; if ttl
// <= 0 { ttl = … }` default repeated at every session-issue site and in
// maybeRenewSession.
func (h *HTTPGateway) sessionTTL() time.Duration {
	if h.SessionTTL <= 0 {
		return 7 * 24 * time.Hour
	}
	return h.SessionTTL
}

// maxSessionAge is the absolute ceiling a session can reach from CreatedAt,
// defaulting to 30d when unset. A non-positive MaxSessionAge keeps the
// default rather than disabling the cap, so the gateway always has a
// backstop even if the daemon forgets to wire it; an operator who truly
// wants unbounded sliding sets it explicitly to a very large value.
func (h *HTTPGateway) maxSessionAge() time.Duration {
	if h.MaxSessionAge <= 0 {
		return 30 * 24 * time.Hour
	}
	return h.MaxSessionAge
}

// maybeRenewSession slides a cookie-backed session's expiry forward so an
// active user isn't logged out at the idle-TTL boundary. It runs after a
// successful authentication on every request, but only writes the store
// (and re-issues the cookie) once the session has passed its renewal
// threshold — see auth.NextSessionExpiry — so steady traffic stays
// write-free. Bearer callers manage their own credential lifetime and are
// skipped; only the browser cookie needs its Expires refreshed to match
// the slid server-side expiry. Best-effort: any error leaves the existing
// (still-valid) session untouched.
func (h *HTTPGateway) maybeRenewSession(rw http.ResponseWriter, r *http.Request, token string) {
	if h.Sessions == nil || !strings.HasPrefix(token, auth.SessionTokenPrefix) {
		return
	}
	// Only refresh the cookie for cookie-authenticated requests; a bearer
	// session token has no cookie to update.
	if c, err := r.Cookie(sessionCookieName); err != nil || c.Value != token {
		return
	}
	key := auth.SessionLookupKey(token)
	sess, err := h.Sessions.GetSession(r.Context(), key)
	if err != nil {
		return
	}
	next, renew := auth.NextSessionExpiry(sess, h.sessionTTL(), h.maxSessionAge(), time.Now())
	if !renew {
		return
	}
	sess.ExpiresAt = next
	if err := h.Sessions.PutSession(r.Context(), sess); err != nil {
		h.logger.Printf("session renew: %v", err)
		return
	}
	h.setSessionCookie(rw, r, token, next)
}

// setSessionCookie installs the host-only session cookie for token, expiring
// at expires. The Secure flag tracks whether the request reached us over TLS
// (requestIsHTTPS, which also honors a trusted X-Forwarded-Proto). Single
// source for every cookie-issuing sign-in path (password, SSO, handoff).
func (h *HTTPGateway) setSessionCookie(rw http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
}

// clearSessionCookie expires the session cookie (sign-out). Mirrors
// setSessionCookie's attributes so the browser matches and drops it.
func (h *HTTPGateway) clearSessionCookie(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
}

// credentialFromRequest extracts a bearer credential from either the
// Authorization header (preferred, used by dzctl and API-key clients)
// or the session cookie set by /auth/signin (used by the browser).
func credentialFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if token != "" {
			return token
		}
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func (h *HTTPGateway) withCORSAndLogging(next http.Handler) http.Handler {
	allowed := "*"
	allowCreds := false
	if len(h.AllowedOrigins) > 0 || h.WildcardDomain != "" {
		allowed = strings.Join(h.AllowedOrigins, ", ")
		// Cookie-based sessions require an explicit origin and
		// Access-Control-Allow-Credentials: true. Wildcard "*" is
		// incompatible with credentials per the CORS spec. With only a
		// WildcardDomain configured, AllowedOrigins may be empty — the
		// per-request originAllowed check below still reflects the exact
		// origin back, so credentials work; the static `allowed` fallback
		// just stays empty for non-matching origins.
		allowCreds = true
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// Wrap the writer to capture the status code, and time the
		// request for the RED metrics. The recorder delegates Flush so
		// SSE streams are unaffected.
		if h.Metrics != nil {
			rec := &statusRecorder{ResponseWriter: rw}
			rw = rec
			start := time.Now()
			defer func() {
				h.Metrics.ObserveHTTP(r.Method, rec.statusCode(), time.Since(start).Seconds())
			}()
		}
		origin := r.Header.Get("Origin")
		if allowCreds && origin != "" && h.originAllowed(origin) {
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Access-Control-Allow-Credentials", "true")
			rw.Header().Set("Vary", "Origin")
		} else {
			rw.Header().Set("Access-Control-Allow-Origin", allowed)
		}
		rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		rw.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		// HSTS only over HTTPS — sending it on a plain-HTTP response is
		// pointless (browsers ignore it) and on a mixed setup could
		// wedge a not-yet-TLS host. 1 year, includeSubDomains.
		if h.requestIsHTTPS(r) {
			rw.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// Conservative content-type hardening for the API surface.
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		// Clickjacking + referrer hardening for the authenticated app surface.
		// The /form/ surface is deliberately embeddable (it sets its own
		// permissive CSP), so don't frame-deny it.
		if !strings.HasPrefix(r.URL.Path, "/form/") {
			rw.Header().Set("X-Frame-Options", "DENY")
			rw.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		}
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// requestIsHTTPS reports whether the request reached the user over TLS.
// Directly: r.TLS is set. Behind a TLS-terminating reverse proxy the
// connection to dzd is plain HTTP, so we consult X-Forwarded-Proto —
// but only when TrustProxyHeaders is on, since an untrusted client
// could otherwise forge it to flip on the Secure cookie flag.
func (h *HTTPGateway) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if h.TrustProxyHeaders && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// originAllowed reports whether a browser Origin header is trusted for
// CORS + CSRF. An origin is allowed if it exactly matches one of the
// configured AllowedOrigins, or — when WildcardDomain is set — if it is
// a subdomain of that domain (e.g. "https://acme.dazyflow.app" against
// WildcardDomain "dazyflow.app"). The Origin header is set by the
// browser and not forgeable by page script, so suffix-matching the host
// is safe; "evil-dazyflow.app" doesn't end in ".dazyflow.app" so it
// won't match. The apex itself ("https://dazyflow.app") is intentionally
// NOT matched here — it carries a scheme/port we want pinned, so it must
// be listed explicitly in AllowedOrigins like any other exact origin.
func (h *HTTPGateway) originAllowed(origin string) bool {
	for _, a := range h.AllowedOrigins {
		if a == origin {
			return true
		}
	}
	// Only honour the wildcard if it's specific enough to be safe. A
	// single-label value like "com" would suffix-match every ".com" origin —
	// catastrophic — so a misconfigured domain trusts nobody rather than
	// everybody. cmd/dzd also rejects such a value at boot (fail-loud); this
	// is the defense-in-depth backstop on the request path.
	if IsValidWildcardDomain(h.WildcardDomain) {
		if u, err := url.Parse(origin); err == nil {
			if hostIsSubdomainOf(u.Hostname(), h.WildcardDomain) {
				return true
			}
		}
	}
	return false
}

// hostIsSubdomainOf reports whether host is a (single- or multi-level)
// subdomain of domain. Both are compared case-insensitively. The apex
// (host == domain) returns false — only strict subdomains match.
func hostIsSubdomainOf(host, domain string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domain = strings.ToLower(domain)
	return domain != "" && strings.HasSuffix(host, "."+domain)
}

// isOrgSubdomainHost reports whether an inbound request's Host is a per-org
// wildcard subdomain (anything under the configured apex), so the landing
// handler can serve the app rather than the marketing page there. The port,
// if any, is stripped first; false when the wildcard feature is off.
func (h *HTTPGateway) isOrgSubdomainHost(host string) bool {
	if h.WildcardDomain == "" {
		return false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return hostIsSubdomainOf(host, h.WildcardDomain)
}

// IsValidWildcardDomain reports whether d is specific enough to use as a
// CORS/CSRF subdomain-suffix match. It requires at least two non-empty labels
// (one dot), so "dazyflow.app" is accepted but a bare public suffix like "com"
// — which would trust every ".com" origin — is rejected. This is a coarse
// label-count guard, not a public-suffix-list check: a value like "co.uk"
// passes the count but is still a registry suffix, so operators must not set
// one. Empty d (wildcard disabled) returns false.
func IsValidWildcardDomain(d string) bool {
	d = strings.Trim(strings.TrimSpace(strings.ToLower(d)), ".")
	if d == "" {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	return true
}

// --- Handlers ---------------------------------------------------------

// signIn validates an email+password pair, mints a session, and sets
// the session cookie. The session token is also returned in the body so
// non-browser clients can hand it back via Authorization: Bearer.
func (h *HTTPGateway) signIn(rw http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "password sign-in not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}](rw, r)
	if !ok {
		return
	}
	user, err := auth.VerifyPassword(r.Context(), h.Users, body.Email, body.Password)
	if err != nil {
		// Tenant is left empty: resolving it would reveal whether the
		// email maps to an account, which the uniform error above
		// deliberately hides.
		h.auditAuth(r.Context(), r, "", strings.ToLower(strings.TrimSpace(body.Email)), "auth.signin_failed", "method=password")
		writeJSONError(rw, http.StatusUnauthorized, "invalid email or password")
		return
	}
	// Locked out? A suspended user — or a member of a suspended org — has a
	// valid password but no access. Refuse at sign-in with a clear reason
	// rather than issuing a session (or a TOTP challenge) the auth
	// ModerationGate would just reject on the next request.
	if msg, locked := h.signInLockout(r.Context(), user); locked {
		h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.signin_suspended", "method=password")
		writeJSONError(rw, http.StatusForbidden, msg)
		return
	}
	// Second factor: if this user has TOTP enabled (and the install has
	// 2FA configured), the password alone is not enough. Mint a
	// short-lived challenge and return it instead of a session; the
	// client posts it back to /auth/totp with a code to finish. We fail
	// closed — an enrolled user can't downgrade to password-only just
	// because the challenge store is missing.
	if user.TOTPEnabled && h.totpConfigured() {
		challenge, cerr := auth.IssueTOTPChallenge(r.Context(), h.TOTPChallenges, user.Email)
		if cerr != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue challenge: %v", cerr))
			return
		}
		h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.mfa_challenge", "method=password")
		writeJSON(rw, http.StatusOK, map[string]any{
			"totp_required": true,
			"challenge":     challenge,
		})
		return
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevateSessionRoles(r.Context(), user), h.sessionTTL())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signin", "method=password")
	h.setSessionCookie(rw, r, token, sess.ExpiresAt)
	writeJSON(rw, http.StatusOK, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
	})
}

// elevatePlatformAdmin grants the platform:admin role to a user whose
// email is in the PlatformAdmins allowlist. Called at every session-issue
// site (sign-in, signup, SSO) so the allowlist is the single source of
// truth — roles are baked into the session at issue time, so an existing
// session must re-authenticate to pick up an allowlist change. No-op when
// the email isn't listed or the role is already present.
func (h *HTTPGateway) elevatePlatformAdmin(ctx context.Context, u auth.User) auth.User {
	env := h.isPlatformAdminEmail(u.Email)
	if !env && !h.isPlatformAdminGranted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermPlatformAdmin) {
			return u
		}
	}
	// Copy before appending: u.Roles may alias a slice held by the user
	// store, and we must not mutate that shared backing array.
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.PlatformAdminRole())
	// Record the escalation on first apply (per email, per process): a
	// platform-admin grant is a privileged-access event worth a durable audit
	// record (ISO 27001 A.5.16/A.8.2). Emitting only once avoids a per-sign-in
	// flood, since elevation runs at every session issue.
	source := "runtime_grant"
	if env {
		source = "DAZYFLOW_PLATFORM_ADMINS"
	}
	key := strings.ToLower(strings.TrimSpace(u.Email))
	if _, seen := h.platformAdminGranted.LoadOrStore(key, struct{}{}); !seen {
		h.audit(ctx, core.Principal{Tenant: u.Tenant, Subject: u.Email},
			"platform_admin.granted", u.Email, "source="+source)
	}
	return u
}

// isPlatformAdminGranted reports whether email holds a runtime platform-admin
// grant (the mutable layer). Cheap — reads the store's cached snapshot. Nil
// store (not wired) means no runtime grants exist.
func (h *HTTPGateway) isPlatformAdminGranted(email string) bool {
	return h.PlatformAdminGrants != nil && h.PlatformAdminGrants.Granted(email)
}

// elevateSessionRoles applies every session-issue role elevation in one place,
// so the ~5 issue sites (sign-in, signup, SSO, TOTP) call a single chokepoint.
func (h *HTTPGateway) elevateSessionRoles(ctx context.Context, u auth.User) auth.User {
	return h.elevateSupportAgent(ctx, h.elevatePlatformAdmin(ctx, u))
}

// elevateSupportAgent stamps core.SupportAgentRole onto a session whose email
// holds a runtime support-agent grant (there is no env-allowlist layer for
// support). Mirrors elevatePlatformAdmin: baked in at issue time, so a grant
// takes effect on the next session issue and a revoke once live sessions drop.
// No-op when unset or already present. The role itself grants no ambient
// access — it only unlocks requesting an AccessGrant and the support-view
// capability (AuthorizeGraphSupportView).
func (h *HTTPGateway) elevateSupportAgent(ctx context.Context, u auth.User) auth.User {
	if h.SupportAgents == nil || !h.SupportAgents.Granted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermSupportAgent) {
			return u
		}
	}
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.SupportAgentRole())
	// Record the escalation once per email per process (privileged-access event).
	key := strings.ToLower(strings.TrimSpace(u.Email))
	if _, seen := h.supportAgentGranted.LoadOrStore(key, struct{}{}); !seen {
		h.audit(ctx, core.Principal{Tenant: u.Tenant, Subject: u.Email},
			"support_agent.granted", u.Email, "source=runtime_grant")
	}
	return u
}

// isPlatformAdmin reports whether email is a platform admin by EITHER layer —
// the immutable env allowlist or a runtime grant. Used for display/effective
// status; the env-only isPlatformAdminEmail still guards immutability (you
// can't revoke an env admin).
func (h *HTTPGateway) isPlatformAdmin(email string) bool {
	return h.isPlatformAdminEmail(email) || h.isPlatformAdminGranted(email)
}

// isPlatformAdminEmail reports whether email is in the allowlist. The
// stored entries are already lowercased + trimmed at wiring time; we
// normalize the candidate the same way so the comparison is exact.
func (h *HTTPGateway) isPlatformAdminEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, a := range h.PlatformAdmins {
		if a == email {
			return true
		}
	}
	return false
}

// adminBootstrapAvailable reports whether at least one platform-admin
// email in the allowlist has not yet claimed an account. It's the
// signal the sign-up page uses to keep itself reachable on a
// signup-disabled deployment: the backend already lets a listed email
// through signUp (see httpsignup.go), but the page would otherwise
// bounce to /signin because EnableSignup is false, leaving the
// bootstrap hatch with no door. The check is self-limiting in lockstep
// with that hatch — once every listed admin has signed up, GetByEmail
// finds them all and this returns false, so the form disappears again
// and a locked-down instance doesn't expose public signup forever.
//
// Unauthenticated callers reach this via getPublicAuthConfig; it leaks
// only a single boolean, never which emails are listed. The allowlist
// is tiny (typically 1-3), so the per-email lookups are cheap.
func (h *HTTPGateway) adminBootstrapAvailable(ctx context.Context) bool {
	if h.Users == nil || len(h.PlatformAdmins) == 0 {
		return false
	}
	for _, email := range h.PlatformAdmins {
		// Mirror signUp's existence test: a non-nil error or an empty
		// email both mean "not claimed yet". Any unclaimed admin keeps
		// the bootstrap door open.
		if u, err := h.Users.GetByEmail(ctx, email); err != nil || u.Email == "" {
			return true
		}
	}
	return false
}

// signOut deletes the server-side session and clears the cookie. It
// silently no-ops when no session is attached so the browser can hit
// this on logout without inspecting state first.
func (h *HTTPGateway) signOut(rw http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "sessions not configured")
		return
	}
	if token := credentialFromRequest(r); strings.HasPrefix(token, auth.SessionTokenPrefix) {
		key := auth.SessionLookupKey(token)
		// Resolve before deleting so the audit event carries the identity
		// that signed out. Best-effort — an already-gone session just
		// skips the record.
		if sess, err := h.Sessions.GetSession(r.Context(), key); err == nil {
			h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signout", "")
		}
		_ = h.Sessions.DeleteSession(r.Context(), key)
	}
	h.clearSessionCookie(rw, r)
	rw.WriteHeader(http.StatusNoContent)
}

// whoami returns the authenticated principal's identity AND the flat
// set of permissions any of their roles grant. The UI uses this for
// role gating (whether to show the Admin link, the Edit button, etc.)
// without re-implementing role unrolling client-side.
func (h *HTTPGateway) whoami(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	permSet := map[core.Permission]struct{}{}
	for _, role := range p.Roles {
		for _, perm := range role.Permissions {
			permSet[perm] = struct{}{}
		}
	}
	perms := make([]core.Permission, 0, len(permSet))
	for perm := range permSet {
		perms = append(perms, perm)
	}
	// Memberships lets the UI surface an org switcher even for non-
	// platform-admin users. The list always includes the principal's
	// current org (Home=true) so a fresh signup with no extra
	// memberships still sees a single entry and the switcher gracefully
	// hides itself.
	memberships := h.collectMemberships(r.Context(), p)
	emailVerified, verificationPending := h.verificationStatus(r, p)
	writeJSON(rw, http.StatusOK, map[string]any{
		"subject":     p.Subject,
		"tenant":      p.Tenant,
		"workspace":   p.Workspace,
		"roles":       p.Roles,
		"permissions": perms,
		"memberships": memberships,
		// email_verified / verification_pending drive the "confirm your
		// email" banner. pending is false on deployments without a
		// mailer (nothing to verify against) and for API-key callers.
		"email_verified":       emailVerified,
		"verification_pending": verificationPending,
		// public_base_url lets the UI build externally-correct webhook /
		// hosted-form URLs instead of guessing the host. Empty when the
		// operator hasn't set --public-base-url; the UI falls back to a
		// localhost hint in that case.
		"public_base_url": h.svc.PublicBaseURL,
		// support_contact surfaces an operator-set email/URL on UI
		// surfaces that depend on server-side setup the end user can't
		// fix themselves (e.g. OAuth/secret-store not configured on the
		// Connections page). Empty = the UI shows a generic "contact
		// your administrator" message with no link.
		"support_contact": h.svc.SupportContact,
	})
}

// orgMembershipDTO is the wire shape whoami emits per membership. The
// home org always appears with home=true; the others come from the
// MembershipStore. DisplayName is the org's human-facing name (from
// OrgProfile) — empty when the org has no profile yet, in which case
// the UI falls back to the raw Tenant ID.
type orgMembershipDTO struct {
	Tenant      string      `json:"tenant"`
	DisplayName string      `json:"display_name,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Workspace   string      `json:"workspace"`
	Roles       []core.Role `json:"roles"`
	Home        bool        `json:"home"`
}

func (h *HTTPGateway) collectMemberships(ctx context.Context, p core.Principal) []orgMembershipDTO {
	// The home entry is the user's OWN tenant (from the user record), not the
	// session's current tenant — otherwise switching into another org would
	// make the home org follow p.Tenant and drop out of the list (it isn't a
	// membership row). Fall back to p.Tenant for API-key principals, which
	// have no user record and are bound to one tenant.
	homeTenant, homeWorkspace, homeRoles := p.Tenant, p.Workspace, p.Roles
	if h.Users != nil && strings.Contains(p.Subject, "@") {
		if u, err := h.Users.GetByEmail(ctx, p.Subject); err == nil {
			homeTenant, homeWorkspace, homeRoles = u.Tenant, u.Workspace, u.Roles
		}
	}
	out := []orgMembershipDTO{{
		Tenant:    homeTenant,
		Workspace: homeWorkspace,
		Roles:     homeRoles,
		Home:      true,
	}}
	if h.Memberships != nil && p.Subject != "" && strings.Contains(p.Subject, "@") {
		// Only password-auth subjects (email-shaped) have Memberships;
		// API-key principals are bound to one tenant by their key. A
		// silent skip on a non-email subject avoids accidentally exposing
		// memberships keyed by a coincidental UUID match.
		rows, err := h.Memberships.ListByEmail(ctx, p.Subject)
		if err == nil {
			for _, m := range rows {
				if m.Tenant == homeTenant {
					// Already in `out` as the home entry — skip the duplicate.
					continue
				}
				out = append(out, orgMembershipDTO{
					Tenant:    m.Tenant,
					Workspace: m.Workspace,
					Roles:     m.Roles,
					Home:      false,
				})
			}
		}
	}
	// Bulk-resolve display names so the switcher can render pretty
	// labels without an extra round-trip per membership. A missing
	// profile leaves DisplayName empty; the UI falls back to Tenant.
	if h.Profiles != nil && len(out) > 0 {
		tenants := make([]string, 0, len(out))
		for _, m := range out {
			tenants = append(tenants, m.Tenant)
		}
		if profiles, err := h.Profiles.ListOrgProfiles(ctx, tenants); err == nil {
			for i := range out {
				if pr, ok := profiles[out[i].Tenant]; ok {
					out[i].DisplayName = pr.DisplayName
					out[i].Icon = pr.Icon
				}
			}
		}
	}
	return out
}

// getOrgProfile returns the per-org display name + last-edited time.
// Always returns a row (even if the profile hasn't been written yet)
// so the UI can show the current value (empty) and the tenant ID side
// by side without distinguishing "no row" from "blank row".
func (h *HTTPGateway) getOrgProfile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot view another tenant's profile")
		return
	}
	pr, err := h.Profiles.GetOrgProfile(r.Context(), tenant)
	if err != nil {
		// Empty row is the right shape — the UI fills in a default.
		writeJSON(rw, http.StatusOK, map[string]any{
			"tenant":          tenant,
			"display_name":    "",
			"wildcard_domain": h.WildcardDomain,
		})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		"icon":         pr.Icon,
		"subdomain":    pr.Subdomain,
		"updated_at":   pr.UpdatedAt,
		// The apex the subdomain hangs off (e.g. "dazyflow.app"), so the
		// editor renders "<label>.<domain>" and only shows the field when the
		// deploy supports per-org subdomains. Empty = feature off.
		"wildcard_domain": h.WildcardDomain,
	})
}

// maxOrgIconBytes caps the inline org icon (data: URL). Icons are
// downscaled client-side; this is a backstop against an oversized blob
// bloating the profile store.
const maxOrgIconBytes = 256 * 1024

func (h *HTTPGateway) putOrgProfile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	body, ok := decodeRequestJSON[struct {
		DisplayName string `json:"display_name"`
		Icon        string `json:"icon"`
	}](rw, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if len(name) > 80 {
		writeJSONError(rw, http.StatusBadRequest, "display name is too long (max 80)")
		return
	}
	if len(body.Icon) > maxOrgIconBytes {
		writeJSONError(rw, http.StatusBadRequest, "icon is too large")
		return
	}
	pr := auth.OrgProfile{
		Tenant:      p.Tenant,
		DisplayName: name,
		Icon:        body.Icon,
		UpdatedAt:   time.Now().UTC(),
	}
	// Preserve the subdomain: it's a full-row upsert, and the subdomain is
	// owned by the dedicated endpoint below (putOrgSubdomain). Without this
	// carry-over a name/icon save would silently clear a claimed subdomain.
	if existing, err := h.Profiles.GetOrgProfile(r.Context(), p.Tenant); err == nil {
		pr.Subdomain = existing.Subdomain
	}
	if err := h.Profiles.PutOrgProfile(r.Context(), pr); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_profile.update", p.Tenant, "name="+name)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		"icon":         pr.Icon,
		"subdomain":    pr.Subdomain,
		"updated_at":   pr.UpdatedAt,
	})
}

// putOrgSubdomain claims (or clears) the org's subdomain label — the dedicated
// owner-only endpoint that owns the subdomain column, separate from the
// name/icon PUT so neither clobbers the other. Validates the label (DNS shape
// + reserved-name check), then upserts; a label already claimed by another org
// surfaces as 409 so the UI can say "taken" rather than 500. An empty label
// clears the subdomain.
func (h *HTTPGateway) putOrgSubdomain(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	if h.WildcardDomain == "" {
		writeAPIError(rw, http.StatusNotImplemented, "subdomains_disabled",
			"this deployment doesn't have per-org subdomains enabled")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Subdomain string `json:"subdomain"`
	}](rw, r)
	if !ok {
		return
	}
	label, err := auth.ValidateSubdomain(body.Subdomain)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_subdomain",
			"a subdomain may only use lowercase letters, numbers and hyphens (and can't be a reserved name)")
		return
	}
	// Load-merge-write so the name/icon already on the profile survive.
	pr := auth.OrgProfile{Tenant: p.Tenant, UpdatedAt: time.Now().UTC()}
	if existing, gerr := h.Profiles.GetOrgProfile(r.Context(), p.Tenant); gerr == nil {
		pr.DisplayName = existing.DisplayName
		pr.Icon = existing.Icon
	}
	pr.Subdomain = label
	if err := h.Profiles.PutOrgProfile(r.Context(), pr); err != nil {
		if errors.Is(err, auth.ErrSubdomainTaken) {
			writeAPIError(rw, http.StatusConflict, "subdomain_taken",
				"that subdomain is already taken — try another")
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_subdomain.update", p.Tenant, "subdomain="+label)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":          pr.Tenant,
		"subdomain":       pr.Subdomain,
		"wildcard_domain": h.WildcardDomain,
	})
}

// orgSubdomainAvailable is the owner-only pre-check the editor calls as the
// user types, so they learn a label is taken/invalid before saving. Returns
// {available, reason}. The caller's OWN current label reads as available (so
// re-saving an unchanged value isn't flagged as a conflict).
func (h *HTTPGateway) orgSubdomainAvailable(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "disabled"})
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	label, err := auth.ValidateSubdomain(r.URL.Query().Get("label"))
	if err != nil || label == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "invalid"})
		return
	}
	owner, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label)
	if err != nil {
		// No org holds it → free.
		writeJSON(rw, http.StatusOK, map[string]any{"available": true})
		return
	}
	if owner.Tenant == p.Tenant {
		writeJSON(rw, http.StatusOK, map[string]any{"available": true, "reason": "current"})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "taken"})
}

// resolveSubdomain is the PUBLIC (pre-auth) lookup the sign-in page uses to map
// a wildcard host label ("klahr" from klahr.dazyflow.app) back to the org's
// real tenant ID, so the SSO probe + Google start target the right org. Only
// the tenant + display name are returned (both already public on the sign-in
// surface); 404 when the label isn't claimed. No auth: a subdomain is public
// by nature, and this leaks nothing a visit to the host wouldn't.
func (h *HTTPGateway) resolveSubdomain(rw http.ResponseWriter, r *http.Request) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	label, err := auth.ValidateSubdomain(r.URL.Query().Get("label"))
	if err != nil || label == "" {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	pr, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		// The org logo (data: URL or glyph name), so the sign-in page can
		// brand itself for the org behind the subdomain. Already public.
		"icon": pr.Icon,
	})
}

// tlsAllow is the Caddy on-demand-TLS authorization endpoint ("ask"): Caddy
// calls GET /api/v1/auth/tls-allow?domain=<host> before issuing a certificate
// for a wildcard-subdomain host, and only issues when this returns 2xx. We
// allow exactly the org subdomains that have been CLAIMED — so an attacker
// pointing arbitrary <random>.<apex> hosts at our IP can't make us mint certs
// (Let's Encrypt rate-limit abuse) for hosts that map to no org. The apex
// itself is served by its own managed-cert site block and never reaches here.
func (h *HTTPGateway) tlsAllow(rw http.ResponseWriter, r *http.Request) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		http.Error(rw, "subdomains disabled", http.StatusForbidden)
		return
	}
	host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	suffix := "." + strings.ToLower(h.WildcardDomain)
	if host == "" || !strings.HasSuffix(host, suffix) {
		http.Error(rw, "not a managed host", http.StatusForbidden)
		return
	}
	label, err := auth.ValidateSubdomain(strings.TrimSuffix(host, suffix))
	if err != nil || label == "" {
		http.Error(rw, "invalid label", http.StatusForbidden)
		return
	}
	if _, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label); err != nil {
		http.Error(rw, "no such organization", http.StatusForbidden)
		return
	}
	rw.WriteHeader(http.StatusOK) // claimed → Caddy may issue the cert
}

// isTruthyQuery reports whether a query-param value means "on" — accepting
// "1"/"true"/"yes"/"on" and treating empty, absent, or garbage as false.
func isTruthyQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// wantsXML reports whether the caller asked for an XML response instead of
// the default JSON — either explicitly via ?format=xml or by an Accept
// header that prefers application/xml (or text/xml). JSON stays the default:
// anything else (including */* and a missing header) is treated as JSON.
func wantsXML(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "xml":
		return true
	case "json":
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/xml") || strings.Contains(accept, "text/xml")
}

// dropsXML wraps the catalog for an XML response. It mirrors the JSON shape
// (the same drops, the same field names via the manifests' xml tags) under a
// <drops><drop>…</drop></drops> root. The JSON body also emits a legacy
// "modules" alias; XML is a new surface with no legacy clients, so it carries
// the canonical "drops" name only.
type dropsXML struct {
	XMLName xml.Name        `xml:"drops"`
	Drops   []core.Manifest `xml:"drop"`
}

func (h *HTTPGateway) listModules(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	q := DropSearch{
		Query: r.URL.Query().Get("q"),
	}
	if c := r.URL.Query()["category"]; len(c) > 0 {
		q.Categories = c
	}
	if pr := r.URL.Query()["provider"]; len(pr) > 0 {
		q.Providers = pr
	}
	if t := r.URL.Query()["tag"]; len(t) > 0 {
		q.Tags = t
	}
	// The editor opts into seeing platform-disabled drops (shown greyed-out,
	// un-pickable) so they don't silently vanish from the palette; every other
	// caller of this endpoint leaves them hidden.
	q.IncludeDisabled = isTruthyQuery(r.URL.Query().Get("include_disabled"))
	mans, err := h.svc.SearchDrops(r.Context(), p, q)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// XML is opt-in (?format=xml or an XML Accept header); it serves the
	// same catalog as the JSON path, just in the XML representation.
	if wantsXML(r) {
		writeXML(rw, http.StatusOK, dropsXML{Drops: mans})
		return
	}
	// Emit both keys: "drops" is the new canonical name; "modules" is
	// kept for the legacy /api/v1/modules clients (and a transition
	// window for anything that still reads the old key).
	writeJSON(rw, http.StatusOK, map[string]any{"drops": mans, "modules": mans})
}

// listRuns returns a slim summary of recent runs for a single graph,
// newest first. Filter and paginate via ?status=&limit=&offset=. The
// hard cap on limit is 200 so a misbehaving client can't drain the
// table in one request.
func (h *HTTPGateway) listRuns(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	// Tenant-scope check: confirm the graph exists for this principal.
	if _, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	opts := parseRunListOpts(r)
	opts.Workspace = workspace
	opts.GraphID = id
	h.writeRunList(rw, r, p, opts)
}

// listAllRuns is the workspace-wide variant. tenant/workspace come from
// the principal (Service.ListGraphRuns overrides any client-supplied
// values), so this endpoint takes no path params — just query filters.
func (h *HTTPGateway) listAllRuns(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.writeRunList(rw, r, p, parseRunListOpts(r))
}

func parseRunListOpts(r *http.Request) core.ListGraphRunsOpts {
	opts := core.ListGraphRunsOpts{Limit: 20}
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if opts.Limit > 200 {
		opts.Limit = 200
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			opts.Offset = n
		}
	}
	if s := r.URL.Query().Get("status"); s != "" {
		opts.Status = core.JobStatus(s)
	}
	// Date range over a run's enqueue time. ?since= is an inclusive lower
	// bound, ?until= an exclusive upper bound (so a UI day-picker passing
	// midnight→next-midnight selects exactly that day). An unparseable value
	// is ignored rather than erroring — it just leaves that bound open.
	if ts, ok := parseRunListTime(r.URL.Query().Get("since")); ok {
		opts.Since = ts
	}
	if ts, ok := parseRunListTime(r.URL.Query().Get("until")); ok {
		opts.Until = ts
	}
	// Optional ?workspace= and ?tenant= narrow admin views. Service
	// layer enforces the actual scope: a scoped principal can't widen
	// past their binding (their tenant/workspace overrides whatever
	// the URL says), only platform admins can pass an arbitrary
	// tenant.
	if s := r.URL.Query().Get("workspace"); s != "" {
		opts.Workspace = s
	}
	if s := r.URL.Query().Get("tenant"); s != "" {
		opts.Tenant = s
	}
	return opts
}

// parseRunListTime parses a run-list ?since=/?until= bound. It accepts a full
// RFC3339 timestamp (what the web UI sends, having resolved a picked day to the
// user's local-midnight instant) or a bare YYYY-MM-DD date interpreted as UTC
// midnight (convenient for hand-rolled API calls). An empty or malformed value
// returns ok=false so the caller leaves that bound unset.
func parseRunListTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (h *HTTPGateway) writeRunList(rw http.ResponseWriter, r *http.Request, p core.Principal, opts core.ListGraphRunsOpts) {
	recs, err := h.svc.ListGraphRuns(r.Context(), p, opts)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]runSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, runSummary{
			ID:         rec.ID,
			GraphID:    rec.GraphID,
			Status:     rec.Status,
			EnqueuedAt: rec.EnqueuedAt,
			StartedAt:  rec.StartedAt,
			FinishedAt: rec.FinishedAt,
			ErrorCode:  errorCode(rec.Result),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runs": out})
}

// runSummary is the slim payload listRuns emits — JobRecord has more
// fields than the UI needs and serializing Result for every run wastes
// bandwidth on a list view.
type runSummary struct {
	ID         string         `json:"id"`
	GraphID    string         `json:"graph_id"`
	Status     core.JobStatus `json:"status"`
	EnqueuedAt time.Time      `json:"enqueued_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	ErrorCode  string         `json:"error_code,omitempty"`
}

func errorCode(r *core.Result) string {
	if r == nil || r.Error == nil {
		return ""
	}
	return r.Error.Code
}

// listAPIKeys, issueAPIKey, revokeAPIKey power the Admin UI's API
// keys card. All three require organization:admin (enforced in Service);
// without an AdminKeys store wired up they return 501.
func (h *HTTPGateway) listAPIKeys(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// ?tenant= narrows to a specific tenant. Platform admins may pass
	// any tenant; everyone else is force-scoped to their own.
	keys, err := h.svc.ListAPIKeys(r.Context(), p, r.URL.Query().Get("tenant"))
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"keys": keys})
}

// listTenants returns the set of tenants on this dzd. Platform admins
// only. Powers the tenant switcher in the top bar for super-admin UIs.
func (h *HTTPGateway) listTenants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenants, err := h.svc.ListTenants(r.Context(), p)
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tenants": tenants})
}

func (h *HTTPGateway) issueAPIKey(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	params, ok := decodeRequestJSON[IssueAPIKeyParams](rw, r)
	if !ok {
		return
	}
	issued, err := h.svc.IssueAPIKey(r.Context(), p, params)
	if err != nil {
		adminError(rw, err)
		return
	}
	h.audit(r.Context(), p, "apikey.issue", params.Subject, "")
	writeJSON(rw, http.StatusCreated, issued)
}

// listUsers derives one entry per distinct Subject from the API keys
// in the principal's tenant. Roles + permissions are rolled up.
func (h *HTTPGateway) listUsers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	users, err := h.svc.ListUsers(r.Context(), p, r.URL.Query().Get("tenant"))
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"users": users})
}

func (h *HTTPGateway) revokeAPIKey(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	id := r.PathValue("id")
	if err := h.svc.RevokeAPIKey(r.Context(), p, id); err != nil {
		adminError(rw, err)
		return
	}
	h.audit(r.Context(), p, "apikey.revoke", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

// adminError maps known Service errors to HTTP statuses without
// duplicating the message inspection at every handler.
func adminError(rw http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "requires permission"),
		strings.Contains(msg, "cannot act on tenant"):
		writeJSONError(rw, http.StatusForbidden, msg)
	case strings.Contains(msg, "not configured"):
		writeJSONError(rw, http.StatusNotImplemented, msg)
	case strings.Contains(msg, "is required"):
		writeJSONError(rw, http.StatusBadRequest, msg)
	case errors.Is(err, auth.ErrInvalidCredential):
		// A malformed / unparseable key id is bad client input, not a
		// server fault — e.g. DELETE /admin/api-keys/{id} with a junk id.
		// Map it to 400 instead of letting it fall through to 500.
		writeJSONError(rw, http.StatusBadRequest, msg)
	default:
		writeJSONError(rw, http.StatusInternalServerError, msg)
	}
}

// listPendingApprovals returns the await_approval inbox: every node
// in the principal's scope currently parked with Status=awaiting and
// a `pending_url` output. Sorted newest-first by the service layer.
//
// Optional ?workspace= narrows the inbox to a single workspace.
// Admins (whose principal carries no workspace binding) get the
// tenant-wide view by default; the UI uses this query param to
// reflect the workspace switcher's current selection.
func (h *HTTPGateway) listPendingApprovals(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	approvals, err := h.svc.ListPendingApprovals(
		r.Context(),
		p,
		r.URL.Query().Get("tenant"),
		r.URL.Query().Get("workspace"),
	)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"approvals": approvals})
}

// approveAuthed is the bearer-token-authenticated approval path used
// by the inbox UI. The principal's identity is trusted directly — no
// HMAC token to validate, because by getting here they've already
// proven workspace membership through the API key chain.
//
// The HMAC-based /approve/{runID}/{nodeID} endpoint stays available
// for the email/Slack notification flow where the approver doesn't
// have a session.
func (h *HTTPGateway) approveAuthed(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	decision := r.URL.Query().Get("decision")
	if decision == "" {
		decision = "approve"
	}
	// Tenant scope: load the parent graph through GetJob, which already
	// enforces the principal's tenant. Prevents a malicious-but-valid
	// API key from approving someone else's pending node.
	if _, err := h.svc.GetJob(r.Context(), p, runID); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	// Always attribute the approval to the authenticated principal — never a
	// client-supplied ?approver=. This path has a proven identity (the API-key
	// chain), so honoring a query param would let a valid caller forge who
	// approved in both the audit log and the resumed node's record. (The
	// unauthenticated HMAC /approve path is different: there the approver is
	// supplied because there's no session identity to trust.)
	if err := h.svc.Approve(r.Context(), runID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: p.Subject,
		Comment:  r.URL.Query().Get("comment"),
	}); err != nil {
		if strings.Contains(err.Error(), "not awaiting") {
			writeJSONError(rw, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		if strings.Contains(err.Error(), "approve or reject") {
			writeJSONError(rw, http.StatusBadRequest, err.Error())
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "approval", runID+"/"+nodeID, decision)
	writeJSON(rw, http.StatusOK, map[string]string{"status": "resumed", "decision": decision})
}

// cancelRun aborts an in-flight run. Body is an optional
// {"reason":"..."} for the audit trail. Maps service-layer errors to
// the conventional status codes: 404 unknown run, 409 already
// terminal, 403 unauthorized.
func (h *HTTPGateway) cancelRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	var body struct {
		Reason string `json:"reason"`
	}
	// Empty body is fine — keep the API ergonomic for the UI's
	// no-arg cancel click. Only fail on malformed JSON.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
			return
		}
	}
	if err := h.svc.CancelGraphRun(r.Context(), p, runID, body.Reason); err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			writeJSONError(rw, http.StatusNotFound, err.Error())
		case errors.Is(err, core.ErrConflict):
			writeJSONError(rw, http.StatusConflict, err.Error())
		case errors.Is(err, core.ErrUnauthorized):
			writeJSONError(rw, http.StatusForbidden, err.Error())
		default:
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "run.cancel", runID, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]string{"status": "cancelled"})
}

// resumeRun continues a run paused at a breakpoint (#12). Body {"step":true}
// advances one node and pauses again; otherwise the run continues to the
// next breakpoint or completion.
func (h *HTTPGateway) resumeRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	var body struct {
		Step bool `json:"step"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
			return
		}
	}
	if err := h.svc.ResumeGraphRun(r.Context(), p, runID, body.Step); err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			writeJSONError(rw, http.StatusNotFound, err.Error())
		case errors.Is(err, core.ErrConflict):
			writeJSONError(rw, http.StatusConflict, err.Error())
		case errors.Is(err, core.ErrUnauthorized):
			writeJSONError(rw, http.StatusForbidden, err.Error())
		default:
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
		}
		return
	}
	action := "run.resume"
	if body.Step {
		action = "run.step"
	}
	h.audit(r.Context(), p, action, runID, "")
	writeJSON(rw, http.StatusOK, map[string]string{"status": "resumed"})
}

func (h *HTTPGateway) runGraph(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	runID, err := h.svc.SubmitGraph(r.Context(), p, g)
	if err != nil {
		// Plan-gate refusals get 402 so the web client can show an
		// upgrade prompt instead of a generic error toast.
		if errors.Is(err, core.ErrPlanLimit) {
			writeJSONError(rw, http.StatusPaymentRequired, err.Error())
			return
		}
		// A suspended org is locked out — 403, not a generic 400.
		if errors.Is(err, core.ErrOrgSuspended) {
			writeJSONError(rw, http.StatusForbidden, err.Error())
			return
		}
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.run", id, "run="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
}

// testTrigger runs a webhook-triggered flow with a synthetic payload so
// a user can verify the flow end-to-end without wiring up an external
// caller (their website form, Zapier, curl, …). The request body is the
// sample payload; we feed it through the exact same seed-building path a
// real /trigger hit uses (buildWebhookSeed), so webhook_input nodes
// light up identically — closing the "Run button does nothing useful on
// a webhook flow" gap and the documented sampleNode webhook limitation.
//
// Unlike the public /trigger listener (bearer-secret auth, system
// principal), this runs under the caller's own token + graph:run, so it
// respects normal flow visibility and shows up in the run list like any
// other run.
func (h *HTTPGateway) testTrigger(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	// Read the sample body with a cap — a synthetic test payload is
	// small, and we don't want a stray large POST to balloon memory.
	var rawBody []byte
	if r.Body != nil {
		const maxSampleBytes = 1 << 20 // 1 MiB
		data, err := io.ReadAll(io.LimitReader(r.Body, maxSampleBytes+1))
		_ = r.Body.Close()
		if err != nil {
			writeJSONError(rw, http.StatusBadRequest, "read body")
			return
		}
		if int64(len(data)) > maxSampleBytes {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "sample body too large")
			return
		}
		rawBody = data
	}
	seed := buildWebhookSeed(rawBody, r)
	seeds := map[string]core.Result{}
	for _, n := range g.Nodes {
		if n.Module == webhookInputModuleID {
			seeds[n.ID] = seed
		}
	}
	if len(seeds) == 0 {
		writeJSONError(rw, http.StatusBadRequest, "flow has no webhook_input node to send a test event to")
		return
	}
	runID, err := h.svc.SubmitGraphWithSeed(r.Context(), p, g, seeds)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.run", id, "test-trigger run="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
}

// sampleNode runs a partial graph that ends at the requested nodeID.
// The submitted run contains only nodeID + its transitive predecessors
// — every other node and every edge that would lead out of the subset
// is dropped before submission. This lets a graph author "preview"
// what one node emits without firing downstream side effects.
//
// Identity is preserved end-to-end: the same graph ID, tenant, and
// workspace are reused, so the run shows up in the normal RunList
// (filtering "sample vs production" runs is a follow-up). Authz
// flows through SubmitGraph unchanged — sampling a node you can't
// run is rejected at the same gate as a full run would be.
//
// Limitations called out for V1: webhook_input nodes in the subset
// will fail standalone with code=no_trigger_data (no body was POSTed
// to the webhook listener for this run). Users on a webhook flow
// should hit the trigger via curl; "sample with a synthetic body"
// is a separate follow-up.
// slackEvents dispatches a Slack Events API POST to the configured
// handler. Returns 501 if the handler isn't wired (so a misconfigured
// deployment surfaces clearly instead of silently rejecting on bad
// signature).
func (h *HTTPGateway) slackEvents(rw http.ResponseWriter, r *http.Request) {
	if h.SlackEvents == nil {
		http.Error(rw, "Slack events endpoint not configured (set --slack-signing-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.SlackEvents.ServeHTTP(rw, r)
}

func (h *HTTPGateway) githubEvents(rw http.ResponseWriter, r *http.Request) {
	if h.GitHubEvents == nil {
		http.Error(rw, "GitHub events endpoint not configured (set --github-webhook-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.GitHubEvents.ServeHTTP(rw, r)
}

// stripeTenantEvents is the tenant-scoped Stripe webhook (payment
// triggers) — not to be confused with stripeEvents, the platform
// billing webhook on the unsuffixed path.
func (h *HTTPGateway) stripeTenantEvents(rw http.ResponseWriter, r *http.Request) {
	if h.StripeEvents == nil {
		http.Error(rw, "Stripe events endpoint not configured (encrypted secret store required)", http.StatusNotImplemented)
		return
	}
	h.StripeEvents.ServeHTTP(rw, r)
}

func (h *HTTPGateway) sampleNode(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	sub, ok := g.UpstreamSubset(nodeID)
	if !ok {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("node %q not in graph %q", nodeID, id))
		return
	}
	// Sampling re-runs the upstream chain, so a trigger node in the subset would
	// fail with no_trigger_data (a trigger has no data outside a real firing).
	// Detect that here and return an actionable error pointing at test-trigger,
	// instead of submitting a run that dies cryptically.
	if mans, mErr := h.svc.ListDrops(r.Context(), p); mErr == nil {
		for _, n := range sub.Nodes {
			if m, ok := mans[n.Module]; ok && m.ExecutionModel == core.ExecutionTrigger {
				writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf(
					"can't sample %q: it depends on trigger node %q (%s), which has no data outside a real firing — use the flow's test-trigger with a payload instead",
					nodeID, n.ID, n.Module))
				return
			}
		}
	}
	runID, err := h.svc.SubmitGraph(r.Context(), p, sub)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]string{
		"job_id":       runID,
		"sampled_node": nodeID,
	})
}

// jobEvents streams bus events for jobID as Server-Sent Events. Each
// frame is `event: <kind>\ndata: <json>\n\n` where kind is "progress",
// "terminal", or "snapshot" (the initial frame containing the current
// JobRecord).
//
// The stream closes when the job reaches a terminal state. The handler
// also flushes on every event so browsers see updates promptly.
func (h *HTTPGateway) jobEvents(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	jobID := r.PathValue("jobID")
	rec, err := h.svc.GetJob(r.Context(), p, jobID)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}

	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Subscribe BEFORE deciding whether the run is already terminal. A short
	// run can reach its terminal state in the gap between the GetJob above and
	// the subscription taking effect; if we checked the snapshot's status and
	// only subscribed afterwards, the terminal bus event published in that gap
	// would reach no subscriber and the stream would hang until the client's
	// deadline (a flaky 30s stall in tests; a wedged "Upgrade…"-style spinner
	// for a UI that reconnects to a just-finished run). Subscribing first means
	// any such event is buffered on our channel, and the status re-read below
	// catches a run that finished at or before subscribe time.
	events, cancel := h.svc.bus().Subscribe(jobID)
	defer cancel()

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)

	// Snapshot first so the UI has the current state without racing
	// against subscriber delivery. Emit the same clean runView the REST
	// /me/runs/{id} endpoint returns — not the raw JobRecord.
	writeSSE(rw, "snapshot", newRunView(rec))
	// Followed by per-node status snapshots — late subscribers (the UI
	// that connects after Submit returns) catch up on transitions that
	// already happened.
	h.emitNodeSnapshots(rw, r.Context(), rec)
	flusher.Flush()

	// Re-read the status now that we're subscribed. If the run reached a
	// terminal state at or before subscribe time, emit terminal and stop; a
	// terminal that lands after this point instead arrives on `events`.
	if cur, err := h.svc.GetJob(r.Context(), p, jobID); err == nil {
		rec = cur
	}
	if core.IsTerminalStatus(rec.Status) {
		writeSSE(rw, "terminal", sseTerminalView{
			RunID:  rec.ID,
			Status: rec.Status,
			Error:  resultError(rec.Result),
		})
		flusher.Flush()
		return
	}

	// Keep-alive ping every 25s — proxies time out idle SSE streams
	// faster than that without a heartbeat.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// SSE comment lines (starting with ":") are dropped by the
			// EventSource API but keep the TCP connection alive.
			_, _ = fmt.Fprintf(rw, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Progress != nil {
				writeSSE(rw, "progress", ev.Progress)
				flusher.Flush()
			}
			if ev.NodeStatus != nil {
				writeSSE(rw, "node", ev.NodeStatus)
				flusher.Flush()
			}
			if ev.Paused != nil {
				writeSSE(rw, "paused", ev.Paused)
				flusher.Flush()
			}
			if ev.Terminal != nil {
				writeSSE(rw, "terminal", newSSETerminalView(ev.Terminal))
				flusher.Flush()
				return
			}
		}
	}
}

// watchFlowMe streams `flow_updated` Server-Sent Events for a flow: one
// frame each time the flow's graph is saved, by anyone (the web editor, the
// MCP server, a direct API call). An open editor subscribes so it can
// live-reflect external edits — e.g. an AI assistant restructuring the flow
// through MCP — animating the new graph onto its canvas.
//
// The frame carries only {flow_id, commit, author, autosave} — no graph
// content. The client re-fetches the graph through the normal authorized
// load path on receipt, and uses `commit` to ignore the echo of its own
// save. Mirrors jobEvents' SSE plumbing (headers, flush, 25s keep-alive,
// disconnect on context cancel).
func (h *HTTPGateway) watchFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// Validate scope + readability up front (and resolve the id parts) the
	// same way a load would — a 403/404 here is clearer than a silent stream
	// that never emits. The graph itself is discarded; only the key matters.
	tenant, workspace, id, _, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}

	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Subscribe BEFORE writing the ": watching" comment that signals the
	// stream is live. If we opened the stream first and subscribed after, an
	// edit landing in that gap would be published to no subscriber and missed;
	// a client that treats ": watching" as "I'm now receiving updates" (and
	// any test that publishes right after it) would then lose the event.
	// Subscribing first makes ": watching" a truthful readiness signal.
	events, cancel := h.svc.bus().Subscribe(flowBusKey(tenant, workspace, id))
	defer cancel()

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)
	// An initial comment opens the stream so the client's fetch resolves its
	// response promptly even before the first edit lands.
	_, _ = fmt.Fprintf(rw, ": watching\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprintf(rw, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.FlowUpdated != nil {
				writeSSE(rw, "flow_updated", ev.FlowUpdated)
				flusher.Flush()
			}
		}
	}
}

// emitNodeSnapshots walks the graph payload from the graph-record and
// emits one `node` SSE frame per node that already has a stored record.
// This catches up subscribers that connect after the worker has already
// processed some nodes — without it, the canvas would show stale
// statuses until the next live transition.
func (h *HTTPGateway) emitNodeSnapshots(rw http.ResponseWriter, ctx context.Context, graphRec core.JobRecord) {
	if graphRec.Kind != core.JobKindGraph || len(graphRec.GraphPayload) == 0 {
		return
	}
	var g core.Graph
	if err := json.Unmarshal(graphRec.GraphPayload, &g); err != nil {
		return
	}
	for _, n := range g.Nodes {
		nodeRec, err := h.svc.Jobs.Get(ctx, NodeJobID(graphRec.ID, n.ID))
		if err != nil {
			continue
		}
		var jerr *core.JobError
		if nodeRec.Result != nil {
			jerr = nodeRec.Result.Error
		}
		writeSSE(rw, "node", NodeStatusEvent{
			NodeID: n.ID,
			Status: nodeRec.Status,
			Error:  jerr,
		})
	}
}

// --- helpers ----------------------------------------------------------

func writeJSON(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body)
}

// writeXML is the XML analogue of writeJSON — it emits the XML declaration
// followed by the marshaled body. Used by endpoints that content-negotiate
// an XML representation (see wantsXML); the body's xml struct tags decide the
// element names, mirroring the JSON tags.
func writeXML(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/xml; charset=utf-8")
	rw.WriteHeader(status)
	_, _ = io.WriteString(rw, xml.Header)
	_ = xml.NewEncoder(rw).Encode(body)
}

// writeJSONError emits the structured ErrorEnvelope with a code derived
// from the HTTP status. It exists so the ~260 legacy call sites that only
// have (status, message) still produce the SAME wire shape as
// writeAPIError — one error envelope across the whole API. Handlers that
// have a more specific machine code should call writeAPIError directly.
func writeJSONError(rw http.ResponseWriter, status int, msg string) {
	writeAPIError(rw, status, codeForStatus(status), msg)
}

// writeSSE writes a single Server-Sent Events frame: `event: <name>\n
// data: <json>\n\n`. The browser's EventSource parser dispatches on
// the event name without parsing the payload twice. Used by the job
// events stream.
func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}
