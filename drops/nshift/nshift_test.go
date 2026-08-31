// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// fakeNshift stands in for the Unifaun ExtAPI: it checks the Bearer key and
// serves the shipments collection + item endpoints, recording the last request
// so tests can assert on the method, path, auth and body.
type fakeNshift struct {
	srv          *httptest.Server
	lastAuth     string
	lastMethod   string
	lastPath     string
	lastBody     map[string]any
	createStatus int
	// createResp is what POST /shipments returns (default: a one-element array).
	createResp string
}

func newFakeNshift(t *testing.T) *fakeNshift {
	t.Helper()
	f := &fakeNshift{
		createStatus: 201,
		createResp:   `[{"id":"774","parcels":[{"copyNo":"00370123456789012345"},{"copyNo":"00370123456789012346"}]}]`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		f.lastMethod = r.Method
		f.lastPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer k1" {
			rw.WriteHeader(401)
			_, _ = io.WriteString(rw, `[{"key":"UNAUTHORIZED","message":"Bad credentials"}]`)
			return
		}
		f.lastBody = nil
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &f.lastBody)
			}
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/rs-extapi/v1/shipments":
			if f.createStatus != 201 {
				rw.WriteHeader(f.createStatus)
				_, _ = io.WriteString(rw, `[{"key":"receiver.country","message":"Invalid receiver country"}]`)
				return
			}
			rw.WriteHeader(201)
			_, _ = io.WriteString(rw, f.createResp)
		case r.Method == "GET" && r.URL.Path == "/rs-extapi/v1/shipments/774":
			_, _ = io.WriteString(rw, `{"id":"774","parcels":[{"copyNo":"00370123456789012345"}],"state":"PRINTED"}`)
		case r.Method == "GET" && r.URL.Path == "/rs-extapi/v1/shipments/missing":
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `[{"key":"NOT_FOUND","message":"No such shipment"}]`)
		case r.Method == "DELETE" && r.URL.Path == "/rs-extapi/v1/shipments/774":
			rw.WriteHeader(204)
		case r.Method == "DELETE" && r.URL.Path == "/rs-extapi/v1/shipments/printed":
			rw.WriteHeader(400)
			_, _ = io.WriteString(rw, `[{"key":"PRINTED","message":"Cannot delete a printed shipment"}]`)
		default:
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `[{"message":"No route"}]`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNshift) job(extra map[string]any) core.Job {
	p := map[string]any{"api_key": "k1", "base_url": f.srv.URL}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

func TestCreateShipment_OK(t *testing.T) {
	f := newFakeNshift(t)
	shipment := map[string]any{"receiver": map[string]any{"country": "SE"}, "parcels": []any{map[string]any{"weight": 2.5}}}
	res, err := executeCreateShipment(context.Background(), f.job(map[string]any{"shipment": shipment}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["shipment_id"].Inline; got != "774" {
		t.Errorf("shipment_id = %v, want 774", got)
	}
	if got := res.Output["tracking_numbers"].Inline; got != "00370123456789012345, 00370123456789012346" {
		t.Errorf("tracking_numbers = %v, want the two copyNos", got)
	}
	if f.lastMethod != "POST" || f.lastPath != "/rs-extapi/v1/shipments" {
		t.Errorf("request = %s %s, want POST /rs-extapi/v1/shipments", f.lastMethod, f.lastPath)
	}
	if f.lastAuth != "Bearer k1" {
		t.Errorf("auth = %q, want Bearer k1", f.lastAuth)
	}
	// The shipment body must be forwarded verbatim.
	if _, ok := f.lastBody["receiver"]; !ok {
		t.Errorf("request body missing the shipment payload: %+v", f.lastBody)
	}
}

func TestCreateShipment_SingleObjectResponse(t *testing.T) {
	f := newFakeNshift(t)
	// Some deployments return a bare object rather than a one-element array.
	f.createResp = `{"id":"775","parcels":[{"parcelNo":"P1"}]}`
	res, _ := executeCreateShipment(context.Background(),
		f.job(map[string]any{"shipment": map[string]any{"x": 1}}), nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["shipment_id"].Inline; got != "775" {
		t.Errorf("shipment_id = %v, want 775", got)
	}
	// parcelNo is the fallback tracking number when copyNo is absent.
	if got := res.Output["tracking_numbers"].Inline; got != "P1" {
		t.Errorf("tracking_numbers = %v, want P1", got)
	}
}

func TestCreateShipment_RequiresShipment(t *testing.T) {
	f := newFakeNshift(t)
	res, _ := executeCreateShipment(context.Background(), f.job(nil), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without a shipment payload")
	}
}

func TestCreateShipment_SurfacesValidationError(t *testing.T) {
	f := newFakeNshift(t)
	f.createStatus = 422
	res, _ := executeCreateShipment(context.Background(),
		f.job(map[string]any{"shipment": map[string]any{"x": 1}}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 422")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Invalid receiver country") {
		t.Errorf("error = %+v, want the ExtAPI reason surfaced", res.Error)
	}
}

func TestCreateShipment_InputOverridesParam(t *testing.T) {
	f := newFakeNshift(t)
	job := f.job(map[string]any{"shipment": map[string]any{"from": "param"}})
	job.Input = map[string]core.Ref{"shipment": {MIME: "application/json", Inline: map[string]any{"from": "input"}}}
	res, _ := executeCreateShipment(context.Background(), job, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got, _ := f.lastBody["from"].(string); got != "input" {
		t.Errorf("body from = %q, want the wired input to win", got)
	}
}

func TestGetShipment_OK(t *testing.T) {
	f := newFakeNshift(t)
	res, err := executeGetShipment(context.Background(), f.job(map[string]any{"shipment_id": "774"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["tracking_numbers"].Inline; got != "00370123456789012345" {
		t.Errorf("tracking_numbers = %v", got)
	}
	if f.lastMethod != "GET" {
		t.Errorf("method = %s, want GET", f.lastMethod)
	}
}

func TestGetShipment_RequiresID(t *testing.T) {
	f := newFakeNshift(t)
	res, _ := executeGetShipment(context.Background(), f.job(nil), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without a shipment id")
	}
}

func TestGetShipment_SurfacesNotFound(t *testing.T) {
	f := newFakeNshift(t)
	res, _ := executeGetShipment(context.Background(), f.job(map[string]any{"shipment_id": "missing"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 404")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "No such shipment") {
		t.Errorf("error = %+v, want the ExtAPI reason surfaced", res.Error)
	}
}

func TestDeleteShipment_OK(t *testing.T) {
	f := newFakeNshift(t)
	res, err := executeDeleteShipment(context.Background(), f.job(map[string]any{"shipment_id": "774"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["deleted"].Inline; got != "true" {
		t.Errorf("deleted = %v, want true", got)
	}
	if f.lastMethod != "DELETE" {
		t.Errorf("method = %s, want DELETE", f.lastMethod)
	}
}

func TestDeleteShipment_PrintedRejected(t *testing.T) {
	f := newFakeNshift(t)
	res, _ := executeDeleteShipment(context.Background(), f.job(map[string]any{"shipment_id": "printed"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result deleting a printed shipment")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "printed shipment") {
		t.Errorf("error = %+v, want the ExtAPI reason surfaced", res.Error)
	}
}

func TestDeleteShipment_MissingCreds(t *testing.T) {
	f := newFakeNshift(t)
	job := core.Job{ID: "j1", Params: map[string]any{"base_url": f.srv.URL, "shipment_id": "774"}}
	res, _ := executeDeleteShipment(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without credentials")
	}
}

func TestEnvBase(t *testing.T) {
	cases := map[string]string{
		"production":  "https://api.unifaun.com",
		"integration": "https://api.unifaun.se",
		"":            "https://api.unifaun.se", // unset → safe sandbox
		"bogus":       "https://api.unifaun.se", // unknown → safe sandbox
	}
	for env, want := range cases {
		if got := envBase(env); got != want {
			t.Errorf("envBase(%q) = %q, want %q", env, got, want)
		}
	}
}

func TestBaseURL_OverrideWinsOverEnv(t *testing.T) {
	job := core.Job{Params: map[string]any{"environment": "production", "base_url": "http://localhost:1/"}}
	if got := baseURL(job); got != "http://localhost:1" {
		t.Errorf("baseURL = %q, want the trimmed override", got)
	}
}

// TestDecodeObject covers the shared JSON-object decoder. It names the PORT in
// its error because the mistake it catches is a wiring one — the run viewer has
// to say which input was wrong, not just that some JSON was.
func TestDecodeObject(t *testing.T) {
	t.Run("decodes an object", func(t *testing.T) {
		m, err := decodeObject([]byte(`{"a":1,"b":"x"}`), "options")
		if err != nil {
			t.Fatalf("decodeObject: %v", err)
		}
		if m["b"] != "x" {
			t.Errorf("m = %#v", m)
		}
	})

	t.Run("empty and whitespace are unset, not errors", func(t *testing.T) {
		// An unwired optional port arrives as empty bytes; that is absence, and
		// must not read as a malformed object.
		for _, raw := range []string{"", "   ", "\n\t ", "\r\n"} {
			m, err := decodeObject([]byte(raw), "options")
			if err != nil || m != nil {
				t.Errorf("decodeObject(%q) = %v, %v; want nil, nil", raw, m, err)
			}
		}
	})

	t.Run("surrounding whitespace is tolerated", func(t *testing.T) {
		m, err := decodeObject([]byte("  \n{\"a\":1}\t "), "options")
		if err != nil || m["a"] == nil {
			t.Fatalf("decodeObject = %v, %v", m, err)
		}
	})

	t.Run("a non-object is rejected and names the port", func(t *testing.T) {
		// An array, a scalar and truncated JSON are all the same wiring error:
		// the port wants an object.
		for _, raw := range []string{
			`[{"a":1}]`, `"a string"`, `42`, `true`, `{"a":1`, `not json`,
		} {
			m, err := decodeObject([]byte(raw), "options")
			if err == nil {
				t.Errorf("decodeObject(%q) was accepted as %v", raw, m)
				continue
			}
			if !strings.Contains(err.Error(), "'options'") {
				t.Errorf("error for %q = %q, want it to name the port", raw, err)
			}
			if !strings.Contains(err.Error(), "JSON object") {
				t.Errorf("error for %q = %q, want it to say a JSON object is expected", raw, err)
			}
		}
	})

	t.Run("JSON null is absence, not a malformed object", func(t *testing.T) {
		// `null` unmarshals into a nil map without error, so it lands in the
		// same place as an unwired port. That is the right reading — a caller
		// that computed "no options" upstream should not get a wiring error.
		m, err := decodeObject([]byte(`null`), "options")
		if err != nil || m != nil {
			t.Errorf("decodeObject(null) = %v, %v; want nil, nil", m, err)
		}
	})

	t.Run("the port name is carried through verbatim", func(t *testing.T) {
		_, err := decodeObject([]byte(`[]`), "receiver")
		if err == nil || !strings.Contains(err.Error(), "'receiver'") {
			t.Errorf("error = %v, want it to name 'receiver'", err)
		}
	})
}
