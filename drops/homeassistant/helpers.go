// Package homeassistant hosts the native Home Assistant connector: call a
// service (turn a light on, run a script, set a thermostat), read an entity's
// current state, and a poll-driven trigger that fires when a watched entity
// changes state.
//
// Auth + endpoint are a per-tenant ConnectionFields bundle (base_url + a
// long-lived access token), configured once on the integration page rather
// than typed on every node — exactly like ntfy's server+token. The engine
// injects the configured connection into each node's unset params at run time
// (injectConnectionDefaults), so flows carry only the per-use fields (which
// service, which entity).
//
// Home Assistant usually lives on the LAN (http://homeassistant.local:8123,
// 192.168.x.x). The connection's base_url is tenant-supplied, so every dial
// goes through the shared SSRF guard (net.SafeHTTPClient) — which refuses
// loopback/private/link-local targets UNLESS the operator opted in via
// HAZYFLOW_ALLOW_PRIVATE_EGRESS. That's the same posture the Postgres/MySQL
// drops take for private DB hosts: reaching a public Nabu Casa URL works out
// of the box; reaching a LAN instance needs the operator flag. The egress
// error message says exactly that.
//
// The state-changed trigger remembers the last state it emitted per (flow,
// node) via the cursor store the daemon wires at startup (SetCursorStore),
// the same mechanism google_form_trigger uses for its watermark.
package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile
// or buggy instance (reachable via the tenant-supplied base_url) can't OOM
// the daemon by streaming an unbounded body. A full /api/states dump on a
// large home is comfortably under this.
const maxResponseBytes = 16 << 20 // 16 MiB

// resolveConn reads the per-tenant connection the engine injected into the
// node's params: base_url (the instance root, e.g.
// http://homeassistant.local:8123) and token (a long-lived access token).
// Both come from ConnectionFields, so an empty value means the tenant hasn't
// connected Home Assistant yet — the error says exactly that.
func resolveConn(job core.Job) (base, token string, err error) {
	base = strings.TrimRight(strings.TrimSpace(params.StringDefault(job.Params, "base_url", "")), "/")
	token = strings.TrimSpace(params.StringDefault(job.Params, "token", ""))
	if base == "" || token == "" {
		return "", "", fmt.Errorf("Home Assistant isn't connected — add your instance URL and a long-lived access token on the Home Assistant integration page")
	}
	return base, token, nil
}

// haDo runs one authenticated Home Assistant REST call and returns the HTTP
// status + raw body. body is nil for GETs. The dial is SSRF-guarded: the
// base_url is tenant-supplied, so net.SafeHTTPClient refuses private/loopback
// targets unless the operator opted into private egress, and the egress
// allowlist (when set) bounds which hosts the token may be sent to.
func haDo(ctx context.Context, job core.Job, method, path string, body []byte) (int, []byte, error) {
	base, token, err := resolveConn(job)
	if err != nil {
		return 0, nil, err
	}
	url := base + path

	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("home assistant response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
}

// extractError pulls a human message out of a Home Assistant error body
// ({"message":"..."}), so "Entity not found." reaches the user instead of a
// bare HTTP status. Falls back to a truncated raw body.
func extractError(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		return e.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// httpFailure maps a transport error or a non-2xx response to an error
// Result, with a friendly code per HTTP class. Returns nil on success — the
// shared epilogue of every drop's haDo call. A 401 is the connected token
// being wrong/expired; a 404 from /api/states is an unknown entity_id.
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach your Home Assistant instance. It looks like a local/private address — the operator must enable private-network access (HAZYFLOW_ALLOW_PRIVATE_EGRESS) for hazyflow to reach it.",
				err.Error())
			return &r
		}
		r := params.Err(job, "ha_http_error", "Couldn't reach Home Assistant: "+err.Error())
		return &r
	}
	if status == 401 {
		r := params.Err(job, "auth", "Home Assistant rejected the access token (401). Re-create a long-lived access token and reconnect.")
		return &r
	}
	if status < 200 || status >= 300 {
		r := params.Err(job, "ha_error", fmt.Sprintf("Home Assistant returned %d: %s", status, extractError(body)))
		return &r
	}
	return nil
}

// textInputOr returns the text wired into input port `port` (string or raw
// bytes), or `fallback` when the port is unwired/empty. ok is false only when
// the port carries a NON-text value — a wiring mistake the caller rejects.
// Same pattern as slack/stripe/ntfy (a local copy per package, never a
// cross-import of another drop).
func textInputOr(job core.Job, port, fallback string) (val string, ok bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
}

// entityState is the shape Home Assistant returns from GET /api/states/<id>.
// State is the value people care about ("on", "23.5", "home"); Attributes
// carries everything else (brightness, friendly_name, unit_of_measurement);
// LastChanged advances only when State changes (LastUpdated also moves on
// attribute-only updates), which is what the state-changed trigger watermarks.
type entityState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
	LastUpdated string         `json:"last_updated"`
}

// friendlyName returns the entity's friendly_name attribute, falling back to
// the entity_id when unset — so result metadata and progress read naturally.
func (e entityState) friendlyName() string {
	if n, ok := e.Attributes["friendly_name"].(string); ok && n != "" {
		return n
	}
	return e.EntityID
}

// --- cursor (watermark) store, for the state-changed trigger ----------------

// CursorReader returns the stored value for an exact tenant/name, or
// ("", nil) when nothing has been stored yet (first observation).
// CursorWriter persists one. The daemon wires these to the encrypted secret
// store under a reserved "cursor." prefix (hidden from the Credentials UI)
// via SetCursorStore — mirrors gform.SetCursorStore.
type (
	CursorReader func(ctx context.Context, tenant, name string) (string, error)
	CursorWriter func(ctx context.Context, tenant, name, value string) error
)

var (
	cursorMu     sync.RWMutex
	cursorReader CursorReader
	cursorWriter CursorWriter
)

func SetCursorStore(r CursorReader, w CursorWriter) {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	cursorReader, cursorWriter = r, w
}

func readCursor(ctx context.Context, tenant, name string) string {
	cursorMu.RLock()
	r := cursorReader
	cursorMu.RUnlock()
	if r == nil {
		return ""
	}
	v, err := r(ctx, tenant, name)
	if err != nil {
		return "" // treat any read failure as "first observation"
	}
	return v
}

func writeCursor(ctx context.Context, tenant, name, value string) error {
	cursorMu.RLock()
	w := cursorWriter
	cursorMu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
}
