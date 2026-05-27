package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

const webhookInputModuleID = "webhook_input"

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

// ServeListener serves on an already-bound listener. Lets cmd/hzd bind
// on the main goroutine and fail-loud on a port-in-use error instead of
// the bind error vanishing into a background goroutine.
func (w *WebhookListener) ServeListener(ctx context.Context, ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", w.handleTrigger)
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errC := make(chan error, 1)
	go func() {
		w.logger.Printf("listening on %s", ln.Addr())
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

	// Read the body with a cap so an attacker can't OOM us by posting
	// 100 GB. We *do* read it now (the previous implementation
	// discarded it) so we can feed it to webhook_input nodes.
	var rawBody []byte
	if r.Body != nil {
		limited := io.LimitReader(r.Body, w.MaxBodyBytes+1)
		data, err := io.ReadAll(limited)
		_ = r.Body.Close()
		if err != nil {
			http.Error(rw, "read body", http.StatusBadRequest)
			return
		}
		if int64(len(data)) > w.MaxBodyBytes {
			http.Error(rw, fmt.Sprintf("body exceeds %d bytes", w.MaxBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		rawBody = data
	}

	// Build the seed result and apply it to every webhook_input node
	// the graph declares. Multiple webhook_input nodes is unusual but
	// not forbidden — they all receive the same payload.
	seed := buildWebhookSeed(rawBody, r)
	seeds := map[string]core.Result{}
	for _, n := range g.Nodes {
		if n.Module == webhookInputModuleID {
			seeds[n.ID] = seed
		}
	}

	// Fire the graph as a system principal scoped to the graph's
	// tenant. Trigger-driven runs bypass per-flow visibility because
	// possession of the per-graph webhook secret already proves
	// authorization — graph:admin lets the principal fire private
	// flows without owning them.
	principal := core.Principal{
		Subject:   "hazyflow-webhook",
		Tenant:    g.Tenant,
		Workspace: g.Workspace,
		Roles: []core.Role{{
			Name:        "webhook",
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
		}},
	}
	runID, err := w.svc.SubmitGraphWithSeed(r.Context(), principal, g, seeds)
	if err != nil {
		w.logger.Printf("submit %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
		return
	}
	w.logger.Printf("fired %s/%s/%s → %s (%d webhook_input seed(s))",
		tenant, workspace, graphID, runID, len(seeds))
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"job_id": runID})
}

// buildWebhookSeed constructs the Result that the webhook handler
// pre-completes webhook_input nodes with. The result has two output
// ports — body and headers — matching the webhook_input manifest.
//
// Body parsing follows Content-Type:
//   - application/json    → map[string]any (parsed object) or whatever JSON.Unmarshal produces
//   - text/* or no body   → string
//   - everything else     → []byte
//
// Headers are flattened to a string map (first value per name) so
// downstream nodes can read them with branch's field-path access.
func buildWebhookSeed(rawBody []byte, r *http.Request) core.Result {
	contentType := r.Header.Get("Content-Type")
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	mediaType = strings.TrimSpace(mediaType)

	var bodyValue any
	switch {
	case len(rawBody) == 0:
		bodyValue = ""
	case mediaType == "application/json":
		var parsed any
		if err := json.Unmarshal(rawBody, &parsed); err == nil {
			bodyValue = parsed
		} else {
			// Fall back to string when JSON is malformed — better to
			// let the graph see the raw text than fail the trigger.
			bodyValue = string(rawBody)
		}
	case strings.HasPrefix(mediaType, "text/"):
		bodyValue = string(rawBody)
	default:
		bodyValue = rawBody
	}

	headers := make(map[string]any, len(r.Header))
	for k, vs := range r.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}

	bodyMIME := contentType
	if bodyMIME == "" {
		bodyMIME = "text/plain"
	}
	return core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"body":    {MIME: bodyMIME, Inline: bodyValue},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}
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
