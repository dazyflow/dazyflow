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

type HTTPGateway struct {
	svc            *Service
	logger         *log.Logger
	AllowedOrigins []string // empty = "*"

	LogTail *LogTail

	Runners     *Runners
	RunnerTasks RunnerTaskStore

	MCPServers *MCPServers

	WebAPIs *WebAPIs

	WildcardDomain string

	Sessions      auth.SessionStore
	Users         auth.UserStore
	SessionTTL    time.Duration
	MaxSessionAge time.Duration

	TOTPKey        []byte
	TOTPChallenges auth.TOTPChallengeStore
	Ephemeral      auth.EphemeralStore

	EncryptedSecrets *EncryptedSecrets

	OAuth *OAuthRegistry

	EnableSignup bool

	PlatformAdmins []string

	PlatformAdminGrants PlatformAdminStore

	platformAdminGranted sync.Map

	SupportAgents       support.AgentStore
	Grants              core.GrantStore
	Bundles             core.BundleStore
	Tickets             core.TicketStore
	SupportGrantTTL     time.Duration
	SupportRateLimit    *ipRateLimiter
	SupportInbox        string
	supportAgentGranted sync.Map
	supportNow          func() time.Time

	UpdateURL string

	EnableMetrics bool

	Audit core.AuditLog

	SlackEvents *SlackEventsHandler

	GitHubEvents *GitHubEventsHandler

	StripeEvents *StripeEventsHandler

	Billing *BillingHandler

	Memberships auth.MembershipStore

	Invitations auth.InvitationStore

	OrgAuth auth.OrgAuthStore

	Profiles auth.OrgProfileStore

	GitMirrors   GitMirrorStore
	MirrorPusher *MirrorPusher

	Blocklist auth.BlocklistStore

	DropSwitches DropSwitchStore

	Webhook *WebhookListener

	Approval *ApprovalListener

	AuthRateLimit *ipRateLimiter

	WebhookRateLimit *ipRateLimiter

	RunnerRateLimit *ipRateLimiter

	TrustProxyHeaders bool

	Metrics *Metrics

	DBPool *pgxpool.Pool

	ReadyCheck func(ctx context.Context) error

	WebDist string

	idempotency *idempotencyStore

	LandingDir string

	DisableCompression bool

	MapTileURL     string
	MapGeocoderURL string

	cspOnce sync.Once
	csp     string
}

func NewHTTPGateway(svc *Service) *HTTPGateway {
	return &HTTPGateway{
		svc:              svc,
		logger:           log.New(log.Writer(), "http-api: ", log.LstdFlags),
		idempotency:      newIdempotencyStore(),
		Ephemeral:        auth.NewMemEphemeralStore(),
		SupportRateLimit: newIPRateLimiter(defaultSupportRatePerMin, defaultSupportRateBurst),
		WebhookRateLimit: newIPRateLimiter(defaultWebhookRatePerMin, defaultWebhookRateBurst),
		RunnerRateLimit:  newIPRateLimiter(defaultRunnerRatePerMin, defaultRunnerRateBurst),
	}
}

// ServeListener takes an already-bound listener, so the daemon can bind on the
// main goroutine and fail loudly at startup. mountRoutes is the authoritative
// route list: route_sweep_test.go scrapes those registrations, so a route
// mounted anywhere else is one nothing covers.

func (h *HTTPGateway) ServeListener(ctx context.Context, ln net.Listener) error {
	go auth.WarmPasswordTiming()
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	srv := &http.Server{
		Handler:           h.withCORSAndLogging(h.verifyCookieOrigin(limitRequestBody(gzipResponses(!h.DisableCompression, jsonErrors(mux))))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

func ServeForTest(h *HTTPGateway, rw http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	h.withCORSAndLogging(h.verifyCookieOrigin(limitRequestBody(gzipResponses(!h.DisableCompression, jsonErrors(mux))))).ServeHTTP(rw, r)
}
