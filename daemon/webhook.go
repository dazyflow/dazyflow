package daemon

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
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
// Hazyflow API key but do hold a per-integration secret.
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
	// Fire the published revision (falls back to HEAD for never-published
	// flows) so a live webhook runs what was deliberately published, not a
	// half-finished draft still being edited.
	g, err := store.LoadPublishedOrHead(graphID)
	if err != nil {
		http.Error(rw, "unknown graph", http.StatusNotFound)
		return
	}
	if g.Disabled {
		// Paused flows reject inbound webhooks. 403 rather than 404 so
		// the caller (e.g. Stripe's webhook UI) sees this as "we know
		// the endpoint but it's off" instead of an unknown-URL retry.
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return
	}

	keys := core.GraphWebhookSecrets(g)
	if len(keys) == 0 {
		http.Error(rw, "graph has no webhook trigger", http.StatusNotFound)
		return
	}
	// Accept the request if the bearer token matches ANY active key.
	// Every candidate is compared (no early break) so the work — and
	// thus the timing — doesn't depend on which key matched or how many
	// there are. This multi-key acceptance is what enables zero-downtime
	// rotation: add a new key, migrate callers, revoke the old one.
	provided := stripBearer(r.Header.Get("Authorization"))
	matched := 0
	for _, k := range keys {
		matched |= subtle.ConstantTimeCompare([]byte(k), []byte(provided))
	}
	if matched != 1 {
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
//   - application/json                  → map[string]any (parsed object) or whatever JSON.Unmarshal produces
//   - application/x-www-form-urlencoded → map[string]any (one entry per field, first value)
//   - text/* or no body                 → string
//   - everything else                   → []byte
//
// The form-urlencoded case reuses collectFormValues so a real HTML
// form (or a sender like Twilio/Slack that posts urlencoded) lands the
// same {key: value} object as the hosted form and a JSON webhook —
// ${trigger.body.email} works identically across all three paths.
//
// Headers are flattened to a string map (first value per name) so
// downstream nodes can read them with branch's field-path access.
func buildWebhookSeed(rawBody []byte, r *http.Request) core.Result {
	contentType := r.Header.Get("Content-Type")
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	// HTTP media types are case-insensitive (RFC 9110 §8.3.1), so a
	// sender using "Application/JSON" or "TEXT/PLAIN" must parse the same
	// as the lowercase form — otherwise it falls through to raw []byte
	// and ${trigger.body.field} silently breaks. Lowercase only this
	// comparison key; bodyMIME below keeps the original header verbatim.
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

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
	case mediaType == "application/x-www-form-urlencoded":
		if parsed, err := url.ParseQuery(string(rawBody)); err == nil {
			bodyValue = collectFormValues(nil, parsed)
		} else {
			// Malformed query string — hand the raw text to the graph
			// rather than fail the trigger, mirroring the JSON path.
			bodyValue = string(rawBody)
		}
	case strings.HasPrefix(mediaType, "text/"):
		bodyValue = string(rawBody)
	default:
		bodyValue = rawBody
	}

	headers := make(map[string]any, len(r.Header))
	for k, vs := range r.Header {
		// Never expose credential headers on the body's sibling port: the
		// Authorization header carries this graph's own webhook bearer
		// secret, and Cookie can carry session creds. A downstream node
		// that forwards ${trigger.headers} to an external service would
		// otherwise leak them. Drop both (canonicalized, case-insensitive).
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Cookie":
			continue
		}
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

// webhookSecret returns the bearer token guarding the graph's /trigger
// endpoint. Config lives on the webhook_input node now (the Triggers menu is
// gone); the secret is the node's `secret` param.
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

// ServeFormForTest is the hosted-form counterpart to
// ServeWebhookForTest — dispatches a request to the /form handler
// without binding a real port.
func ServeFormForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleForm(rw, r)
}

// CollectFormValuesForTest exposes the field-collection helper to the
// external _test package so the "extra fields aren't silently dropped"
// guarantee is unit-testable without standing up an HTTP server +
// graph run round trip.
func CollectFormValuesForTest(declared []string, posted url.Values) map[string]any {
	return collectFormValues(declared, posted)
}

// BuildWebhookSeedForTest exposes the Content-Type-driven body parser
// to the external _test package so the per-encoding decoding (JSON,
// form-urlencoded, text, raw) is unit-testable without a graph run.
func BuildWebhookSeedForTest(rawBody []byte, r *http.Request) core.Result {
	return buildWebhookSeed(rawBody, r)
}
