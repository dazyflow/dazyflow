package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// slackOnMentionModuleID identifies the trigger drop graph authors
// drop into their flow to subscribe to mentions. The events handler
// fans out to every graph in the tenant that has at least one node
// using this module.
const slackOnMentionModuleID = "slack_on_mention"

// maxSlackBodyBytes caps the body the handler accepts. Slack events
// are usually a few KB; the cap protects against an attacker who
// learned the signing secret from posting an arbitrarily large body.
const maxSlackBodyBytes = 256 * 1024 // 256 KiB

// slackSignatureMaxSkew is how far apart the request timestamp can
// be from server time before the request is treated as a replay.
// Five minutes matches Slack's published guidance.
const slackSignatureMaxSkew = 5 * time.Minute

// SlackEventsHandler verifies Slack Events API requests and dispatches
// app_mention events to every graph in the tenant that uses the
// slack_on_mention trigger.
//
// Routing layout:
//
//	POST /api/v1/events/slack/{tenant}
//	X-Slack-Signature: v0=<hmac-sha256-hex>
//	X-Slack-Request-Timestamp: <unix-seconds>
//	(body — JSON envelope)
//
// Auth: Slack's HMAC signature is the only auth. No bearer token —
// Slack POSTs as a stranger. The handler MUST refuse any request
// whose signature doesn't match, whose timestamp is older than ~5
// minutes (replay window), or whose URL tenant has no subscribed
// graphs.
//
// Two request shapes handled:
//
//   - type=url_verification — Slack's first-subscription challenge.
//     The handler echoes back the `challenge` string as plain text;
//     this proves the daemon controls the endpoint.
//   - type=event_callback — a wrapped event. We extract event.team_id
//     into the seed and SubmitGraphWithSeed against every graph in
//     the tenant with a slack_on_mention node. Multiple graphs in
//     the tenant each get their own run for the same event — a fan-
//     out the user can intentionally use (e.g. one graph routes
//     mentions to a ticketing system, another to a #notify channel).
type SlackEventsHandler struct {
	svc           *Service
	signingSecret string
	logger        *log.Logger

	// now is overridable for testing the replay-window guard.
	now func() time.Time
}

// NewSlackEventsHandler wires a handler against the daemon Service.
// signingSecret is the Slack app's "Signing Secret" from
// https://api.slack.com/apps → Basic Information. Empty disables the
// endpoint — POSTs return 501 so misconfiguration shows up clearly
// rather than silently rejecting events as bad signatures.
func NewSlackEventsHandler(svc *Service, signingSecret string) *SlackEventsHandler {
	return &SlackEventsHandler{
		svc:           svc,
		signingSecret: signingSecret,
		logger:        log.New(log.Writer(), "slack-events: ", log.LstdFlags),
		now:           time.Now,
	}
}

// ServeHTTP routes a single Slack event POST. Mounted at
// `/api/v1/events/slack/{tenant}` by HTTPGateway.
func (h *SlackEventsHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.signingSecret == "" {
		http.Error(rw, "Slack events endpoint not configured (set --slack-signing-secret)", http.StatusNotImplemented)
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "" {
		http.Error(rw, "expected /api/v1/events/slack/<tenant>", http.StatusBadRequest)
		return
	}

	// Read the body before signature verification so we have the
	// exact bytes Slack signed. Slack's signature is over the raw
	// body string concatenated with the timestamp; re-marshalling
	// the JSON would change the bytes.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSlackBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "read body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxSlackBodyBytes {
		http.Error(rw, fmt.Sprintf("body exceeds %d bytes", maxSlackBodyBytes), http.StatusRequestEntityTooLarge)
		return
	}

	if err := h.verifySignature(r.Header, body); err != nil {
		// 401 keeps the error generic — exposing "stale timestamp" vs
		// "bad signature" would help an attacker calibrate. Detailed
		// reason goes to the log only.
		h.logger.Printf("reject %s: %v", tenant, err)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	// Decode just enough to dispatch — keep the original body around
	// for the per-graph event seed.
	var env slackEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(rw, fmt.Sprintf("parse: %v", err), http.StatusBadRequest)
		return
	}

	switch env.Type {
	case "url_verification":
		// Slack's first-subscription handshake. Echo the challenge
		// back as plain text — Slack reads response body verbatim,
		// not JSON. This is the only response type that doesn't
		// require a graph match (the URL hasn't been used yet, the
		// user may not have built a graph at this point).
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte(env.Challenge))
		return
	case "event_callback":
		h.dispatchEvent(r.Context(), tenant, env, rw)
		return
	default:
		// Unknown envelope types (reaction_removed, etc.) get a
		// success ack so Slack doesn't retry. Returning 200 without
		// dispatch is the right shape — we just don't subscribe to
		// this event type.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

// slackEventEnvelope is the outermost wrapper Slack sends. Inner
// `event` shape varies per event type; we decode it lazily once we
// know we care about it.
type slackEventEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge,omitempty"`
	TeamID    string          `json:"team_id,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
}

// slackAppMentionEvent is the shape of the `event` field for
// app_mention. We extract the named ports up front; the raw JSON
// also goes through to the `event` output port for graphs that need
// fields we didn't pull out.
type slackAppMentionEvent struct {
	Type    string `json:"type"`
	User    string `json:"user"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// dispatchEvent fans an event_callback out to every graph in the
// tenant with at least one slack_on_mention node. Each graph gets
// its own run with that node pre-completed.
func (h *SlackEventsHandler) dispatchEvent(_ context.Context, tenant string, env slackEventEnvelope, rw http.ResponseWriter) {
	// V1 only handles app_mention. Other event types could share
	// this dispatcher later by branching on event.type and emitting
	// different outputs — keeping the trigger drop's manifest
	// stable (text/user/channel/team/ts/event are the common shape).
	var ev slackAppMentionEvent
	if err := json.Unmarshal(env.Event, &ev); err != nil {
		http.Error(rw, fmt.Sprintf("parse event: %v", err), http.StatusBadRequest)
		return
	}
	if ev.Type != "app_mention" {
		// Acknowledged but not dispatched — same handling as unknown
		// outer types above.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
		return
	}

	// Build the seed once. Every graph that subscribes gets the
	// same seed — the trigger node's outputs match the manifest's
	// declared ports exactly.
	var rawEvent any
	_ = json.Unmarshal(env.Event, &rawEvent) // best-effort; signature was already validated
	seed := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":    {MIME: "text/plain", Inline: ev.Text},
			"user":    {MIME: "text/plain", Inline: ev.User},
			"channel": {MIME: "text/plain", Inline: ev.Channel},
			"team":    {MIME: "text/plain", Inline: env.TeamID},
			"ts":      {MIME: "text/plain", Inline: ev.TS},
			"event":   {MIME: "application/json", Inline: rawEvent},
		},
	}

	// Slack expects a fast response — they retry on >3s. Spawn the
	// fanout in a background goroutine and ack immediately. Errors
	// during fanout go to the daemon log; the user sees them on
	// the run record.
	go h.fanoutSeed(context.Background(), tenant, ev.Channel, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

// fanoutSeed walks every workspace under the tenant, loads each
// graph, and submits a run for any that declares a slack_on_mention
// node WHOSE channel_filter param matches the event's channel (or
// has no filter set). Per-node filtering at the gateway cuts the
// worker-side churn for tenants who run many channel-specific
// graphs against one Slack app.
//
// eventChannel is the Slack channel ID the mention happened in
// (e.g. C0123). channel_filter param semantics: empty/missing =
// match anything; non-empty = exact match required. A graph with
// multiple slack_on_mention nodes only includes the ones whose
// filter matches — others stay dormant for this event.
func (h *SlackEventsHandler) fanoutSeed(ctx context.Context, tenant, eventChannel string, seed core.Result) {
	workspaces, err := h.svc.Workspaces.List(tenant)
	if err != nil {
		h.logger.Printf("list workspaces for %s: %v", tenant, err)
		return
	}
	// Use a system principal for the dispatch — possession of the
	// signing secret already proves authorization, same model the
	// webhook listener uses (graph:admin lets the principal fire
	// private flows without owning them).
	principal := core.Principal{
		Subject: "hazyflow-slack-events",
		Tenant:  tenant,
		Roles: []core.Role{{
			Name:        "slack-events",
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
		}},
	}
	for _, ws := range workspaces {
		store, err := h.svc.Workspaces.Open(tenant, ws)
		if err != nil {
			h.logger.Printf("open %s/%s: %v", tenant, ws, err)
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			h.logger.Printf("list graphs %s/%s: %v", tenant, ws, err)
			continue
		}
		principal.Workspace = ws
		for _, id := range ids {
			// Match + run the published revision (HEAD fallback for
			// never-published flows): an external event fires the version
			// that was deliberately published, not a draft.
			g, err := store.LoadPublishedOrHead(id)
			if err != nil {
				h.logger.Printf("load %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			seeds := map[string]core.Result{}
			for _, n := range g.Nodes {
				if n.Module != slackOnMentionModuleID {
					continue
				}
				if !nodeChannelFilterMatches(n.Params, eventChannel) {
					continue
				}
				seeds[n.ID] = seed
			}
			if len(seeds) == 0 {
				continue
			}
			runID, err := h.svc.SubmitGraphWithSeed(ctx, principal, g, seeds)
			if err != nil {
				h.logger.Printf("submit %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			h.logger.Printf("fired %s/%s/%s → %s (%d slack_on_mention seed(s))",
				tenant, ws, id, runID, len(seeds))
		}
	}
}

// nodeChannelFilterMatches checks the slack_on_mention node's
// channel_filter param against the event's channel. Empty filter
// (or missing param, or non-string value) matches every channel —
// preserves backward compatibility with graphs authored before this
// param existed.
func nodeChannelFilterMatches(params map[string]any, eventChannel string) bool {
	if params == nil {
		return true
	}
	raw, ok := params["channel_filter"]
	if !ok {
		return true
	}
	f, ok := raw.(string)
	if !ok || f == "" {
		return true
	}
	return f == eventChannel
}

// verifySignature implements the Slack signing-secret scheme:
//
//	base   = "v0:" + timestamp + ":" + body
//	sig    = "v0=" + hex(hmac-sha256(signingSecret, base))
//	header X-Slack-Signature must equal sig (constant-time)
//
// Plus a replay window: reject if the timestamp is more than
// ~5 minutes off from server time. See:
// https://api.slack.com/authentication/verifying-requests-from-slack
func (h *SlackEventsHandler) verifySignature(header http.Header, body []byte) error {
	tsStr := header.Get("X-Slack-Request-Timestamp")
	sig := header.Get("X-Slack-Signature")
	if tsStr == "" || sig == "" {
		return fmt.Errorf("missing signature headers")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp: %w", err)
	}
	if skew := h.now().Unix() - ts; skew > int64(slackSignatureMaxSkew.Seconds()) || skew < -int64(slackSignatureMaxSkew.Seconds()) {
		return fmt.Errorf("timestamp skew %ds outside replay window", skew)
	}
	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte("v0:"))
	mac.Write([]byte(tsStr))
	mac.Write([]byte(":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	// Constant-time compare on the FULL header value, including the
	// "v0=" prefix, so timing can't leak whether the version prefix
	// was right.
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(sig))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
