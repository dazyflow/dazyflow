package openmeteo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestCovFloatParam(t *testing.T) {
	cases := []struct {
		name string
		val  any
		ok   bool
		want float64
	}{
		{"float64", float64(1.5), true, 1.5},
		{"float32", float32(2), true, 2},
		{"int", int(3), true, 3},
		{"int64", int64(4), true, 4},
		{"jsonNumber", json.Number("5.5"), true, 5.5},
		{"badJsonNumber", json.Number("x"), false, 0},
		{"string", "6", false, 0},
		{"missing", nil, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{}
			if c.val != nil {
				m["k"] = c.val
			}
			got, ok := floatParam(m, "k")
			if ok != c.ok || (ok && got != c.want) {
				t.Fatalf("floatParam=%v,%v want %v,%v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestCovResolveCoordRanges(t *testing.T) {
	// Non-text coordinate input.
	if _, _, err := resolveCoord(core.Job{Input: map[string]core.Ref{"coordinate": {Inline: 1}}}); err == nil {
		t.Fatal("non-text coordinate should error")
	}
	// Bad coordinate text.
	if _, _, err := resolveCoord(core.Job{Input: map[string]core.Ref{"coordinate": {Inline: "nope"}}}); err == nil {
		t.Fatal("bad coordinate text should error")
	}
	// lon out of range (param path).
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": 1.0, "lon": 999.0}}); err == nil {
		t.Fatal("lon out of range should error")
	}
}

func TestCovUnitSymbols(t *testing.T) {
	if tempUnit("imperial") != "°F" || tempUnit("metric") != "°C" {
		t.Fatal("tempUnit")
	}
	if speedUnit("imperial") != "mph" || speedUnit("metric") != "m/s" {
		t.Fatal("speedUnit")
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

func TestCovCapitalizeFirst(t *testing.T) {
	if capitalizeFirst("") != "" || capitalizeFirst("rain") != "Rain" || capitalizeFirst("Rain") != "Rain" || capitalizeFirst("9") != "9" {
		t.Fatal("capitalizeFirst")
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
