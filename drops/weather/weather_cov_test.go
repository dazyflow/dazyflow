package weather

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func keyParams(extra map[string]any) map[string]any {
	p := map[string]any{"api_key": "test-key"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

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
	if _, _, err := resolveCoord(core.Job{Input: map[string]core.Ref{"coordinate": {Inline: 1}}}); err == nil {
		t.Fatal("non-text coordinate should error")
	}
	if _, _, err := resolveCoord(core.Job{Input: map[string]core.Ref{"coordinate": {Inline: "nope"}}}); err == nil {
		t.Fatal("bad coordinate text should error")
	}
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": 1.0, "lon": 999.0}}); err == nil {
		t.Fatal("lon out of range should error")
	}
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": 99.0, "lon": 1.0}}); err == nil {
		t.Fatal("lat out of range should error")
	}
}

func TestCovOwmGetNoKey(t *testing.T) {
	_, _, err := owmGet(context.Background(), core.Job{Params: map[string]any{}}, currentURL, 1, 2)
	if err == nil {
		t.Fatal("missing key should error")
	}
}

func TestCovUnitSymbols(t *testing.T) {
	if tempUnit("imperial") != "°F" || tempUnit("standard") != "K" || tempUnit("metric") != "°C" {
		t.Fatal("tempUnit")
	}
	if speedUnit("imperial") != "mph" || speedUnit("metric") != "m/s" {
		t.Fatal("speedUnit")
	}
	if normalizeUnits("standard") != "standard" || normalizeUnits("junk") != "metric" {
		t.Fatal("normalizeUnits")
	}
}

func TestCovExtractOWMError(t *testing.T) {
	if got := extractOWMError([]byte(`{"message":"Invalid API key"}`)); got != "Invalid API key" {
		t.Fatalf("message = %q", got)
	}
	if got := extractOWMError([]byte("  plain  ")); got != "plain" {
		t.Fatalf("plain = %q", got)
	}
	long := strings.Repeat("z", 500)
	if got := extractOWMError([]byte(long)); len(got) != 300 {
		t.Fatalf("truncate len = %d", len(got))
	}
}

func TestCovHttpFailure(t *testing.T) {
	f := httpFailure(core.Job{}, 0, nil, errors.New("connection refused"))
	if f == nil || f.Error.Code != "owm_http_error" {
		t.Fatalf("want owm_http_error, got %+v", f)
	}
	f = httpFailure(core.Job{}, 401, []byte("not json"), nil)
	if f == nil || f.Error.Code != "auth" {
		t.Fatalf("want auth, got %+v", f)
	}
	f = httpFailure(core.Job{}, 401, []byte(`{"message":"bad key"}`), nil)
	if f == nil || !strings.Contains(f.Error.Message, "bad key") {
		t.Fatalf("401 should surface message, got %+v", f)
	}
	f = httpFailure(core.Job{}, 500, []byte(`{"message":"boom"}`), nil)
	if f == nil {
		t.Fatal("500 should fail")
	}
	if httpFailure(core.Job{}, 200, []byte("{}"), nil) != nil {
		t.Fatal("200 should be nil")
	}
}

func TestCovCapitalizeFirst(t *testing.T) {
	if capitalizeFirst("") != "" || capitalizeFirst("rain") != "Rain" || capitalizeFirst("Rain") != "Rain" || capitalizeFirst("9") != "9" {
		t.Fatal("capitalizeFirst")
	}
}

func TestCovCurrentSummaryNoDesc(t *testing.T) {
	var o owmObservation
	o.Main.Temp = 10
	o.Main.Humidity = 50
	got := currentSummary(o, "metric")
	if !strings.HasPrefix(got, "10.0°C") {
		t.Fatalf("no-desc summary should start with temp, got %q", got)
	}
}

func TestCovExecuteCurrentNotConnected(t *testing.T) {
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0}}, nil)
	if r.Error == nil || r.Error.Code != "not_connected" {
		t.Fatalf("want not_connected, got %+v", r.Error)
	}
}

func TestCovExecuteCurrentBadParam(t *testing.T) {
	r, _ := executeCurrent(context.Background(), core.Job{Params: keyParams(nil)}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
}

func TestCovExecuteCurrentBadJSON(t *testing.T) {
	stubServer(t, 200, "not json", nil)
	r, _ := executeCurrent(context.Background(), core.Job{Params: keyParams(map[string]any{"lat": 1.0, "lon": 2.0})}, nil)
	if r.Error == nil || r.Error.Code != "owm_error" {
		t.Fatalf("want owm_error, got %+v", r.Error)
	}
}

func TestCovExecuteForecastNotConnected(t *testing.T) {
	r, _ := executeForecast(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0}}, nil)
	if r.Error == nil || r.Error.Code != "not_connected" {
		t.Fatalf("want not_connected, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadParam(t *testing.T) {
	r, _ := executeForecast(context.Background(), core.Job{Params: keyParams(nil)}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
}

func TestCovExecuteForecastBadJSON(t *testing.T) {
	stubServer(t, 200, "{{{", nil)
	r, _ := executeForecast(context.Background(), core.Job{Params: keyParams(map[string]any{"lat": 1.0, "lon": 2.0})}, nil)
	if r.Error == nil || r.Error.Code != "owm_error" {
		t.Fatalf("want owm_error, got %+v", r.Error)
	}
}

func TestCovExecuteCurrentLangAndTimeout(t *testing.T) {
	// Exercises the lang param branch and the timeout clamp in owmGet.
	var q map[string][]string
	stubServer(t, 200, sampleCurrent, &q)
	job := core.Job{Params: keyParams(map[string]any{"lat": 1.0, "lon": 2.0, "lang": "sv", "timeout_ms": 0})}
	r, _ := executeCurrent(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if len(q["lang"]) == 0 || q["lang"][0] != "sv" {
		t.Fatalf("lang should be forwarded, got %v", q["lang"])
	}
}

func TestCovVerifyOpenWeather(t *testing.T) {
	// Empty key.
	if err := verifyOpenWeather(context.Background(), map[string]string{}); err == nil {
		t.Fatal("empty key should error")
	}
	// 401 with message.
	stubServer(t, 401, `{"message":"Invalid API key"}`, nil)
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "bad"}); err == nil || err.Error() != "Invalid API key" {
		t.Fatalf("401 should surface message, got %v", err)
	}
}

func TestCovVerify401NoMessage(t *testing.T) {
	stubServer(t, 401, "not json", nil)
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "bad"}); err == nil {
		t.Fatal("401 should fail")
	}
}

func TestCovVerifyNon2xx(t *testing.T) {
	stubServer(t, 503, `{"message":"down"}`, nil)
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("503 should fail")
	}
}

func TestCovVerifyOK(t *testing.T) {
	stubServer(t, 200, sampleCurrent, nil)
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "good"}); err != nil {
		t.Fatalf("200 should verify, got %v", err)
	}
}

func TestCovVerifyNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	prev := currentURL
	currentURL = srv.URL
	srv.Close()
	t.Cleanup(func() { currentURL = prev })
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "k"}); err == nil {
		t.Fatal("unreachable should fail")
	}
}
