package homeassistant

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

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
