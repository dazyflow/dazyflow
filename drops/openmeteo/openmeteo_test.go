// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package openmeteo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestCovExtractOMError(t *testing.T) {
	if got := extractOMError([]byte(`{"reason":"bad lat"}`)); got != "bad lat" {
		t.Fatalf("reason = %q", got)
	}
	if got := extractOMError([]byte("  plain  ")); got != "plain" {
		t.Fatalf("plain fallback = %q", got)
	}
	long := strings.Repeat("y", 500)
	if got := extractOMError([]byte(long)); len(got) != 300 {
		t.Fatalf("truncate len = %d", len(got))
	}
}

func TestCovHttpFailure(t *testing.T) {
	// Non-SSRF transport error.
	f := httpFailure(core.Job{}, 0, nil, errors.New("connection refused"))
	if f == nil || f.Error.Code != "openmeteo_http_error" {
		t.Fatalf("want openmeteo_http_error, got %+v", f)
	}
	// 401 without a reason body uses default message.
	f = httpFailure(core.Job{}, 401, []byte("not json"), nil)
	if f == nil || f.Error.Code != "auth" {
		t.Fatalf("want auth, got %+v", f)
	}
	// Generic non-2xx.
	f = httpFailure(core.Job{}, 500, []byte(`{"reason":"boom"}`), nil)
	if f == nil {
		t.Fatal("500 should fail")
	}
	// Success.
	if httpFailure(core.Job{}, 200, []byte("{}"), nil) != nil {
		t.Fatal("200 should be nil")
	}
}

func TestCovClassForDefault(t *testing.T) {
	if classFor(123) != "" {
		t.Fatal("unknown code should be empty class")
	}
}

func TestCovCurrentSummaryNoDesc(t *testing.T) {
	// weather_code 123 → no wmo description → leading description omitted.
	c := omCurrent{}
	c.Current.WeatherCode = 123
	c.Current.Temperature = 10
	got := currentSummary(c, "metric")
	if !strings.HasPrefix(got, "10.0°C") {
		t.Fatalf("no-desc summary should start with temp, got %q", got)
	}
}

func TestCovExecuteCurrentBadJSON(t *testing.T) {
	stubServer(t, 200, "not json", nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0}}, nil)
	if r.Error == nil || r.Error.Code != "openmeteo_error" {
		t.Fatalf("want openmeteo_error, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadParam(t *testing.T) {
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadJSON(t *testing.T) {
	stubServer(t, 200, "{{{", nil)
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0}}, nil)
	if r.Error == nil || r.Error.Code != "openmeteo_error" {
		t.Fatalf("want openmeteo_error, got %+v", r.Error)
	}
}

func TestCovExecuteCurrentTimeoutClamp(t *testing.T) {
	stubServer(t, 200, sampleCurrent, nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0, "timeout_ms": 0}}, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
}

func TestCovFlattenDailyShortColumns(t *testing.T) {
	// Time has 2 entries but other columns are shorter — defensive reads leave
	// missing fields zero.
	var r omDailyResponse
	r.Daily.Time = []string{"2026-06-24", "2026-06-25"}
	r.Daily.TempMax = []float64{20}
	r.Daily.WeatherCode = []int{2}
	out := flattenDaily(r, 7)
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d", len(out))
	}
	if out[1].TempMax != 0 || out[1].TempMin != 0 || out[1].Conditions != "" {
		t.Fatalf("short columns should leave fields zero, got %+v", out[1])
	}
}

func TestCovVerifyNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	prev := commercialURL
	commercialURL = srv.URL
	srv.Close()
	t.Cleanup(func() { commercialURL = prev })
	if err := verifyOpenMeteo(context.Background(), map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("unreachable host should fail verification")
	}
}

func TestCovVerifyNon2xx(t *testing.T) {
	stubServer(t, 503, `{"reason":"down"}`, nil)
	if err := verifyOpenMeteo(context.Background(), map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("503 should fail verification")
	}
}

func TestCovVerify401NoReason(t *testing.T) {
	stubServer(t, 401, "not json", nil)
	if err := verifyOpenMeteo(context.Background(), map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("401 should fail verification")
	}
}

func TestCovBaseQueryImperial(t *testing.T) {
	q := baseQuery(1, 2, "imperial")
	if q.Get("temperature_unit") != "fahrenheit" || q.Get("wind_speed_unit") != "mph" {
		t.Fatalf("imperial query = %v", url.Values(q))
	}
}
