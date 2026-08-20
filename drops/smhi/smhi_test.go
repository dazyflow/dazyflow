// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package smhi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func init() { hfnet.SetAllowPrivateEgress(true) }

// snow1g point response: a flat data map per step, symbol_code = Wsymb2.
const sample = `{
  "createdTime":"2026-06-24T12:59:00Z","referenceTime":"2026-06-24T12:45:00Z",
  "geometry":{"type":"Point","coordinates":[18.077207,59.330360]},
  "timeSeries":[
    {"time":"2026-06-24T13:00:00Z","data":{"air_temperature":18.3,"relative_humidity":64,"wind_speed":3.4,"symbol_code":2}},
    {"time":"2026-06-24T12:00:00Z","data":{"air_temperature":20.0,"symbol_code":5}},
    {"time":"2026-06-25T12:00:00Z","data":{"air_temperature":15.0,"symbol_code":18}},
    {"time":"2026-06-25T18:00:00Z","data":{"air_temperature":11.0,"symbol_code":6}},
    {"time":"2026-06-26T12:00:00Z","data":{"air_temperature":9.0,"symbol_code":25}}
  ]
}`

func stubSMHI(t *testing.T, status int, body string, gotReq **http.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			*gotReq = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := forecastBase
	forecastBase = srv.URL
	t.Cleanup(func() { forecastBase = prev; srv.Close() })
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

func TestWsymbClass(t *testing.T) {
	for code, want := range map[int]string{1: "Clear", 2: "Clear", 5: "Clouds", 7: "Fog", 11: "Thunder", 18: "Rain", 25: "Snow", 22: "Sleet"} {
		if got := classFor(code); got != want {
			t.Errorf("classFor(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestExecuteCurrent_Success(t *testing.T) {
	var req *http.Request
	stubSMHI(t, 200, sample, &req)

	job := core.Job{Params: map[string]any{"lat": 59.3293, "lon": 18.0686}}
	r, err := executeCurrent(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v %+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "summary"); got != "Nearly clear sky, 18.3°C, humidity 64%, wind 3.4 m/s" {
		t.Errorf("summary = %q", got)
	}
	if got := textPin(t, r, "temperature"); got != "18.3" {
		t.Errorf("temperature = %q", got)
	}
	if got := textPin(t, r, "conditions"); got != "Clear" {
		t.Errorf("conditions = %q", got)
	}
	// lon BEFORE lat, 6 decimals; current asks for a single step.
	if !strings.Contains(req.URL.Path, "/lon/18.068600/lat/59.329300/") {
		t.Errorf("request path = %q", req.URL.Path)
	}
	if !strings.Contains(req.URL.RawQuery, "timeseries=1") {
		t.Errorf("current should request timeseries=1, query = %q", req.URL.RawQuery)
	}
}

func TestExecuteCurrent_CoordInputOverrides(t *testing.T) {
	var req *http.Request
	stubSMHI(t, 200, sample, &req)
	job := core.Job{
		Params: map[string]any{"lat": 1.0, "lon": 2.0},
		Input:  map[string]core.Ref{"coordinate": {Inline: "59.3293,18.0686"}},
	}
	if r, _ := executeCurrent(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if !strings.Contains(req.URL.Path, "/lon/18.068600/lat/59.329300/") {
		t.Errorf("Coordinate input should drive the request, path = %q", req.URL.Path)
	}
}

func TestExecuteCurrent_BadParam_NoNetwork(t *testing.T) {
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
	r, _ = executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": "5", "lon": "5"}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("string lat/lon: want bad_param, got %+v", r.Error)
	}
}

func TestExecuteCurrent_OutOfDomain(t *testing.T) {
	stubSMHI(t, 404, "Requested point is out of bounds", nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 0.0, "lon": 0.0}}, nil)
	if r.Error == nil || r.Error.Code != "out_of_domain" {
		t.Fatalf("404 → want out_of_domain, got %+v", r.Error)
	}
}

func TestExecuteForecast_Success(t *testing.T) {
	var req *http.Request
	stubSMHI(t, 200, sample, &req)
	job := core.Job{Params: map[string]any{"lat": 59.3293, "lon": 18.0686, "days": 2}}
	r, err := executeForecast(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v %+v", r.Status, err, r.Error)
	}
	summary := textPin(t, r, "summary")
	if strings.Count(summary, "\n") != 1 {
		t.Errorf("want 2 day lines, got %q", summary)
	}
	if !strings.Contains(summary, "Cloudy sky, 18–20°C") {
		t.Errorf("day-1 line off: %q", summary)
	}
	if !strings.Contains(summary, "Light rain, 11–15°C") {
		t.Errorf("day-2 line off: %q", summary)
	}
	if strings.Contains(strings.ToLower(summary), "snow") {
		t.Errorf("third day leaked past days=2: %q", summary)
	}
	daily, ok := r.Output["daily"].Inline.([]smhiDay)
	if !ok || len(daily) != 2 {
		t.Fatalf("daily pin = %T len %d, want 2", r.Output["daily"].Inline, len(daily))
	}
	if daily[0].Conditions != "Clouds" || daily[0].TempMin != 18.3 || daily[0].TempMax != 20.0 {
		t.Errorf("day-0 aggregate wrong: %+v", daily[0])
	}
	// Forecast asks for the full series — no timeseries=1.
	if strings.Contains(req.URL.RawQuery, "timeseries=1") {
		t.Errorf("forecast should NOT limit to one step, query = %q", req.URL.RawQuery)
	}
}

func TestCovHTTPFailureSSRF(t *testing.T) {
	// Plain transport error → smhi_http_error.
	f := httpFailure(core.Job{}, 0, nil, context.DeadlineExceeded)
	if f == nil || f.Error.Code != "smhi_http_error" {
		t.Fatalf("want smhi_http_error, got %+v", f)
	}
	// Non-2xx, non-404, with long body truncation.
	long := strings.Repeat("x", 500)
	f = httpFailure(core.Job{}, 500, []byte(long), nil)
	if f == nil || f.Error.Code != "smhi_error" {
		t.Fatalf("want smhi_error, got %+v", f)
	}
	// Success returns nil.
	if httpFailure(core.Job{}, 200, []byte("{}"), nil) != nil {
		t.Fatal("200 should yield nil failure")
	}
}

func TestCovEntryNumJSONNumber(t *testing.T) {
	e := smhiEntry{Data: map[string]any{"a": json.Number("3.2"), "b": json.Number("bad"), "c": "str"}}
	if v, ok := e.num("a"); !ok || v != 3.2 {
		t.Fatalf("json.Number: %v %v", v, ok)
	}
	if _, ok := e.num("b"); ok {
		t.Fatal("bad json.Number should be !ok")
	}
	if _, ok := e.num("c"); ok {
		t.Fatal("string should be !ok")
	}
}

func TestCovCurrentSummaryNoSymbol(t *testing.T) {
	e := smhiEntry{Data: map[string]any{"air_temperature": 5.0, "relative_humidity": 50.0, "wind_speed": 2.0}}
	got := currentSummary(e)
	if strings.Contains(got, ",") && strings.HasPrefix(got, "5.0") == false {
		// Without a symbol the line begins with the temperature.
		t.Fatalf("no-symbol summary = %q", got)
	}
	if !strings.HasPrefix(got, "5.0°C") {
		t.Fatalf("no-symbol summary should start with temp, got %q", got)
	}
}

func TestCovExecuteCurrentBadJSON(t *testing.T) {
	var req any
	_ = req
	stubSMHI(t, 200, "not json", nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 59.0, "lon": 18.0}}, nil)
	if r.Error == nil || r.Error.Code != "smhi_error" {
		t.Fatalf("bad json → want smhi_error, got %+v", r.Error)
	}
}

func TestCovExecuteCurrentEmptySeries(t *testing.T) {
	stubSMHI(t, 200, `{"timeSeries":[]}`, nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 59.0, "lon": 18.0}}, nil)
	if r.Error == nil || r.Error.Code != "smhi_error" {
		t.Fatalf("empty series → want smhi_error, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadParam(t *testing.T) {
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadJSON(t *testing.T) {
	stubSMHI(t, 200, "{{{", nil)
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{"lat": 59.0, "lon": 18.0}}, nil)
	if r.Error == nil || r.Error.Code != "smhi_error" {
		t.Fatalf("bad json → want smhi_error, got %+v", r.Error)
	}
}

func TestCovExecuteForecastEmptySeries(t *testing.T) {
	stubSMHI(t, 200, `{"timeSeries":[]}`, nil)
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{"lat": 59.0, "lon": 18.0}}, nil)
	if r.Error == nil || r.Error.Code != "smhi_error" {
		t.Fatalf("empty series → want smhi_error, got %+v", r.Error)
	}
}

func TestCovAggregateDaysEdge(t *testing.T) {
	// Short time string skipped; entry with no temperature leaves Inf→0.
	ts := []smhiEntry{
		{Time: "short", Data: map[string]any{"air_temperature": 5.0}},
		{Time: "2026-06-24T12:00:00Z", Data: map[string]any{"symbol_code": float64(1)}},
	}
	out := aggregateDays(ts, 5)
	if len(out) != 1 {
		t.Fatalf("want 1 day, got %d", len(out))
	}
	if out[0].TempMin != 0 || out[0].TempMax != 0 {
		t.Fatalf("no-temp day should clamp Inf to 0, got %+v", out[0])
	}
}

func TestCovForecastSummaryEmpty(t *testing.T) {
	if forecastSummary(nil) != "No forecast available." {
		t.Fatal("empty forecast summary")
	}
	// Bad date label falls through to raw string.
	got := forecastSummary([]smhiDay{{Date: "bogus", TempMin: 1, TempMax: 2}})
	if !strings.Contains(got, "bogus") {
		t.Fatalf("bad date should pass through, got %q", got)
	}
}

func TestCovExecuteForecastTimeoutClamp(t *testing.T) {
	// Non-positive timeout_ms exercises the clamp branch in smhiGet.
	stubSMHI(t, 200, sample, nil)
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{"lat": 59.0, "lon": 18.0, "timeout_ms": 0}}, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
}
