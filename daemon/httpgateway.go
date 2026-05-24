package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// HTTPGateway exposes Service over JSON/HTTP so browsers and other
// non-gRPC clients can drive Hazy Flow. The endpoint surface is small
// on purpose — just enough to power a visual editor:
//
//	GET    /api/v1/modules                                    — list manifests
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
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(h.listModules))
	mux.HandleFunc("GET /api/v1/graphs", h.requireAuth(h.listGraphs))
	mux.HandleFunc("GET /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.loadGraph))
	mux.HandleFunc("PUT /api/v1/graphs/{tenant}/{workspace}/{id}", h.requireAuth(h.saveGraph))
	mux.HandleFunc("POST /api/v1/graphs/{tenant}/{workspace}/{id}/run", h.requireAuth(h.runGraph))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}", h.requireAuth(h.jobSnapshot))
	mux.HandleFunc("GET /api/v1/jobs/{jobID}/events", h.requireAuth(h.jobEvents))
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
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		token = strings.TrimSpace(token)
		if token == "" {
			writeJSONError(rw, http.StatusUnauthorized, "missing Authorization: Bearer <token>")
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

func (h *HTTPGateway) withCORSAndLogging(next http.Handler) http.Handler {
	allowed := "*"
	if len(h.AllowedOrigins) > 0 {
		allowed = strings.Join(h.AllowedOrigins, ", ")
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Access-Control-Allow-Origin", allowed)
		rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		rw.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// --- Handlers ---------------------------------------------------------

func (h *HTTPGateway) listModules(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	q := ModuleSearch{
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
	mans, err := h.svc.SearchModules(r.Context(), p, q)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"modules": mans})
}

func (h *HTTPGateway) listGraphs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.URL.Query().Get("tenant")
	workspace := r.URL.Query().Get("workspace")
	if tenant == "" || workspace == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant and workspace query params required")
		return
	}
	ids, err := h.svc.ListGraphs(r.Context(), p, tenant, workspace)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"graphs": ids})
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
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"commit": commit, "graph_id": g.ID})
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
			if ev.Terminal != nil {
				writeSSE(rw, "terminal", ev.Terminal)
				flusher.Flush()
				return
			}
		}
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

func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}
