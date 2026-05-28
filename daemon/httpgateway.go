package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
)

// HTTPGateway exposes Service over JSON/HTTP so browsers and other
// non-gRPC clients can drive Hazy Flow. The endpoint surface is small
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

	// Sessions and Users power the email+password sign-in flow. Both
	// are optional — leaving them nil disables the /auth endpoints
	// without breaking API-key auth.
	Sessions   auth.SessionStore
	Users      auth.UserStore
	SessionTTL time.Duration // default 24h when zero

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
	// often want admin-invite-only signup. The hzd binary opts in
	// via --signup or $HAZYFLOW_ENABLE_SIGNUP.
	EnableSignup bool

	// EnableMetrics mounts an unauthenticated GET /metrics Prometheus
	// endpoint. Off by default: it exposes tenant names + disk usage, so
	// operators opt in via --metrics and restrict scrape access at the
	// network/proxy layer. Wired by hzd.
	EnableMetrics bool

	// Audit, when set, records administrative actions (graph save/run,
	// secret + API-key changes, approvals, cancels) and powers
	// GET /api/v1/admin/audit. Nil disables auditing + that endpoint.
	Audit core.AuditLog

	// SlackEvents handles Slack Events API POSTs (app_mention etc.).
	// Nil = the route returns 501. Wired by hzd when the Slack
	// signing-secret flag/env is set.
	SlackEvents *SlackEventsHandler

	// GitHubEvents handles GitHub webhook POSTs (push, pull_request).
	// Nil = the route returns 501. Wired by hzd when the GitHub
	// webhook-secret flag/env is set.
	GitHubEvents *GitHubEventsHandler

	// AuthRateLimit, when set, throttles the sign-in / sign-up
	// endpoints per client IP. Nil disables throttling (dev default).
	AuthRateLimit *ipRateLimiter

	// TrustProxyHeaders makes the gateway honor X-Forwarded-Proto when
	// deciding whether a request arrived over HTTPS (for the Secure
	// cookie flag + HSTS). Enable ONLY when hzd sits behind a trusted
	// TLS-terminating reverse proxy — otherwise a direct client could
	// spoof the header. Off by default.
	TrustProxyHeaders bool

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

	// LandingDir, when non-empty, points at a directory of static
	// marketing-site files (the separate hazy-flow-landing repo:
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
		svc:    svc,
		logger: log.New(log.Writer(), "http-api: ", log.LstdFlags),
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
		Handler:           h.withCORSAndLogging(h.verifyCookieOrigin(mux)),
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
	// Liveness: the process is up and serving. Never touches deps.
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	// Readiness: can the process actually serve requests (deps reachable)?
	// ReadyCheck is nil for the dev/in-memory deployment — then ready ==
	// alive. With Postgres, cmd/hzd wires a pool ping so orchestration
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
		_, _ = rw.Write([]byte("ready"))
	})
	// Prometheus scrape endpoint, opt-in (exposes tenant names + usage).
	if h.EnableMetrics {
		mux.HandleFunc("GET /metrics", h.metrics)
	}
	mux.HandleFunc("POST /api/v1/auth/signin", h.rateLimitAuth(h.signIn))
	mux.HandleFunc("POST /api/v1/auth/signup", h.rateLimitAuth(h.signUp))
	mux.HandleFunc("POST /api/v1/auth/signout", h.signOut)
	mux.HandleFunc("GET /api/v1/whoami", h.requireAuth(h.whoami))
	mux.HandleFunc("GET /api/v1/workspaces", h.requireAuth(h.listWorkspaces))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.uploadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/secrets", h.requireAuth(h.listSecrets))
	mux.HandleFunc("PUT /api/v1/secrets/{name}", h.requireAuth(h.putSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", h.requireAuth(h.deleteSecret))
	mux.HandleFunc("GET /api/v1/oauth/providers", h.requireAuth(h.oauthListProviders))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/authorize", h.requireAuth(h.oauthAuthorize))
	// Callback is UNAUTHENTICATED — the OAuth provider redirects the
	// user's browser back here without a Bearer token. State-token
	// validation in the handler is what binds the callback to the
	// original principal.
	mux.HandleFunc("GET /api/v1/oauth/{provider}/callback", h.oauthCallback)
	mux.HandleFunc("GET /api/v1/drops", h.requireAuth(h.listModules))
	// Legacy alias — hzctl and older proxies still hit /modules. Keep
	// it pointing at the same handler so we can deprecate at our pace.
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(h.listModules))
	mux.HandleFunc("GET /api/v1/graphs", h.requireAuth(h.listGraphs))
	mux.HandleFunc("GET /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.loadGraph))
	mux.HandleFunc("PUT /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.saveGraph))
	mux.HandleFunc("POST /api/v1/graphs/{tenant}/{workspace}/{id}/run", h.requireAuth(h.runGraph))
	mux.HandleFunc("POST /api/v1/graphs/{tenant}/{workspace}/{id}/test-trigger", h.requireAuth(h.testTrigger))
	mux.HandleFunc("POST /api/v1/graphs/{tenant}/{workspace}/{id}/nodes/{nodeID}/sample", h.requireAuth(h.sampleNode))
	mux.HandleFunc("POST /api/v1/validate/cron", h.requireAuth(h.validateCron))
	// Slack Events API endpoint. NOT under requireAuth — Slack POSTs
	// as a stranger; the HMAC signature is the auth.
	mux.HandleFunc("POST /api/v1/events/slack/{tenant}", h.slackEvents)
	mux.HandleFunc("POST /api/v1/events/github/{tenant}", h.githubEvents)
	mux.HandleFunc("GET /api/v1/graphs/{tenant}/{workspace}/{id}/runs", h.requireAuth(h.listRuns))
	mux.HandleFunc("GET /api/v1/runs", h.requireAuth(h.listAllRuns))
	mux.HandleFunc("POST /api/v1/runs/{runID}/cancel", h.requireAuth(h.cancelRun))
	mux.HandleFunc("GET /api/v1/approvals/pending", h.requireAuth(h.listPendingApprovals))
	mux.HandleFunc("POST /api/v1/approvals/{runID}/{nodeID}", h.requireAuth(h.approveAuthed))
	mux.HandleFunc("GET /api/v1/admin/api-keys", h.requireAuth(h.listAPIKeys))
	mux.HandleFunc("POST /api/v1/admin/api-keys", h.requireAuth(h.issueAPIKey))
	mux.HandleFunc("DELETE /api/v1/admin/api-keys/{id}", h.requireAuth(h.revokeAPIKey))
	mux.HandleFunc("GET /api/v1/admin/users", h.requireAuth(h.listUsers))
	mux.HandleFunc("GET /api/v1/admin/tenants", h.requireAuth(h.listTenants))
	mux.HandleFunc("GET /api/v1/admin/audit", h.requireAuth(h.listAudit))
	mux.HandleFunc("GET /api/v1/admin/limits", h.requireAuth(h.workspaceLimits))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}", h.requireAuth(h.jobSnapshot))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/nodes", h.requireAuth(h.listRunNodes))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/nodes/{nodeID}", h.requireAuth(h.nodeSnapshot))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/events", h.requireAuth(h.jobEvents))
	mux.HandleFunc("POST /api/v1/chat/stream", h.requireAuth(h.chatStream))

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
		if r.URL.Path == "/" {
			if h.hasValidSession(r) {
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
	h.withCORSAndLogging(h.verifyCookieOrigin(mux)).ServeHTTP(rw, r)
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
		if !originAllowed(origin, h.AllowedOrigins) {
			h.logger.Printf("CSRF reject: Origin=%q not in allowed=%v (host=%q)", origin, h.AllowedOrigins, r.Host)
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
			writeJSONError(rw, http.StatusUnauthorized, fmt.Sprintf("auth: %v", err))
			return
		}
		next(rw, r, p)
	}
}

const sessionCookieName = "hazyflow_session"

// credentialFromRequest extracts a bearer credential from either the
// Authorization header (preferred, used by hzctl and API-key clients)
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
	if len(h.AllowedOrigins) > 0 {
		allowed = strings.Join(h.AllowedOrigins, ", ")
		// Cookie-based sessions require an explicit origin and
		// Access-Control-Allow-Credentials: true. Wildcard "*" is
		// incompatible with credentials per the CORS spec.
		allowCreds = true
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowCreds && origin != "" && originAllowed(origin, h.AllowedOrigins) {
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
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// requestIsHTTPS reports whether the request reached the user over TLS.
// Directly: r.TLS is set. Behind a TLS-terminating reverse proxy the
// connection to hzd is plain HTTP, so we consult X-Forwarded-Proto —
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

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
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
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	user, err := auth.VerifyPassword(r.Context(), h.Users, body.Email, body.Password)
	if err != nil {
		writeJSONError(rw, http.StatusUnauthorized, "invalid email or password")
		return
	}
	ttl := h.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, user, ttl)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
	writeJSON(rw, http.StatusOK, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
	})
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
		_ = h.Sessions.DeleteSession(r.Context(), token)
	}
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
	rw.WriteHeader(http.StatusNoContent)
}

// whoami returns the authenticated principal's identity AND the flat
// set of permissions any of their roles grant. The UI uses this for
// role gating (whether to show the Admin link, the Edit button, etc.)
// without re-implementing role unrolling client-side.
func (h *HTTPGateway) whoami(rw http.ResponseWriter, _ *http.Request, p core.Principal) {
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
	writeJSON(rw, http.StatusOK, map[string]any{
		"subject":     p.Subject,
		"tenant":      p.Tenant,
		"workspace":   p.Workspace,
		"roles":       p.Roles,
		"permissions": perms,
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

// listWorkspaces returns the workspaces the bearer can access. The UI
// uses it to drive the top-bar switcher. Platform admins may pass
// ?tenant= to list workspaces in any tenant; everyone else's tenant
// query is ignored (their principal binding wins).
func (h *HTTPGateway) listWorkspaces(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	ws, err := h.svc.ListWorkspaces(r.Context(), p, r.URL.Query().Get("tenant"))
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"workspaces": ws})
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
	mans, err := h.svc.SearchDrops(r.Context(), p, q)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Emit both keys: "drops" is the new canonical name; "modules" is
	// kept for the legacy /api/v1/modules clients (and a transition
	// window for anything that still reads the old key).
	writeJSON(rw, http.StatusOK, map[string]any{"drops": mans, "modules": mans})
}

func (h *HTTPGateway) listGraphs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.URL.Query().Get("tenant")
	workspace := r.URL.Query().Get("workspace")
	if tenant == "" || workspace == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant and workspace query params required")
		return
	}
	summaries, err := h.svc.ListFlowSummaries(r.Context(), p, tenant, workspace)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"graphs": summaries})
}

func (h *HTTPGateway) loadGraph(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	ref := r.URL.Query().Get("ref") // empty = HEAD
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ref)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, g)
}

func (h *HTTPGateway) saveGraph(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	var g core.Graph
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	// Honor the path parameters as the source of truth — the UI may
	// have omitted them in the body, or may have stale values.
	g.Tenant = r.PathValue("tenant")
	g.Workspace = r.PathValue("workspace")
	g.ID = r.PathValue("id")
	commit, err := h.svc.SaveGraph(r.Context(), p, g)
	if err != nil {
		// 409 when the flow is locked by an in-flight run so the UI can
		// surface "Locked — run in progress" instead of a generic 400.
		if errors.Is(err, core.ErrConflict) {
			writeJSONError(rw, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Run the advisory lint on the saved graph and include findings in
	// the response. Lint is non-blocking — a failed lint doesn't stop
	// the save; the UI surfaces the warnings post-save so the user can
	// fix-and-resave or dismiss.
	h.audit(r.Context(), p, "graph.save", g.ID, "commit="+commit)
	writeJSON(rw, http.StatusOK, map[string]any{
		"commit":   commit,
		"graph_id": g.ID,
		"lint":     core.LintGraph(g),
	})
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
// keys card. All three require tenant:admin (enforced in Service);
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

// listTenants returns the set of tenants on this hzd. Platform admins
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
	var params IssueAPIKeyParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
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
	case strings.Contains(msg, "requires permission"):
		writeJSONError(rw, http.StatusForbidden, msg)
	case strings.Contains(msg, "not configured"):
		writeJSONError(rw, http.StatusNotImplemented, msg)
	case strings.Contains(msg, "is required"):
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
	approver := r.URL.Query().Get("approver")
	if approver == "" {
		approver = p.Subject
	}
	if err := h.svc.Approve(r.Context(), runID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: approver,
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
		http.Error(rw, "Slack events endpoint not configured (set --slack-signing-secret on hzd)", http.StatusNotImplemented)
		return
	}
	h.SlackEvents.ServeHTTP(rw, r)
}

func (h *HTTPGateway) githubEvents(rw http.ResponseWriter, r *http.Request) {
	if h.GitHubEvents == nil {
		http.Error(rw, "GitHub events endpoint not configured (set --github-webhook-secret on hzd)", http.StatusNotImplemented)
		return
	}
	h.GitHubEvents.ServeHTTP(rw, r)
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

func (h *HTTPGateway) jobSnapshot(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	jobID := r.PathValue("jobID")
	rec, err := h.svc.GetJob(r.Context(), p, jobID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, rec)
}

// nodeSnapshot returns the node-record (status + result) for a single
// node within a graph run. Used by the output-preview pane in the UI
// inspector so an operator can see exactly what each port emitted.
//
// Authz is enforced by reading the parent graph-record (which
// Service.GetJob already checks) before exposing the node — otherwise
// a leaked node-record ID would bypass the tenant scope.
func (h *HTTPGateway) nodeSnapshot(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("jobID")
	nodeID := r.PathValue("nodeID")
	// Authz: load the parent graph-record through Service.GetJob so the
	// principal-scope check fires before we hand back node data.
	if _, err := h.svc.GetJob(r.Context(), p, runID); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	nodeRec, err := h.svc.Jobs.Get(r.Context(), NodeJobID(runID, nodeID))
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, nodeRec)
}

// listRunNodes returns every per-node record for a single run. The
// run-detail UI calls this once on page load to draw the per-node
// timeline + status dots in one round trip, instead of N nodeSnapshot
// calls. Authz mirrors nodeSnapshot — the run-record GetJob check is
// what gates access; node records inherit that scope.
func (h *HTTPGateway) listRunNodes(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("jobID")
	// Authz: load the parent run-record through Service.GetJob first.
	runRec, err := h.svc.GetJob(r.Context(), p, runID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	nodes, err := h.svc.Jobs.ListNodeRecords(r.Context(), core.ListNodeRecordsOpts{
		Tenant:     runRec.Tenant,
		Workspace:  runRec.Workspace,
		GraphRunID: runID,
		Limit:      1000, // typical graphs have <100 nodes; cap defensively
	})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if nodes == nil {
		nodes = []core.JobRecord{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"nodes": nodes})
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
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)

	// Snapshot first so the UI has the current state without racing
	// against subscriber delivery.
	writeSSE(rw, "snapshot", rec)
	// Followed by per-node status snapshots — late subscribers (the UI
	// that connects after Submit returns) catch up on transitions that
	// already happened.
	h.emitNodeSnapshots(rw, r.Context(), rec)
	flusher.Flush()
	if core.IsTerminalStatus(rec.Status) {
		writeSSE(rw, "terminal", map[string]any{"status": rec.Status})
		flusher.Flush()
		return
	}

	events, cancel := h.svc.bus().Subscribe(jobID)
	defer cancel()

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
			if ev.Terminal != nil {
				writeSSE(rw, "terminal", ev.Terminal)
				flusher.Flush()
				return
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

func writeJSONError(rw http.ResponseWriter, status int, msg string) {
	writeJSON(rw, status, map[string]string{"error": msg})
}

// chatStream runs Service.ChatStream and forwards each ChatEvent
// to the client as an SSE frame named after the event's Type
// ("text", "tool_use_start", "tool_use_result", "proposal", "done",
// "error"). The browser parses these and updates the chat panel.
//
// Body shape:
//
//	{
//	  "messages": [{"role":"user","content":"plain text"}, ...]
//	}
//
// On 500/timeout/aborted ctx, the handler emits one final "error"
// frame before closing so the browser can show a banner.
func (h *HTTPGateway) chatStream(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.svc.AnthropicAPIKey == "" && !h.svc.UseClaudeCLI {
		writeJSONError(rw, http.StatusServiceUnavailable, "chat is not enabled on this daemon (set ANTHROPIC_API_KEY or -claude-cli)")
		return
	}
	// Grab the raw bearer for forwarding to hz-mcp in claude-cli mode.
	// requireAuth already validated it; this is the same string.
	bearerToken := credentialFromRequest(r)
	var body struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if len(body.Messages) == 0 {
		writeJSONError(rw, http.StatusBadRequest, "messages must be non-empty")
		return
	}
	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no")
	rw.WriteHeader(http.StatusOK)
	flusher.Flush()

	err := h.svc.ChatStream(r.Context(), p, bearerToken, body.Messages, func(ev ChatEvent) error {
		// The event name doubles as the SSE `event:` field so the
		// browser can dispatch on it without parsing the payload.
		writeSSE(rw, ev.Type, ev)
		flusher.Flush()
		return r.Context().Err()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		writeSSE(rw, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
	}
}

func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}
