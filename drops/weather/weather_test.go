package weather

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

// The drops dial a 127.0.0.1 httptest server, so they need the same
// private-egress opt-in production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
func init() { hfnet.SetAllowPrivateEgress(true) }

// Current Weather 2.5 (/data/2.5/weather) sample.
const sampleCurrent = `{
	"coord":{"lon":18.07,"lat":59.33},
	"weather":[{"id":800,"main":"Clear","description":"clear sky","icon":"01d"}],
	"main":{"temp":12.34,"feels_like":11.06,"pressure":1015,"humidity":64},
	"wind":{"speed":3.4,"deg":210},
	"clouds":{"all":20},
	"dt":1719223200,"name":"Stockholm","cod":200
}`

// 5-day/3-hour Forecast 2.5 (/data/2.5/forecast) sample: three 3-hour slots on
// 2024-06-24, one on 06-25, one on 06-26. timezone=0 so local day == UTC day.
const sampleForecast = `{
	"cod":"200","cnt":5,
	"list":[
		{"dt":1719219600,"main":{"temp":14,"temp_min":9.1,"temp_max":15},"pop":0.1,"weather":[{"id":801,"main":"Clouds","description":"few clouds"}]},
		{"dt":1719230400,"main":{"temp":16.2,"temp_min":13,"temp_max":18.7},"pop":0.2,"weather":[{"id":500,"main":"Rain","description":"light rain"}]},
		{"dt":1719241200,"main":{"temp":15,"temp_min":12,"temp_max":17},"pop":0.15,"weather":[{"id":802,"main":"Clouds","description":"scattered clouds"}]},
		{"dt":1719316800,"main":{"temp":17,"temp_min":10,"temp_max":20},"pop":0.0,"weather":[{"id":800,"main":"Clear","description":"clear sky"}]},
		{"dt":1719403200,"main":{"temp":18,"temp_min":11,"temp_max":21},"pop":0.5,"weather":[{"id":600,"main":"Snow","description":"light snow"}]}
	],
	"city":{"id":1,"name":"Stockholm","timezone":0}
}`

// stubServer points BOTH 2.5 endpoints at a test server that records the query
// it was called with and returns the given status + body. It restores them.
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
	prevC, prevF := currentURL, forecastURL
	currentURL, forecastURL = srv.URL, srv.URL
	t.Cleanup(func() {
		currentURL, forecastURL = prevC, prevF
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
	if _, _, err := resolveCoord(core.Job{Params: map[string]any{"lat": 0.0, "lon": 200.0}}); err == nil {
		t.Error("lon 200: want range error")
	}
}

func TestNormalizeUnitsAndSymbols(t *testing.T) {
	if normalizeUnits("METRIC") != "metric" || normalizeUnits("imperial") != "imperial" ||
		normalizeUnits("standard") != "standard" || normalizeUnits("bananas") != "metric" {
		t.Fatal("normalizeUnits mapping wrong")
	}
	if tempUnit("metric") != "°C" || tempUnit("imperial") != "°F" || tempUnit("standard") != "K" {
		t.Fatal("tempUnit wrong")
	}
	if speedUnit("imperial") != "mph" || speedUnit("metric") != "m/s" {
		t.Fatal("speedUnit wrong")
	}
}

func TestExecuteCurrent_ErrorPaths(t *testing.T) {
	r, _ := executeCurrent(context.Background(), core.Job{Params: map[string]any{"api_key": "k"}}, nil)
	if r.Status != core.StatusError || r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("missing coord: got %+v", r.Error)
	}
	r, _ = executeCurrent(context.Background(), core.Job{Params: map[string]any{"lat": 1.0, "lon": 2.0}}, nil)
	if r.Status != core.StatusError || r.Error == nil || r.Error.Code != "not_connected" {
		t.Fatalf("no key: got %+v", r.Error)
	}
}

func TestExecuteCurrent_Success(t *testing.T) {
	var q map[string][]string
	stubServer(t, 200, sampleCurrent, &q)

	job := core.Job{Params: map[string]any{"api_key": "testkey", "lat": 59.33, "lon": 18.07, "units": "metric"}}
	r, err := executeCurrent(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v errobj=%+v", r.Status, err, r.Error)
	}

	if got := textPin(t, r, "summary"); got != "Clear sky, 12.3°C (feels 11.1°C), humidity 64%, wind 3.4 m/s" {
		t.Errorf("summary = %q", got)
	}
	if got := textPin(t, r, "temperature"); got != "12.3" {
		t.Errorf("temperature = %q", got)
	}
	if got := textPin(t, r, "conditions"); got != "Clear" {
		t.Errorf("conditions = %q", got)
	}
	if _, ok := r.Output["weather"]; !ok {
		t.Error("missing weather pin")
	}

	// Request carried the right query and NO 'exclude' (that was a One Call param).
	if q["appid"][0] != "testkey" || q["units"][0] != "metric" || q["lat"][0] != "59.33" {
		t.Errorf("query = %v", map[string][]string(q))
	}
	if len(q["exclude"]) != 0 {
		t.Errorf("2.5 endpoint must not send 'exclude', got %v", q["exclude"])
	}
}

func TestExecuteCurrent_AuthError(t *testing.T) {
	const owmMsg = "Invalid API key. Please see https://openweathermap.org/faq#error401 for more info."
	stubServer(t, 401, `{"cod":401,"message":"`+owmMsg+`"}`, nil)
	job := core.Job{Params: map[string]any{"api_key": "bad", "lat": 1.0, "lon": 2.0}}
	r, _ := executeCurrent(context.Background(), job, nil)
	if r.Status != core.StatusError || r.Error == nil || r.Error.Code != "auth" {
		t.Fatalf("want auth error, got %+v", r.Error)
	}
	if !strings.Contains(r.Error.Message, owmMsg) {
		t.Errorf("run error should surface OpenWeather's message, got %q", r.Error.Message)
	}
}

func TestExecuteForecast_Success(t *testing.T) {
	var q map[string][]string
	stubServer(t, 200, sampleForecast, &q)

	job := core.Job{Params: map[string]any{"api_key": "k", "lat": 59.33, "lon": 18.07, "units": "metric", "days": 2}}
	r, err := executeForecast(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status=%v err=%v errobj=%+v", r.Status, err, r.Error)
	}

	summary := textPin(t, r, "summary")
	if strings.Count(summary, "\n") != 1 { // days=2 → 2 lines → 1 break
		t.Errorf("want 2 summary lines, got %q", summary)
	}
	// Day 1: aggregated min 9.1→9, max 18.7→19, pop 0.2→20%, noon slot = Rain.
	if !strings.Contains(summary, "Mon Jun 24: Light rain, 9–19°C, rain 20%") {
		t.Errorf("day-1 line off: %q", summary)
	}
	// Day 2: Clear, 10–20°C, 0%.
	if !strings.Contains(summary, "Clear sky, 10–20°C, rain 0%") {
		t.Errorf("day-2 line off: %q", summary)
	}
	// Day 3 (Snow) must be trimmed away by days=2.
	if strings.Contains(summary, "snow") {
		t.Errorf("third day leaked past days=2: %q", summary)
	}

	// Daily pin: 2 aggregated days, with the rolled-up values.
	daily, ok := r.Output["daily"].Inline.([]dayAgg)
	if !ok || len(daily) != 2 {
		t.Fatalf("daily pin = %T len %d, want 2 dayAgg", r.Output["daily"].Inline, len(daily))
	}
	if daily[0].Conditions != "Rain" || daily[0].TempMin != 9.1 || daily[0].TempMax != 18.7 || daily[0].Pop != 0.2 {
		t.Errorf("day-0 aggregate wrong: %+v", daily[0])
	}

	if len(q["exclude"]) != 0 {
		t.Errorf("2.5 endpoint must not send 'exclude', got %v", q["exclude"])
	}
}

func TestForecastSummary_Empty(t *testing.T) {
	if got := forecastSummary(nil, "metric"); got != "No forecast available." {
		t.Errorf("empty forecast = %q", got)
	}
}

func TestVerifyOpenWeather(t *testing.T) {
	if err := verifyOpenWeather(context.Background(), map[string]string{}); err == nil {
		t.Error("empty key: want error")
	}

	// 401 → surfaced verbatim.
	const owmMsg = "Invalid API key. Please see https://openweathermap.org/faq#error401 for more info."
	stubServer(t, 401, `{"cod":401,"message":"`+owmMsg+`"}`, nil)
	err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "bad"})
	if err == nil {
		t.Fatal("401: want error")
	}
	if err.Error() != owmMsg {
		t.Errorf("verify error should be OpenWeather's message verbatim, got %q", err.Error())
	}
}

func TestVerifyOpenWeather_OK(t *testing.T) {
	stubServer(t, 200, sampleCurrent, nil)
	if err := verifyOpenWeather(context.Background(), map[string]string{"api_key": "good"}); err != nil {
		t.Errorf("200: unexpected error %v", err)
	}
}

// guard against accidental schema drift in the typed structs the summary uses.
func TestSampleDecodes(t *testing.T) {
	var obs owmObservation
	if err := json.Unmarshal([]byte(sampleCurrent), &obs); err != nil {
		t.Fatal(err)
	}
	if obs.Main.Humidity != 64 || len(obs.Weather) != 1 || obs.Wind.Speed != 3.4 {
		t.Fatalf("decoded observation wrong: %+v", obs)
	}
	var fc owmForecast
	if err := json.Unmarshal([]byte(sampleForecast), &fc); err != nil {
		t.Fatal(err)
	}
	if len(fc.List) != 5 || fc.City.Timezone != 0 {
		t.Fatalf("decoded forecast wrong: %d slots, tz %d", len(fc.List), fc.City.Timezone)
	}
}
