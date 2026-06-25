package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// decodeEnvelope decodes a response body as the structured ErrorEnvelope and
// fails if it isn't that shape — the whole point of the unified error surface.
func decodeEnvelope(t *testing.T, body []byte) ErrorEnvelope {
	t.Helper()
	var env ErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("body is not an ErrorEnvelope: %v (body=%s)", err, body)
	}
	if env.Error.Code == "" || env.Error.Message == "" {
		t.Fatalf("envelope missing code/message: %+v (body=%s)", env, body)
	}
	return env
}

// TestErrorEnvelope_Unified pins the §1 invariant that every API error path
// emits ONE shape — {"error":{"code","message",...}} — whether it comes from
// writeAPIError (specific code), writeJSONError (status-derived code), or the
// jsonErrors middleware rewriting a mux-default 404/405. No legacy
// {"error":"<string>"} shape survives.
func TestErrorEnvelope_Unified(t *testing.T) {
	t.Run("writeAPIError carries the explicit code", func(t *testing.T) {
		rw := httptest.NewRecorder()
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", "no such flow")
		if ct := rw.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		env := decodeEnvelope(t, rw.Body.Bytes())
		if env.Error.Code != "flow_not_found" || env.Error.Message != "no such flow" {
			t.Errorf("env = %+v", env)
		}
	})

	t.Run("writeJSONError derives the code from the status", func(t *testing.T) {
		rw := httptest.NewRecorder()
		writeJSONError(rw, http.StatusConflict, "already running")
		env := decodeEnvelope(t, rw.Body.Bytes())
		if env.Error.Code != "conflict" {
			t.Errorf("code = %q, want conflict (status-derived)", env.Error.Code)
		}
	})

	t.Run("jsonErrors rewrites a mux-default 404 into the envelope", func(t *testing.T) {
		// http.NotFound writes a text/plain 404 — the mux default the
		// middleware must convert.
		h := jsonErrors(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			http.NotFound(rw, r)
		}))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest("GET", "/missing", nil))
		if rw.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rw.Code)
		}
		env := decodeEnvelope(t, rw.Body.Bytes())
		if env.Error.Code != "not_found" {
			t.Errorf("mux-default code = %q, want not_found", env.Error.Code)
		}
	})

	t.Run("a handler-produced JSON 404 passes through unchanged", func(t *testing.T) {
		// writeAPIError already set application/json, so the middleware must
		// NOT swallow/rewrite it.
		h := jsonErrors(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			writeAPIError(rw, http.StatusNotFound, "drop_not_found", "no such drop: foo")
		}))
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest("GET", "/x", nil))
		env := decodeEnvelope(t, rw.Body.Bytes())
		if env.Error.Code != "drop_not_found" {
			t.Errorf("code = %q, want the handler's drop_not_found (not rewritten)", env.Error.Code)
		}
	})
}

// TestCodeForStatus pins the status→code mapping the whole legacy-path
// envelope relies on, so the web client's code-based branching stays stable.
func TestCodeForStatus(t *testing.T) {
	cases := map[int]string{
		http.StatusBadRequest:            "bad_request",
		http.StatusUnauthorized:          "unauthorized",
		http.StatusForbidden:             "forbidden",
		http.StatusNotFound:              "not_found",
		http.StatusMethodNotAllowed:      "method_not_allowed",
		http.StatusConflict:              "conflict",
		http.StatusRequestEntityTooLarge: "payload_too_large",
		http.StatusTooManyRequests:       "rate_limited",
		http.StatusInsufficientStorage:   "storage_full",
		http.StatusNotImplemented:        "not_implemented",
		http.StatusServiceUnavailable:    "unavailable",
		http.StatusInternalServerError:   "internal_error",
		http.StatusTeapot:                "error", // unmapped 4xx → generic
	}
	for status, want := range cases {
		if got := codeForStatus(status); got != want {
			t.Errorf("codeForStatus(%d) = %q, want %q", status, got, want)
		}
	}
}
