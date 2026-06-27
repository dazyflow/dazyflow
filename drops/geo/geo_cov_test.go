// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// errCode returns the error code of a result, or "" when there is no error.
func errCode(r core.Result) string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

// --- parseLatLon edge cases --------------------------------------------------

func TestParseLatLon_Table(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantErr  bool
		lat, lon float64
	}{
		{"ok", "59.33,18.07", false, 59.33, 18.07},
		{"ok_negative", "-12.5,-77.0", false, -12.5, -77.0},
		{"wrong_arity", "1,2,3", true, 0, 0},
		{"single", "59.33", true, 0, 0},
		{"lat_nan", "abc,18.07", true, 0, 0},
		{"lon_nan", "59.33,xyz", true, 0, 0},
		{"lat_out_of_range", "91,0", true, 0, 0},
		{"lat_under_range", "-91,0", true, 0, 0},
		{"lon_out_of_range", "0,200", true, 0, 0},
		{"lon_under_range", "0,-200", true, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lat, lon, err := parseLatLon(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseLatLon(%q): want error, got (%v,%v)", c.in, lat, lon)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLatLon(%q): unexpected err %v", c.in, err)
			}
			if lat != c.lat || lon != c.lon {
				t.Errorf("parseLatLon(%q) = (%v,%v), want (%v,%v)", c.in, lat, lon, c.lat, c.lon)
			}
		})
	}
}

// --- executeLocation input/precedence paths ----------------------------------

func TestExecuteLocation_NonTextPlaceInput(t *testing.T) {
	// A non-string, non-[]byte Place input is a wiring mistake → bad_input,
	// and no network is touched.
	job := core.Job{
		Params: map[string]any{"point": "1,2"},
		Input:  map[string]core.Ref{"place": {Inline: 42}},
	}
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "bad_input" {
		t.Fatalf("non-text place input → want bad_input, got %+v", r.Error)
	}
}

func TestExecuteLocation_BadPointParam(t *testing.T) {
	// No Place, malformed point param → bad_param (parseLatLon error surfaced).
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"point": "not-a-coord"}}, nil)
	if errCode(r) != "bad_param" {
		t.Fatalf("bad point → want bad_param, got %+v", r.Error)
	}
}

func TestExecuteLocation_ForwardErrorSurfaced(t *testing.T) {
	// A Place that the backend rejects (non-2xx) surfaces the geocoder error
	// Result through executeLocation's errRes branch.
	stubNominatim(t, 500, `boom`, nil)
	job := core.Job{Params: map[string]any{"place": "Nowhere"}}
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("500 on forward → want geocoder_error, got %+v", r.Error)
	}
}

// --- executeReverse / resolveCoord input paths -------------------------------

func TestResolveCoord_NonTextInput(t *testing.T) {
	job := core.Job{
		Params: map[string]any{"point": "1,2"},
		Input:  map[string]core.Ref{"coordinate": {Inline: 3.14}},
	}
	r, _ := executeReverse(context.Background(), job, nil)
	if errCode(r) != "bad_param" {
		t.Fatalf("non-text coordinate input → want bad_param, got %+v", r.Error)
	}
}

func TestExecuteReverse_ReverseErrorSurfaced(t *testing.T) {
	stubNominatim(t, 500, `boom`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("500 on reverse → want geocoder_error, got %+v", r.Error)
	}
}

// --- geoHTTPFailure status classification ------------------------------------

func TestGeoHTTPFailure_StatusCodes(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		wantCode string
	}{
		{"forbidden", 403, "rate_limited"},
		{"too_many", 429, "rate_limited"},
		{"server_error", 500, "geocoder_error"},
		{"not_found", 404, "geocoder_error"},
		{"bad_request", 400, "geocoder_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubNominatim(t, c.status, `error body`, nil)
			r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
			if errCode(r) != c.wantCode {
				t.Fatalf("status %d → want %s, got %+v", c.status, c.wantCode, r.Error)
			}
		})
	}
}

func TestGeoHTTPFailure_LongBodyTruncated(t *testing.T) {
	// A >200-byte non-2xx body is truncated into the error message.
	long := strings.Repeat("x", 500)
	stubNominatim(t, 502, long, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("502 → want geocoder_error, got %+v", r.Error)
	}
	if r.Error == nil || len(r.Error.Message) > 300 {
		t.Fatalf("expected a truncated message, got len=%d", len(r.Error.Message))
	}
}

func TestGeoHTTPFailure_TransportError(t *testing.T) {
	// Point the backend at an unreachable host so the dial fails with a
	// transport error (not an HTTP status). SSRF guard allows 127.0.0.1; an
	// unused high port refuses the connection.
	prev := nominatimURL
	nominatimURL = "http://127.0.0.1:1"
	t.Cleanup(func() { nominatimURL = prev })
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2", "timeout_ms": 1000}}, nil)
	// Either a transport error or an SSRF block — both are error Results.
	if r.Error == nil {
		t.Fatalf("transport failure → want an error Result, got %+v", r)
	}
	if c := r.Error.Code; c != "geocoder_http_error" && c != "egress_blocked" {
		t.Fatalf("transport failure → unexpected code %q", c)
	}
}

func TestGeoHTTPFailure_SSRFBlocked(t *testing.T) {
	// Disable the private-egress opt-in so the SSRF guard blocks the dial to
	// the 127.0.0.1 httptest server, exercising the egress_blocked branch.
	// Restored on cleanup so other tests keep their loopback access.
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	hfnet.SetAllowPrivateEgress(false)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(true) })
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2", "timeout_ms": 1000}}, nil)
	if errCode(r) != "egress_blocked" {
		t.Fatalf("SSRF-blocked dial → want egress_blocked, got %+v", r.Error)
	}
	if req != nil {
		t.Error("request should have been blocked before reaching the server")
	}
}

// --- geoFetch: Accept-Language header + timeout fallback ----------------------

func TestGeoFetch_AcceptLanguageHeader(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	// language set → Accept-Language header; timeout_ms <= 0 → falls back to default.
	job := core.Job{Params: map[string]any{"point": "59.3293,18.0686", "language": "sv", "timeout_ms": 0}}
	r, _ := executeReverse(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if got := req.Header.Get("Accept-Language"); got != "sv" {
		t.Errorf("Accept-Language = %q, want sv", got)
	}
}

// --- nominatim forward/reverse parse & match paths ---------------------------

func TestNominatimForward_CountryCodes(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleSearch, &req)
	job := core.Job{Params: map[string]any{"place": "Stockholm", "countrycodes": "se,no"}}
	r, _ := executeLocation(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if got := req.URL.Query().Get("countrycodes"); got != "se,no" {
		t.Errorf("countrycodes = %q, want se,no", got)
	}
}

func TestNominatimForward_NoMatch(t *testing.T) {
	stubNominatim(t, 200, `[]`, nil)
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"place": "Nowhere"}}, nil)
	if errCode(r) != "no_match" {
		t.Fatalf("empty array → want no_match, got %+v", r.Error)
	}
}

func TestNominatimForward_BadJSON(t *testing.T) {
	stubNominatim(t, 200, `{not json`, nil)
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"place": "Stockholm"}}, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("malformed JSON → want geocoder_error, got %+v", r.Error)
	}
}

func TestNominatimReverse_NoMatch(t *testing.T) {
	// A valid object with an empty display_name → no_match.
	stubNominatim(t, 200, `{"lat":"1","lon":"2","display_name":""}`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if errCode(r) != "no_match" {
		t.Fatalf("empty display_name → want no_match, got %+v", r.Error)
	}
}

func TestNominatimReverse_BadJSON(t *testing.T) {
	stubNominatim(t, 200, `not-json-at-all`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("malformed JSON → want geocoder_error, got %+v", r.Error)
	}
}

// --- toGeoPlace malformed coordinate paths -----------------------------------

func TestToGeoPlace_MalformedCoordinate(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bad_lat", `{"lat":"abc","lon":"18.07","display_name":"X"}`},
		{"bad_lon", `{"lat":"59.33","lon":"xyz","display_name":"X"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubNominatim(t, 200, c.body, nil)
			r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
			if errCode(r) != "geocoder_error" {
				t.Fatalf("%s → want geocoder_error, got %+v", c.name, r.Error)
			}
		})
	}
}

// --- locationiq forward missing-key path -------------------------------------

func TestLocationIQ_ForwardMissingKey(t *testing.T) {
	var req *http.Request
	stubLocationIQ(t, 200, sampleSearch, &req)
	// Select LocationIQ but supply no api_key, via the forward (Place) path.
	job := core.Job{Params: map[string]any{"backend": "locationiq", "place": "Stockholm"}}
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "not_connected" {
		t.Fatalf("forward without key → want not_connected, got %+v", r.Error)
	}
	if req != nil {
		t.Error("should not hit the network without a key")
	}
}

// --- photon forward/reverse + firstFeature paths -----------------------------

func TestPhoton_ForwardLangAndNoMatch(t *testing.T) {
	var req *http.Request
	stubPhoton(t, 200, `{"type":"FeatureCollection","features":[]}`, &req)
	job := photonJob(map[string]any{"place": "Nowhere", "language": "de"})
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "no_match" {
		t.Fatalf("empty features on forward → want no_match, got %+v", r.Error)
	}
	if got := req.URL.Query().Get("lang"); got != "de" {
		t.Errorf("lang = %q, want de", got)
	}
}

func TestPhoton_ForwardRateLimited(t *testing.T) {
	// A non-2xx on the Photon forward path returns the failure Result before
	// firstFeature (the g.fail branch in forward).
	stubPhoton(t, 429, `slow down`, nil)
	r, _ := executeLocation(context.Background(), photonJob(map[string]any{"place": "Stockholm"}), nil)
	if errCode(r) != "rate_limited" {
		t.Fatalf("429 on forward → want rate_limited, got %+v", r.Error)
	}
}

func TestPhoton_ForwardSuccess(t *testing.T) {
	// A successful Photon forward (Place) lookup exercises firstFeature's
	// happy path through the forward entry point.
	var req *http.Request
	stubPhoton(t, 200, samplePhoton, &req)
	job := photonJob(map[string]any{"place": "Stockholm"})
	r, _ := executeLocation(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate = %q", got)
	}
	if req.URL.Path != "/api" {
		t.Errorf("path = %q, want /api", req.URL.Path)
	}
}

func TestExecuteReverse_NoAddress(t *testing.T) {
	// A reverse hit with no address object → the address pin falls back to an
	// empty object (the addr == nil branch).
	stubNominatim(t, 200, `{"lat":"59.33","lon":"18.07","display_name":"Somewhere"}`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	addr, ok := r.Output["address"]
	if !ok {
		t.Fatal("missing address pin")
	}
	m, ok := addr.Inline.(map[string]any)
	if !ok || len(m) != 0 {
		t.Errorf("address = %#v, want empty map", addr.Inline)
	}
}

func TestPhoton_ReverseLang(t *testing.T) {
	var req *http.Request
	stubPhoton(t, 200, samplePhoton, &req)
	job := photonJob(map[string]any{"point": "59.3293,18.0686", "language": "fr"})
	r, _ := executeReverse(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if got := req.URL.Query().Get("lang"); got != "fr" {
		t.Errorf("lang = %q, want fr", got)
	}
}

func TestPhotonFirstFeature_ErrorPaths(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"bad_json", `{not json`, "geocoder_error"},
		{"missing_coordinate", `{"type":"FeatureCollection","features":[{"geometry":{"coordinates":[]},"properties":{}}]}`, "geocoder_error"},
		{"single_coordinate", `{"type":"FeatureCollection","features":[{"geometry":{"coordinates":[18.07]},"properties":{}}]}`, "geocoder_error"},
		{"out_of_range", `{"type":"FeatureCollection","features":[{"geometry":{"coordinates":[18.07,999]},"properties":{}}]}`, "geocoder_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubPhoton(t, 200, c.body, nil)
			r, _ := executeReverse(context.Background(), photonJob(map[string]any{"point": "1,2"}), nil)
			if errCode(r) != c.wantCode {
				t.Fatalf("%s → want %s, got %+v", c.name, c.wantCode, r.Error)
			}
		})
	}
}
