// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// HTTPGateway exposes Service over JSON/HTTP so browsers and other
// non-gRPC clients can drive Dazyflow.
//
// The route list that used to live in this comment named
// /api/v1/graphs/... endpoints that are no longer mounted, which made it
// worse than no list at all. mountRoutes is the authoritative surface —
// read it there, and note that route_sweep_test.go scrapes those
// registrations so every mounted route stays covered without a doc edit.
//
// Auth is bearer token in `Authorization: Bearer <api-key>` — the same
// API-key chain the gRPC server uses — or a session cookie for browser
// clients. Cross-origin access is restricted per-deployment via
// AllowedOrigins.
type HTTPGateway struct {
	svc            *Service
	logger         *log.Logger
	AllowedOrigins []string // empty = "*"

	// LogTail, when set, captures the daemon's log stream for the
	// platform-admin "System log" viewer (GET /api/v1/admin/system/log).
	// Nil leaves that endpoint returning 501 — the feature is simply off.
	// cmd/dzd installs it as a tee behind the standard logger at startup.
	LogTail *LogTail

	// Runners and RunnerTasks power the runner feature: the admin API an org
	// uses to register machines, and the queue their agents claim work from.
	// Nil leaves those endpoints answering 501, so a deployment that has not
	// configured runner storage simply does not offer the feature.
	Runners     *Runners
	RunnerTasks RunnerTaskStore

	// MCPServers powers Admin → MCP servers: the org's own MCP endpoints,
	// whose tools become steps in its palette. Nil leaves those endpoints
	// answering 501, so a deployment without the store simply does not offer
	// the feature — the same shape as Runners above.
	MCPServers *MCPServers

	// WebAPIs powers Admin → Web APIs: the org's own service, described once
	// and turned into one step per operation. Nil leaves those endpoints
	// answering 501, the same shape as MCPServers above.
	WebAPIs *WebAPIs

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

	// Support feature. SupportAgents is the
	// runtime grant store deciding who gets core.SupportAgentRole at session
	// issue; Grants persists AccessGrants (the consented, time-boxed views);
	// Bundles persists redacted SupportBundleRecords. All nil-safe: nil leaves
	// the support endpoints returning 501/empty, and no agent is elevated.
	SupportAgents support.AgentStore
	Grants        core.GrantStore
	Bundles       core.BundleStore
	// Tickets persists support tickets + their chat threads (Phase 2). Nil-safe:
	// nil leaves the ticket endpoints returning 501, same as the grant surface.
	Tickets core.TicketStore
	// SupportGrantTTL is how long an approved AccessGrant stays valid. Zero
	// falls back to defaultSupportGrantTTL (4h).
	SupportGrantTTL time.Duration
	// SupportRateLimit throttles authenticated support WRITES per subject
	// (filing a ticket, posting a message). Defaulted in the constructor like
	// WebhookRateLimit — an unset limiter would leave the one endpoint that
	// persists a bundle per call completely unbounded.
	SupportRateLimit *ipRateLimiter
	// SupportInbox is the shared address told about NEW and unassigned-ticket
	// activity (DAZYFLOW_SUPPORT_INBOX). Empty means those edges send nothing:
	// there is no single agent to address for an unclaimed ticket, and mailing
	// every provisioned agent is not a default worth having.
	SupportInbox string
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

	// GitMirrors stores each workspace's git-mirror config + last-push
	// status; MirrorPusher performs the pushes. Both nil leaves the
	// /api/v1/git/mirror endpoints returning 501 — the feature is simply
	// off, which is the state of any deployment without Postgres or
	// without the encrypted secret store the SSH key lives in.
	GitMirrors   GitMirrorStore
	MirrorPusher *MirrorPusher

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

	// RunnerRateLimit throttles the agent's own endpoints (register, claim,
	// progress, result) per client IP. They sit outside requireAuth and each
	// touches the database before it can reject a stranger, so without a
	// throttle one host can saturate the connection pool and starve both
	// legitimate runner polling and the web app. Defaulted in the constructor.
	RunnerRateLimit *ipRateLimiter

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
		// Defaulted here rather than in mountRoutes. These endpoints
		// (support + provider events) are reachable by strangers, so they
		// must always be throttled — a generous per-IP allowance that
		// legitimate webhook senders won't hit but that caps a
		// brute-force/flood. Defaulting them as a mountRoutes side effect
		// meant the fields were MUTATED during route mounting, which races
		// if mountRoutes ever runs twice concurrently (ServeForTest does
		// call it per invocation). Construction is the right place: the
		// gateway is never usable without them.
		SupportRateLimit: newIPRateLimiter(defaultSupportRatePerMin, defaultSupportRateBurst),
		WebhookRateLimit: newIPRateLimiter(defaultWebhookRatePerMin, defaultWebhookRateBurst),
		RunnerRateLimit:  newIPRateLimiter(defaultRunnerRatePerMin, defaultRunnerRateBurst),
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

// ServeForTest exposes the mux without binding a port — analogous to
// ServeWebhookForTest. Tests build a Gateway, attach it to httptest.
func ServeForTest(h *HTTPGateway, rw http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	h.withCORSAndLogging(h.verifyCookieOrigin(h.limitRequestBody(jsonErrors(mux)))).ServeHTTP(rw, r)
}
