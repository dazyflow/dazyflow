// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package openmeteo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// The drops dial a 127.0.0.1 httptest server, so they need the same
// private-egress opt-in production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
func init() { hfnet.SetAllowPrivateEgress(true) }

// /v1/forecast current= sample.
const sampleCurrent = `{
	"latitude":59.34,"longitude":18.06,
	"current_units":{"temperature_2m":"°C","wind_speed_10m":"m/s"},
	"current":{
		"time":"2026-06-24T13:00",
		"temperature_2m":18.3,
		"relative_humidity_2m":64,
		"apparent_temperature":17.1,
		"weather_code":2,
		"wind_speed_10m":3.4
	}
}`

// /v1/forecast daily= sample: column arrays aligned by index. Day 0 partly
// cloudy, day 1 slight rain (60% pop), day 2 slight snowfall.
const sampleForecast = `{
	"latitude":59.34,"longitude":18.06,"timezone":"Europe/Stockholm",
	"daily_units":{"temperature_2m_max":"°C"},
	"daily":{
		"time":["2026-06-24","2026-06-25","2026-06-26"],
		"weather_code":[2,61,71],
		"temperature_2m_max":[20.0,15.0,3.0],
		"temperature_2m_min":[12.0,9.0,-2.0],
		"precipitation_probability_max":[10,60,80]
	}
}`

// stubServer points BOTH hosts at a test server that records the query it was
// called with and returns the given status + body. It restores them.
func stubServer(t *testing.T, status int, body string, gotQuery *map[string][]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prevFree, prevComm := freeURL, commercialURL
	freeURL, commercialURL = srv.URL, srv.URL
	t.Cleanup(func() {
		freeURL, commercialURL = prevFree, prevComm
		srv.Close()
	})
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

func TestParseCoordinate(t *testing.T) {
	cases := []struct {
		in      string
		wantLat float64
		wantLon float64
		wantErr bool
	}{
		{"59.33,18.07", 59.33, 18.07, false},
		{"  40.71 , -74.01 ", 40.71, -74.01, false},
		{"59.33", 0, 0, true},         // no comma
		{"a,b", 0, 0, true},           // non-numeric
		{"59.33,18.07,1", 0, 0, true}, // too many parts
	}
	for _, c := range cases {
		lat, lon, err := parseCoordinate(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseCoordinate(%q): want error, got (%v,%v)", c.in, lat, lon)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCoordinate(%q): unexpected error %v", c.in, err)
			continue
		}
		if lat != c.wantLat || lon != c.wantLon {
			t.Errorf("parseCoordinate(%q) = (%v,%v), want (%v,%v)", c.in, lat, lon, c.wantLat, c.wantLon)
		}
	}
}

func TestResolveCoord(t *testing.T) {
	lat, lon, err := resolveCoord(core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07}})
	if err != nil || lat != 59.33 || lon != 18.07 {
		t.Fatalf("params path: got (%v,%v,%v)", lat, lon, err)
	}
	// Coordinate input overrides params.
	job := core.Job{
		Params: map[string]any{"lat": 1.0, "lon": 2.0},
		Input:  map[string]core.Ref{"coordinate": {Inline: "40.71,-74.01"}},
	}
	lat, lon, err = resolveCoord(job)
	if err != nil || lat != 40.71 || lon != -74.01 {
		t.Fatalf("input override: got (%v,%v,%v)", lat, lon, err)
	}
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{}}); err == nil {
		t.Error("missing coordinate: want error")
	}
	// A numeric string param must NOT be accepted as a coordinate.
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": "5", "lon": "5"}}); err == nil {
		t.Error("string lat/lon: want error (numbers only)")
	}
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": 91.0, "lon": 0.0}}); err == nil {
		t.Error("lat 91: want range error")
	}
}

func TestUnitsAndClass(t *testing.T) {
	if normalizeUnits("METRIC") != "metric" || normalizeUnits("imperial") != "imperial" || normalizeUnits("kelvin") != "metric" {
		t.Fatal("normalizeUnits mapping wrong")
	}
	if tempParam("metric") != "celsius" || tempParam("imperial") != "fahrenheit" {
		t.Fatal("tempParam wrong")
	}
	if windParam("metric") != "ms" || windParam("imperial") != "mph" {
		t.Fatal("windParam wrong")
	}
	for code, want := range map[int]string{0: "Clear", 1: "Clear", 2: "Clouds", 45: "Fog", 51: "Drizzle", 61: "Rain", 80: "Rain", 71: "Snow", 86: "Snow", 95: "Thunder"} {
		if got := classFor(code); got != want {
			t.Errorf("classFor(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestEndpointSelection(t *testing.T) {
	// No key → free host, no apikey on the wire.
	var q map[string][]string
	stubServer(t, 200, sampleCurrent, &q)
	job := core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07}}
	if r, _ := executeCurrent(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("free path status %v %+v", r.Status, r.Error)
	}
	if len(q["apikey"]) != 0 {
		t.Errorf("free path must not send apikey, got %v", q["apikey"])
	}

	// Key present → apikey forwarded.
	var q2 map[string][]string
	stubServer(t, 200, sampleCurrent, &q2)
	jobKey := core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07, "api_key": "secret"}}
	if r, _ := executeCurrent(context.Background(), jobKey, nil); r.Status != core.StatusOK {
		t.Fatalf("commercial path status %v %+v", r.Status, r.Error)
	}
	if len(q2["apikey"]) == 0 || q2["apikey"][0] != "secret" {
		t.Errorf("commercial path must forward apikey, got %v", q2["apikey"])
	}
}

func TestExecuteCurrent_Success(t *testing.T) {
	var q map[string][]string
	stubServer(t, 200, sampleCurrent, &q)

	job := core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07, "units": "metric"}}
	r, err := executeCurrent(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v errobj=%+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "summary"); got != "Partly cloudy, 18.3°C (feels 17.1°C), humidity 64%, wind 3.4 m/s" {
		t.Errorf("summary = %q", got)
	}
	if got := textPin(t, r, "temperature"); got != "18.3" {
		t.Errorf("temperature = %q", got)
	}
	if got := textPin(t, r, "conditions"); got != "Clouds" {
		t.Errorf("conditions = %q", got)
	}
	if _, ok := r.Output["weather"]; !ok {
		t.Error("missing weather pin")
	}
	if q["latitude"][0] != "59.33" || q["temperature_unit"][0] != "celsius" || q["wind_speed_unit"][0] != "ms" {
		t.Errorf("query = %v", map[string][]string(q))
	}
}

func TestExecuteCurrent_BadParam_NoNetwork(t *testing.T) {
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Status != core.StatusError || r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("missing coord: got %+v", r.Error)
	}
}

func TestExecuteCurrent_AuthError(t *testing.T) {
	const reason = "API key invalid."
	stubServer(t, 401, `{"error":true,"reason":"`+reason+`"}`, nil)
	job := core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0, "api_key": "bad"}}
	r, _ := executeCurrent(context.Background(), job, nil)
	if r.Status != core.StatusError || r.Error == nil || r.Error.Code != "auth" {
		t.Fatalf("want auth error, got %+v", r.Error)
	}
	if !strings.Contains(r.Error.Message, reason) {
		t.Errorf("run error should surface Open-Meteo's reason, got %q", r.Error.Message)
	}
}

func TestExecuteForecast_Success(t *testing.T) {
	var q map[string][]string
	stubServer(t, 200, sampleForecast, &q)

	job := core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07, "units": "metric", "days": 2}}
	r, err := executeForecast(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v errobj=%+v", r.Status, err, r.Error)
	}

	summary := textPin(t, r, "summary")
	if strings.Count(summary, "\n") != 1 { // days=2 → 2 lines → 1 break
		t.Errorf("want 2 summary lines, got %q", summary)
	}
	if !strings.Contains(summary, "Wed Jun 24: Partly cloudy, 12–20°C, rain 10%") {
		t.Errorf("day-1 line off: %q", summary)
	}
	if !strings.Contains(summary, "Slight rain, 9–15°C, rain 60%") {
		t.Errorf("day-2 line off: %q", summary)
	}
	// Day 3 (snow) must be trimmed away by days=2.
	if strings.Contains(strings.ToLower(summary), "snow") {
		t.Errorf("third day leaked past days=2: %q", summary)
	}

	daily, ok := r.Output["daily"].Inline.([]dayEntry)
	if !ok || len(daily) != 2 {
		t.Fatalf("daily pin = %T len %d, want 2 dayEntry", r.Output["daily"].Inline, len(daily))
	}
	if daily[1].Conditions != "Rain" || daily[1].TempMin != 9.0 || daily[1].TempMax != 15.0 || daily[1].Pop != 0.6 {
		t.Errorf("day-1 row wrong: %+v", daily[1])
	}

	if q["forecast_days"][0] != "2" || q["timezone"][0] != "auto" {
		t.Errorf("forecast query = %v", map[string][]string(q))
	}
}

func TestForecastSummary_Empty(t *testing.T) {
	if got := forecastSummary(nil, "metric"); got != "No forecast available." {
		t.Errorf("empty forecast = %q", got)
	}
}

func TestVerifyOpenMeteo(t *testing.T) {
	// Empty key is a valid configuration (free endpoint is key-less).
	if err := verifyOpenMeteo(context.Background(), map[string]string{}); err != nil {
		t.Errorf("empty key should verify, got %v", err)
	}

	// 401 → reason surfaced verbatim.
	const reason = "API key invalid."
	stubServer(t, 401, `{"error":true,"reason":"`+reason+`"}`, nil)
	err := verifyOpenMeteo(context.Background(), map[string]string{"api_key": "bad"})
	if err == nil || err.Error() != reason {
		t.Errorf("401: want reason %q, got %v", reason, err)
	}
}

func TestVerifyOpenMeteo_OK(t *testing.T) {
	stubServer(t, 200, sampleCurrent, nil)
	if err := verifyOpenMeteo(context.Background(), map[string]string{"api_key": "good"}); err != nil {
		t.Errorf("200: unexpected error %v", err)
	}
}
