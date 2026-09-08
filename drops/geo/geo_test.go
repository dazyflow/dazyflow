// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func init() { hfnet.SetAllowPrivateEgress(true) }

const sampleSearch = `[{"lat":"59.3293","lon":"18.0686","display_name":"Stockholm, Sweden","address":{"city":"Stockholm","country":"Sweden"}}]`
const sampleReverse = `{"lat":"59.3293","lon":"18.0686","display_name":"Stockholm, Södermanland, Sweden","address":{"city":"Stockholm","country":"Sweden"}}`

func stubNominatim(t *testing.T, status int, body string, gotReq **http.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			*gotReq = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := nominatimURL
	nominatimURL = srv.URL
	t.Cleanup(func() { nominatimURL = prev; srv.Close() })
}

func textPin(t *testing.T, r core.Result, port string) string {
	t.Helper()
	ref, ok := r.Output[port]
	if !ok {
		t.Fatalf("missing output pin %q", port)
	}
	s, ok := ref.Inline.(string)
	if !ok {
		t.Fatalf("output pin %q is %T, want string", port, ref.Inline)
	}
	return s
}

func TestExecuteLocation_Point(t *testing.T) {
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"point": "59.3293,18.0686"}}, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v err %+v", r.Status, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate = %q", got)
	}
	if textPin(t, r, "place") != "" {
		t.Errorf("place should be empty for a map pin, got %q", textPin(t, r, "place"))
	}
}

func TestExecuteLocation_Neither(t *testing.T) {
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Errorf("empty: want bad_param, got %+v", r.Error)
	}
}

func TestExecuteLocation_PlaceParamOverridesPoint(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleSearch, &req)
	job := core.Job{Params: map[string]any{"point": "1.0,2.0", "place": "Stockholm"}}
	r, err := executeLocation(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("Place should override the pin, coordinate = %q", got)
	}
	if req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("geocoded query = %q", req.URL.Query().Get("q"))
	}
}

func TestExecuteLocation_PlaceInputOverridesParam(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleSearch, &req)
	job := core.Job{
		Params: map[string]any{"point": "1.0,2.0", "place": "typed"},
		Input:  map[string]core.Ref{"place": {Inline: "Stockholm"}},
	}
	if r, _ := executeLocation(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("Place input should override the typed Place param, got q=%q", req.URL.Query().Get("q"))
	}
}

func TestExecuteReverse_Point(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	r, err := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "59.3293,18.0686"}}, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if textPin(t, r, "place") != "Stockholm, Södermanland, Sweden" {
		t.Errorf("place = %q", textPin(t, r, "place"))
	}
	if textPin(t, r, "coordinate") != "59.3293,18.0686" {
		t.Errorf("coordinate echo = %q", textPin(t, r, "coordinate"))
	}
	if req.URL.Path != "/reverse" || req.URL.Query().Get("lat") != "59.3293" {
		t.Errorf("request = %s?%s", req.URL.Path, req.URL.RawQuery)
	}
	if _, ok := r.Output["address"]; !ok {
		t.Error("missing address pin")
	}
}

func TestExecuteReverse_CoordInputOverridesPoint(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	job := core.Job{
		Params: map[string]any{"point": "1.0,2.0"},
		Input:  map[string]core.Ref{"coordinate": {Inline: "59.3293,18.0686"}},
	}
	if r, _ := executeReverse(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if req.URL.Query().Get("lat") != "59.3293" {
		t.Errorf("Coordinate input should override the map pin, got lat=%q", req.URL.Query().Get("lat"))
	}
}

func TestExecuteReverse_Neither_NoNetwork(t *testing.T) {
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
	r, _ = executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "not-a-point"}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("bad point: want bad_param, got %+v", r.Error)
	}
}

func TestHTTPFailure_RateLimited(t *testing.T) {
	stubNominatim(t, 403, `rate limited`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if r.Error == nil || r.Error.Code != "rate_limited" {
		t.Fatalf("403 → want rate_limited, got %+v", r.Error)
	}
}

func errCode(r core.Result) string {
	if r.Error == nil {
		return ""
	}
	return r.Error.Code
}

func TestExecuteLocation_NonTextPlaceInput(t *testing.T) {
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
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"point": "not-a-coord"}}, nil)
	if errCode(r) != "bad_param" {
		t.Fatalf("bad point → want bad_param, got %+v", r.Error)
	}
}

func TestExecuteLocation_ForwardErrorSurfaced(t *testing.T) {
	stubNominatim(t, 500, `boom`, nil)
	job := core.Job{Params: map[string]any{"place": "Nowhere"}}
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "geocoder_error" {
		t.Fatalf("500 on forward → want geocoder_error, got %+v", r.Error)
	}
}

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
	prev := nominatimURL
	nominatimURL = "http://127.0.0.1:1"
	t.Cleanup(func() { nominatimURL = prev })
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2", "timeout_ms": 1000}}, nil)
	if r.Error == nil {
		t.Fatalf("transport failure → want an error Result, got %+v", r)
	}
	if c := r.Error.Code; c != "geocoder_http_error" && c != "egress_blocked" {
		t.Fatalf("transport failure → unexpected code %q", c)
	}
}

func TestGeoHTTPFailure_SSRFBlocked(t *testing.T) {
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

func TestGeoFetch_AcceptLanguageHeader(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	job := core.Job{Params: map[string]any{"point": "59.3293,18.0686", "language": "sv", "timeout_ms": 0}}
	r, _ := executeReverse(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if got := req.Header.Get("Accept-Language"); got != "sv" {
		t.Errorf("Accept-Language = %q, want sv", got)
	}
}

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

func TestLocationIQ_ForwardMissingKey(t *testing.T) {
	var req *http.Request
	stubLocationIQ(t, 200, sampleSearch, &req)
	job := core.Job{Params: map[string]any{"backend": "locationiq", "place": "Stockholm"}}
	r, _ := executeLocation(context.Background(), job, nil)
	if errCode(r) != "not_connected" {
		t.Fatalf("forward without key → want not_connected, got %+v", r.Error)
	}
	if req != nil {
		t.Error("should not hit the network without a key")
	}
}

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
	stubPhoton(t, 429, `slow down`, nil)
	r, _ := executeLocation(context.Background(), photonJob(map[string]any{"place": "Stockholm"}), nil)
	if errCode(r) != "rate_limited" {
		t.Fatalf("429 on forward → want rate_limited, got %+v", r.Error)
	}
}

func TestPhoton_ForwardSuccess(t *testing.T) {
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
