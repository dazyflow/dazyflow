package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// WebhookListener exposes an HTTP endpoint per graph that has a webhook
// trigger. Layout:
//
//	POST /trigger/<tenant>/<workspace>/<graph-id>
//	Authorization: Bearer <secret-from-graph-trigger>
//	(body — passed to the graph as a webhook_input record if present)
//
// Responses:
//
//	200 + JSON {job_id: "..."} on accepted fire
//	401 on bad secret
//	404 on unknown graph or graph without webhook trigger
//	400 on malformed paths
//
// The listener authenticates via the per-graph trigger secret rather
// than the daemon's normal API-key chain. That's intentional: webhook
// callers (Stripe, GitHub, your CI provider) typically don't have a
// Hazy Flow API key but do hold a per-integration secret.
type WebhookListener struct {
	svc    *Service
	logger *log.Logger

	// MaxBodyBytes caps the inline body included in graph input.
	MaxBodyBytes int64
}

func NewWebhookListener(svc *Service) *WebhookListener {
	return &WebhookListener{
		svc:          svc,
		logger:       log.New(log.Writer(), "webhook: ", log.LstdFlags),
		MaxBodyBytes: 1 * 1024 * 1024, // 1 MiB default
	}
}

// Serve blocks until ctx is cancelled. It binds the HTTP server on
// listenAddr and routes /trigger/* requests through the daemon's
// Service.SubmitGraph. The listener is intentionally tiny — no admin
// UI, no metrics, no rate-limiting yet (TODO).
func (w *WebhookListener) Serve(ctx context.Context, listenAddr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", w.handleTrigger)
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errC := make(chan error, 1)
	go func() {
		w.logger.Printf("listening on %s", listenAddr)
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

func (w *WebhookListener) handleTrigger(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/trigger/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /trigger/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.Error(rw, "unknown workspace", http.StatusNotFound)
		return
	}
	g, err := store.Load(graphID)
	if err != nil {
		http.Error(rw, "unknown graph", http.StatusNotFound)
		return
	}

	secret := webhookSecret(g)
	if secret == "" {
		http.Error(rw, "graph has no webhook trigger", http.StatusNotFound)
		return
	}
	provided := stripBearer(r.Header.Get("Authorization"))
	if subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) != 1 {
		http.Error(rw, "invalid secret", http.StatusUnauthorized)
		return
	}

	// Body is read with a cap so an attacker can't fill our memory by
	// posting 100 GB. The body is currently ignored — surfacing it as
	// a graph input requires a dedicated webhook_input module (TODO).
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, w.MaxBodyBytes))
		_ = r.Body.Close()
	}

	// Fire the graph as a system principal scoped to the graph's tenant.
	principal := core.Principal{
		Subject:   "hazyflow-webhook",
		Tenant:    g.Tenant,
		Workspace: g.Workspace,
		Roles: []core.Role{{
			Name:        "webhook",
			Permissions: []core.Permission{core.PermGraphRun},
		}},
	}
	runID, err := w.svc.SubmitGraph(r.Context(), principal, g)
	if err != nil {
		w.logger.Printf("submit %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
		return
	}
	w.logger.Printf("fired %s/%s/%s → %s", tenant, workspace, graphID, runID)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"job_id": runID})
}

func webhookSecret(g core.Graph) string {
	for _, t := range g.Triggers {
		if t.Type == "webhook" && t.Secret != "" {
			return t.Secret
		}
	}
	return ""
}

func stripBearer(h string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return strings.TrimSpace(h)
}

// ServeWebhookForTest exposes the listener's per-request handler without
// binding a real port — used by tests that want to assert HTTP-level
// behaviour via httptest. Production code should use Serve.
func ServeWebhookForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleTrigger(rw, r)
}
