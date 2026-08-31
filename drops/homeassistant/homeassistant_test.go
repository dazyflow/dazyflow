// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// TestMain opts this package's tests into private-network egress. The drops
// dial through net.SafeHTTPClient, whose SSRF guard blocks loopback unless
// the operator opts in; the tests point at a 127.0.0.1 httptest server, so
// they need the same opt-in production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// connParams returns the per-tenant connection params the engine would inject
// (base_url + token), pointed at a test server.
func connParams(base string, extra map[string]any) map[string]any {
	p := map[string]any{"base_url": base, "token": "test-llat"}
	maps.Copy(p, extra)
	return p
}

func TestCallService_PostsServiceAndEntity(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"entity_id":"light.living_room","state":"on","attributes":{"brightness":128}}]`)
	}))
	defer srv.Close()

	job := core.Job{
		ID:     "j1",
		Params: connParams(srv.URL, map[string]any{"service": "light.turn_on", "entity_id": "light.living_room", "data": map[string]any{"brightness_pct": 50.0}}),
	}
	res, err := executeCallService(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	if gotPath != "/api/services/light/turn_on" {
		t.Errorf("path = %q, want /api/services/light/turn_on", gotPath)
	}
	if gotAuth != "Bearer test-llat" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["entity_id"] != "light.living_room" {
		t.Errorf("body entity_id = %v", gotBody["entity_id"])
	}
	if gotBody["brightness_pct"] != 50.0 {
		t.Errorf("body brightness_pct = %v, want 50 (from data)", gotBody["brightness_pct"])
	}
	// The targeted entity is re-emitted for chaining into a status check.
	if res.Output["entity_id"].Inline != "light.living_room" {
		t.Errorf("entity_id output = %v, want light.living_room", res.Output["entity_id"].Inline)
	}
}

func TestCallService_OmitsEntityOutputWhenNoEntity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	// An entity-less service (e.g. notify.notify) emits no entity pin, so the
	// downstream edge stays dormant.
	job := core.Job{ID: "j", Params: connParams(srv.URL, map[string]any{"service": "notify.notify"})}
	res, _ := executeCallService(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status %v: %+v", res.Status, res.Error)
	}
	if _, present := res.Output["entity_id"]; present {
		t.Errorf("no entity targeted → entity_id pin should be omitted, got %v", res.Output["entity_id"])
	}
}

func TestCallService_RejectsServiceWithoutDomain(t *testing.T) {
	job := core.Job{ID: "j", Params: connParams("http://x", map[string]any{"service": "turn_on"})}
	res, _ := executeCallService(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %v %+v", res.Status, res.Error)
	}
}

func TestCallService_InputOverridesParam(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()

	job := core.Job{
		ID:     "j",
		Params: connParams(srv.URL, map[string]any{"service": "light.turn_off"}),
		Input:  map[string]core.Ref{"service": {Inline: "switch.toggle"}},
	}
	res, _ := executeCallService(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status %v: %+v", res.Status, res.Error)
	}
	if gotPath != "/api/services/switch/toggle" {
		t.Errorf("wired input should override param; path = %q", gotPath)
	}
}

func TestGetState_EmitsStateAndAttributes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states/sensor.temp" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"entity_id":"sensor.temp","state":"21.5","attributes":{"unit_of_measurement":"°C","friendly_name":"Temp"},"last_changed":"2026-06-16T10:00:00Z"}`)
	}))
	defer srv.Close()

	job := core.Job{ID: "j", Params: connParams(srv.URL, map[string]any{"entity_id": "sensor.temp"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status %v: %+v", res.Status, res.Error)
	}
	if got := res.Output["state"].Inline; got != "21.5" {
		t.Errorf("state = %v, want 21.5", got)
	}
	attrs, ok := res.Output["attributes"].Inline.(map[string]any)
	if !ok || attrs["unit_of_measurement"] != "°C" {
		t.Errorf("attributes = %v", res.Output["attributes"].Inline)
	}
}

func TestGetState_404IsFriendly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"Entity not found."}`)
	}))
	defer srv.Close()

	job := core.Job{ID: "j", Params: connParams(srv.URL, map[string]any{"entity_id": "sensor.nope"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "not_found" {
		t.Fatalf("want not_found, got %v %+v", res.Status, res.Error)
	}
}

func TestGetState_401IsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	job := core.Job{ID: "j", Params: connParams(srv.URL, map[string]any{"entity_id": "x.y"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("want auth, got %v %+v", res.Status, res.Error)
	}
}

// memCursor is an in-memory cursor store for the trigger tests.
type memCursor struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *memCursor) read(_ context.Context, tenant, name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[tenant+"|"+name], nil
}

func (c *memCursor) write(_ context.Context, tenant, name, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]string{}
	}
	c.m[tenant+"|"+name] = value
	return nil
}

func TestStateChanged_FirstObservationDoesNotFire(t *testing.T) {
	store := &memCursor{}
	SetCursorStore(store.read, store.write)
	defer SetCursorStore(nil, nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"entity_id":"binary_sensor.door","state":"off","attributes":{},"last_changed":"2026-06-16T10:00:00Z"}`)
	}))
	defer srv.Close()

	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t", Params: connParams(srv.URL, map[string]any{"entity_id": "binary_sensor.door"})}
	res, _ := executeStateChanged(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status %v: %+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Fatalf("first observation should emit nothing, got %v", res.Output)
	}
	// The watermark should have been recorded.
	if v, _ := store.read(context.Background(), "t", "cursor.homeassistant.g.n"); v == "" {
		t.Fatalf("expected cursor to be stored on first observation")
	}
}

func TestStateChanged_FiresOnChangeWithPrevious(t *testing.T) {
	store := &memCursor{}
	SetCursorStore(store.read, store.write)
	defer SetCursorStore(nil, nil)

	state := `{"entity_id":"binary_sensor.door","state":"off","attributes":{},"last_changed":"2026-06-16T10:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, state)
	}))
	defer srv.Close()

	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t", Params: connParams(srv.URL, map[string]any{"entity_id": "binary_sensor.door"})}

	// First poll: records "off", no fire.
	if res, _ := executeStateChanged(context.Background(), job, nil); len(res.Output) != 0 {
		t.Fatalf("first poll should not fire")
	}
	// Second poll, same state: still no fire.
	if res, _ := executeStateChanged(context.Background(), job, nil); len(res.Output) != 0 {
		t.Fatalf("unchanged poll should not fire")
	}
	// Door opens.
	state = `{"entity_id":"binary_sensor.door","state":"on","attributes":{"friendly_name":"Front Door"},"last_changed":"2026-06-16T10:05:00Z"}`
	res, _ := executeStateChanged(context.Background(), job, nil)
	if res.Status != core.StatusOK || len(res.Output) == 0 {
		t.Fatalf("change should fire; got %v %+v", res.Status, res.Error)
	}
	if res.Output["state"].Inline != "on" {
		t.Errorf("state = %v, want on", res.Output["state"].Inline)
	}
	if res.Output["previous_state"].Inline != "off" {
		t.Errorf("previous_state = %v, want off", res.Output["previous_state"].Inline)
	}
	// And it doesn't re-fire on the next identical poll.
	if r2, _ := executeStateChanged(context.Background(), job, nil); len(r2.Output) != 0 {
		t.Fatalf("should not re-fire the same change")
	}
}

func TestVerify_OKAndBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"message":"API running."}`)
	}))
	defer srv.Close()

	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": srv.URL, "token": "good"}); err != nil {
		t.Errorf("good creds should verify: %v", err)
	}
	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": srv.URL, "token": "bad"}); err == nil {
		t.Errorf("bad token should fail verification")
	}
	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": "", "token": "x"}); err == nil {
		t.Errorf("missing URL should fail")
	}
}

func TestHaDo_SSRFGuardBlocksPrivateWhenOptedOut(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	job := core.Job{ID: "j", Params: map[string]any{"base_url": "http://127.0.0.1:9", "token": "t"}}
	_, _, err := haDo(context.Background(), job, "GET", "/api/", nil)
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}

func TestListEntities_FriendlyNamesSorted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[
			{"entity_id":"light.living_room","state":"on","attributes":{"friendly_name":"Living Room Light"}},
			{"entity_id":"binary_sensor.front_door","state":"off","attributes":{}}
		]`)
	}))
	defer srv.Close()

	items, err := ListEntities(context.Background(), core.Job{Params: connParams(srv.URL, nil)})
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 entities, got %d", len(items))
	}
	// Sorted by name: "binary_sensor.front_door" (no friendly_name → its id)
	// sorts before "Living Room Light".
	if items[0].ID != "binary_sensor.front_door" || items[0].Name != "binary_sensor.front_door" {
		t.Errorf("entity[0] = %+v (want id+name = entity_id fallback)", items[0])
	}
	if items[1].ID != "light.living_room" || items[1].Name != "Living Room Light" {
		t.Errorf("entity[1] = %+v", items[1])
	}
}

func TestListServices_DomainServiceIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[
			{"domain":"light","services":{"turn_on":{"name":"Turn on"},"turn_off":{"name":"Turn off"}}},
			{"domain":"input_boolean","services":{"toggle":{}}}
		]`)
	}))
	defer srv.Close()

	items, err := ListServices(context.Background(), core.Job{Params: connParams(srv.URL, nil)})
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	byID := map[string]string{}
	for _, it := range items {
		byID[it.ID] = it.Name
	}
	if byID["light.turn_on"] != "Light: Turn on" {
		t.Errorf("light.turn_on = %q, want 'Light: Turn on'", byID["light.turn_on"])
	}
	// No friendly name → falls back to the service key; domain underscores → space.
	if byID["input_boolean.toggle"] != "Input boolean: toggle" {
		t.Errorf("input_boolean.toggle = %q", byID["input_boolean.toggle"])
	}
}

func TestResolveConn_RequiresBoth(t *testing.T) {
	if _, _, err := resolveConn(core.Job{Params: map[string]any{"base_url": "http://x"}}); err == nil {
		t.Errorf("missing token should error")
	}
	if _, _, err := resolveConn(core.Job{Params: map[string]any{"token": "t"}}); err == nil {
		t.Errorf("missing base_url should error")
	}
}

func TestExtractError(t *testing.T) {
	// A Home Assistant error body carries the human message under "message".
	if got := extractError([]byte(`{"message":"Entity not found."}`)); got != "Entity not found." {
		t.Errorf("message body = %q, want 'Entity not found.'", got)
	}
	// Non-JSON (or JSON without a message) falls back to the trimmed raw body.
	if got := extractError([]byte("  not json  ")); got != "not json" {
		t.Errorf("raw fallback = %q, want 'not json'", got)
	}
	// JSON with an empty message also falls back to the raw body.
	if got := extractError([]byte(`{"message":""}`)); got != `{"message":""}` {
		t.Errorf("empty-message fallback = %q", got)
	}
	// An over-long body is truncated to 300 bytes so it can't flood the result.
	long := strings.Repeat("x", 500)
	if got := extractError([]byte(long)); len(got) != 300 {
		t.Errorf("truncated len = %d, want 300", len(got))
	}
}

func TestGetState_GenericErrorSurfacesMessage(t *testing.T) {
	// A non-2xx that isn't 401/404 maps to ha_error, and the body's message is
	// surfaced through extractError so the user sees what went wrong.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"message":"template error"}`)
	}))
	defer srv.Close()

	job := core.Job{ID: "j", Params: connParams(srv.URL, map[string]any{"entity_id": "sensor.x"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "ha_error" {
		t.Fatalf("want ha_error, got %v %+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "template error") {
		t.Errorf("message %q should include the HA error detail", res.Error.Message)
	}
}

func TestGetState_EgressBlockedIsFriendly(t *testing.T) {
	// With private egress off, a LAN base_url trips the SSRF guard; httpFailure
	// turns that into the operator-facing egress_blocked code.
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)

	job := core.Job{ID: "j", Params: connParams("http://127.0.0.1:9", map[string]any{"entity_id": "sensor.x"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Fatalf("want egress_blocked, got %v %+v", res.Status, res.Error)
	}
}

func TestCallService_NonTextInputRejected(t *testing.T) {
	// A wired 'Service' port carrying a non-text value is a wiring mistake, not
	// a service name — textInputOr returns ok=false and the call is rejected.
	job := core.Job{
		ID:     "j",
		Params: connParams("http://x", map[string]any{"service": "light.turn_on"}),
		Input:  map[string]core.Ref{"service": {Inline: 42}},
	}
	res, _ := executeCallService(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input, got %v %+v", res.Status, res.Error)
	}
}

func TestCallService_BadDataParam(t *testing.T) {
	// 'data' must be an object of service options; a scalar is rejected before
	// any request goes out.
	job := core.Job{
		ID:     "j",
		Params: connParams("http://x", map[string]any{"service": "light.turn_on", "data": "nope"}),
	}
	res, _ := executeCallService(context.Background(), job, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %v %+v", res.Status, res.Error)
	}
}

func TestHaDo_RejectsOversizedBody(t *testing.T) {
	// A hostile/buggy instance streaming an unbounded body is capped: haDo
	// errors past maxResponseBytes rather than buffering it all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20) // 1 MiB of zeros per write
		for written := 0; written <= maxResponseBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	job := core.Job{ID: "j", Params: connParams(srv.URL, nil)}
	_, _, err := haDo(context.Background(), job, "GET", "/api/states", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized body should be rejected, got %v", err)
	}
}

func TestCovHaDoMissingConn(t *testing.T) {
	// resolveConn fails → haDo returns the error before dialing.
	_, _, err := haDo(context.Background(), core.Job{Params: map[string]any{}}, "GET", "/api/", nil)
	if err == nil {
		t.Fatal("missing connection should error")
	}
}

func TestCovHttpFailureNonSSRFTransport(t *testing.T) {
	// A plain transport error (not SSRF) maps to ha_http_error.
	f := httpFailure(core.Job{}, 0, nil, errors.New("dial tcp: connection refused"))
	if f == nil || f.Error.Code != "ha_http_error" {
		t.Fatalf("want ha_http_error, got %+v", f)
	}
	// 200 → nil.
	if httpFailure(core.Job{}, 200, []byte("[]"), nil) != nil {
		t.Fatal("2xx should be nil")
	}
}

func TestCovCursorNilStore(t *testing.T) {
	SetCursorStore(nil, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	if got := readCursor(context.Background(), "t", "n"); got != "" {
		t.Fatalf("nil reader → empty, got %q", got)
	}
	if err := writeCursor(context.Background(), "t", "n", "v"); err != nil {
		t.Fatalf("nil writer → nil err, got %v", err)
	}
}

func TestCovReadCursorError(t *testing.T) {
	reader := func(_ context.Context, _, _ string) (string, error) {
		return "", errors.New("store down")
	}
	SetCursorStore(reader, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	if got := readCursor(context.Background(), "t", "n"); got != "" {
		t.Fatalf("read error → treat as first observation, got %q", got)
	}
}

func TestCovReadStoredCursorUnparseable(t *testing.T) {
	reader := func(_ context.Context, _, _ string) (string, error) { return "not json", nil }
	SetCursorStore(reader, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	if c := readStoredCursor(context.Background(), "t", "n"); c != nil {
		t.Fatalf("unparseable cursor → nil, got %+v", c)
	}
}

func TestCovStateChangedBadParam(t *testing.T) {
	res, _ := executeStateChanged(context.Background(), core.Job{Params: connParams("http://x", nil)}, nil)
	if res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", res.Error)
	}
}

func TestCovStateChanged404(t *testing.T) {
	SetCursorStore(nil, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"message":"Entity not found."}`)
	}))
	defer srv.Close()
	job := core.Job{Params: connParams(srv.URL, map[string]any{"entity_id": "x.y"})}
	res, _ := executeStateChanged(context.Background(), job, nil)
	if res.Error == nil || res.Error.Code != "not_found" {
		t.Fatalf("want not_found, got %+v", res.Error)
	}
}

func TestCovStateChangedBadJSON(t *testing.T) {
	SetCursorStore(nil, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv.Close()
	job := core.Job{Params: connParams(srv.URL, map[string]any{"entity_id": "x.y"})}
	res, _ := executeStateChanged(context.Background(), job, nil)
	if res.Error == nil || res.Error.Code != "ha_error" {
		t.Fatalf("want ha_error, got %+v", res.Error)
	}
}

func TestCovGetStateBadInput(t *testing.T) {
	job := core.Job{Params: connParams("http://x", nil), Input: map[string]core.Ref{"entity_id": {Inline: 7}}}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Error == nil || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input, got %+v", res.Error)
	}
}

func TestCovGetStateMissingEntity(t *testing.T) {
	res, _ := executeGetState(context.Background(), core.Job{Params: connParams("http://x", nil)}, nil)
	if res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", res.Error)
	}
}

func TestCovGetStateBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv.Close()
	job := core.Job{Params: connParams(srv.URL, map[string]any{"entity_id": "x.y"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Error == nil || res.Error.Code != "ha_error" {
		t.Fatalf("want ha_error, got %+v", res.Error)
	}
}

func TestCovGetStateNilAttributes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"entity_id":"sensor.x","state":"5"}`)
	}))
	defer srv.Close()
	job := core.Job{Params: connParams(srv.URL, map[string]any{"entity_id": "sensor.x"})}
	res, _ := executeGetState(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status %v %+v", res.Status, res.Error)
	}
	if m, ok := res.Output["attributes"].Inline.(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("nil attributes should become empty map, got %v", res.Output["attributes"].Inline)
	}
}

func TestCovListEntitiesErrors(t *testing.T) {
	// Transport error (no connection).
	if _, err := ListEntities(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("missing conn should error")
	}
	// Non-2xx status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	defer srv.Close()
	if _, err := ListEntities(context.Background(), core.Job{Params: connParams(srv.URL, nil)}); err == nil {
		t.Fatal("500 should error")
	}
	// Bad JSON.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv2.Close()
	if _, err := ListEntities(context.Background(), core.Job{Params: connParams(srv2.URL, nil)}); err == nil {
		t.Fatal("bad json should error")
	}
}

func TestCovListEntitiesSkipsBlankID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"entity_id":"","state":"x"},{"entity_id":"light.a","state":"on","attributes":{"friendly_name":"A"}}]`)
	}))
	defer srv.Close()
	items, err := ListEntities(context.Background(), core.Job{Params: connParams(srv.URL, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "light.a" {
		t.Fatalf("blank entity_id should be skipped, got %+v", items)
	}
}

func TestCovListServicesErrors(t *testing.T) {
	if _, err := ListServices(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("missing conn should error")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = io.WriteString(w, `down`)
	}))
	defer srv.Close()
	if _, err := ListServices(context.Background(), core.Job{Params: connParams(srv.URL, nil)}); err == nil {
		t.Fatal("503 should error")
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "not json")
	}))
	defer srv2.Close()
	if _, err := ListServices(context.Background(), core.Job{Params: connParams(srv2.URL, nil)}); err == nil {
		t.Fatal("bad json should error")
	}
}

func TestCovTitleizeDomain(t *testing.T) {
	if titleizeDomain("") != "" {
		t.Fatal("empty")
	}
	if titleizeDomain("light") != "Light" {
		t.Fatal("light")
	}
	if titleizeDomain("input_boolean") != "Input boolean" {
		t.Fatal("underscore")
	}
}

func TestCovVerifyHomeAssistant(t *testing.T) {
	// Missing URL / token.
	if err := verifyHomeAssistant(context.Background(), map[string]string{"token": "t"}); err == nil {
		t.Fatal("missing url")
	}
	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": "http://x"}); err == nil {
		t.Fatal("missing token")
	}
	// 500 server error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": srv.URL, "token": "t"}); err == nil {
		t.Fatal("500 should fail verification")
	}
	// Network error (closed server).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv2.URL
	srv2.Close()
	if err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": addr, "token": "t"}); err == nil {
		t.Fatal("unreachable should fail verification")
	}
}

func TestCovVerifyEgressBlocked(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(true) })
	err := verifyHomeAssistant(context.Background(), map[string]string{"base_url": "http://127.0.0.1:9", "token": "t"})
	if err == nil {
		t.Fatal("private address should be blocked by egress guard")
	}
}
