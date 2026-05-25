package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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
}

func NewHTTPGateway(svc *Service) *HTTPGateway {
	return &HTTPGateway{
		svc:    svc,
		logger: log.New(log.Writer(), "http-api: ", log.LstdFlags),
	}
}

// Serve binds the gateway on listenAddr and blocks until ctx is cancelled.
// Production deployments terminate TLS at an ingress layer.
func (h *HTTPGateway) Serve(ctx context.Context, listenAddr string) error {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           h.withCORSAndLogging(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout — SSE responses are long-lived. Per-request
		// deadlines come from the client's connection (and context).
		IdleTimeout: 60 * time.Second,
	}
	errC := make(chan error, 1)
	go func() {
		h.logger.Printf("listening on %s", listenAddr)
		errC <- srv.ListenAndServe()
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
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /api/v1/auth/signin", h.signIn)
	mux.HandleFunc("POST /api/v1/auth/signout", h.signOut)
	mux.HandleFunc("GET /api/v1/whoami", h.requireAuth(h.whoami))
	mux.HandleFunc("GET /api/v1/workspaces", h.requireAuth(h.listWorkspaces))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.uploadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/drops", h.requireAuth(h.listModules))
	// Legacy alias — hzctl and older proxies still hit /modules. Keep
	// it pointing at the same handler so we can deprecate at our pace.
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(h.listModules))
	mux.HandleFunc("GET /api/v1/graphs", h.requireAuth(h.listGraphs))
	mux.HandleFunc("GET /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.loadGraph))
	mux.HandleFunc("PUT /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.saveGraph))
	mux.HandleFunc("POST /api/v1/graphs/{tenant}/{workspace}/{id}/run", h.requireAuth(h.runGraph))
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
	mux.HandleFunc("GET /api/v1/jobs/{jobID}", h.requireAuth(h.jobSnapshot))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/nodes/{nodeID}", h.requireAuth(h.nodeSnapshot))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/events", h.requireAuth(h.jobEvents))
	mux.HandleFunc("POST /api/v1/chat/stream", h.requireAuth(h.chatStream))
}

// ServeForTest exposes the mux without binding a port — analogous to
// ServeWebhookForTest. Tests build a Gateway, attach it to httptest.
func ServeForTest(h *HTTPGateway, rw http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	h.mountRoutes(mux)
	h.withCORSAndLogging(mux).ServeHTTP(rw, r)
}

// principalCtx is the type used to stash an authenticated principal
// onto the request context. requireAuth populates it; handlers extract it.
type principalCtx struct{}

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
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
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
		Secure:   r.TLS != nil,
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
		Secure:   r.TLS != nil,
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
	writeJSON(rw, http.StatusOK, map[string]string{"commit": commit, "graph_id": g.ID})
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
	if err := h.svc.RevokeAPIKey(r.Context(), p, r.PathValue("id")); err != nil {
		adminError(rw, err)
		return
	}
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
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
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
