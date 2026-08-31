// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// These cases are the union of the coordinate/unit tests weather, openmeteo,
// smhi and geo each carried against their own copy of these helpers. The
// user-facing error strings are contract, so the assertions that pinned them
// live here now.
package geoloc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantErr  bool
		lat, lon float64
	}{
		{"ok", "59.33,18.07", false, 59.33, 18.07},
		{"ok_padded", "  40.71 , -74.01 ", false, 40.71, -74.01},
		{"ok_negative", "-12.5,-77.0", false, -12.5, -77.0},
		{"no_comma", "59.33", true, 0, 0},
		{"wrong_arity", "59.33,18.07,1", true, 0, 0},
		{"both_nan", "a,b", true, 0, 0},
		{"lat_nan", "abc,18.07", true, 0, 0},
		{"lon_nan", "59.33,xyz", true, 0, 0},
		{"lat_over", "91,0", true, 0, 0},
		{"lat_under", "-91,0", true, 0, 0},
		{"lon_over", "0,200", true, 0, 0},
		{"lon_under", "0,-200", true, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lat, lon, err := Parse(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q): want error, got (%v,%v)", c.in, lat, lon)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error %v", c.in, err)
			}
			if lat != c.lat || lon != c.lon {
				t.Errorf("Parse(%q) = (%v,%v), want (%v,%v)", c.in, lat, lon, c.lat, c.lon)
			}
		})
	}
}

func TestCheckRange(t *testing.T) {
	if err := CheckRange(59.33, 18.07); err != nil {
		t.Fatalf("in-range: %v", err)
	}
	for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}} {
		if err := CheckRange(c[0], c[1]); err == nil {
			t.Errorf("CheckRange(%v,%v): want error", c[0], c[1])
		}
	}
}

func TestNum(t *testing.T) {
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
		{"badJsonNumber", json.Number("nope"), false, 0},
		// A numeric string must NOT be accepted — a coordinate is a number, and
		// refusing strings keeps a mis-wired text param from passing for one.
		{"string", "6", false, 0},
		{"missing", nil, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{}
			if c.val != nil {
				m["k"] = c.val
			}
			got, ok := Num(m, "k")
			if ok != c.ok || (ok && got != c.want) {
				t.Fatalf("Num=%v,%v want %v,%v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestResolveLatLon(t *testing.T) {
	lat, lon, err := ResolveLatLon(core.Job{Params: map[string]any{"lat": 59.33, "lon": 18.07}})
	if err != nil || lat != 59.33 || lon != 18.07 {
		t.Fatalf("params path: got (%v,%v,%v)", lat, lon, err)
	}

	// Coordinate input overrides params.
	job := core.Job{
		Params: map[string]any{"lat": 1.0, "lon": 2.0},
		Input:  map[string]core.Ref{"coordinate": {Inline: "40.71,-74.01"}},
	}
	lat, lon, err = ResolveLatLon(job)
	if err != nil || lat != 40.71 || lon != -74.01 {
		t.Fatalf("input override: got (%v,%v,%v)", lat, lon, err)
	}

	// Coordinate text alone is enough.
	lat, lon, err = ResolveLatLon(core.Job{Input: map[string]core.Ref{"coordinate": {Inline: "10,20"}}})
	if err != nil || lat != 10 || lon != 20 {
		t.Fatalf("coordinate text path: %v %v %v", lat, lon, err)
	}

	bad := []struct {
		name string
		job  core.Job
	}{
		{"missing", core.Job{Params: map[string]any{}}},
		{"string_params", core.Job{Params: map[string]any{"lat": "5", "lon": "5"}}},
		{"lat_over", core.Job{Params: map[string]any{"lat": 91.0, "lon": 0.0}}},
		{"lat_way_over", core.Job{Params: map[string]any{"lat": 99.0, "lon": 1.0}}},
		{"lon_over", core.Job{Params: map[string]any{"lat": 0.0, "lon": 200.0}}},
		{"lon_way_over", core.Job{Params: map[string]any{"lat": 1.0, "lon": 999.0}}},
		{"non_text_input", core.Job{Input: map[string]core.Ref{"coordinate": {Inline: 1}}}},
		{"unparseable_input", core.Job{Input: map[string]core.Ref{"coordinate": {Inline: "nope"}}}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := ResolveLatLon(c.job); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestFmt(t *testing.T) {
	// Trailing zeros are trimmed so a tidy point stays tidy.
	if got := Fmt(59.3293, 18.0686); got != "59.3293,18.0686" {
		t.Errorf("Fmt = %q", got)
	}
	if got := Fmt(10, -20); got != "10,-20" {
		t.Errorf("Fmt = %q", got)
	}
	// Round-trips through Parse.
	lat, lon, err := Parse(Fmt(-12.5, -77))
	if err != nil || lat != -12.5 || lon != -77 {
		t.Errorf("round-trip: (%v,%v,%v)", lat, lon, err)
	}
}

func TestUnitSymbols(t *testing.T) {
	if TempUnit("metric") != "°C" || TempUnit("imperial") != "°F" || TempUnit("standard") != "K" {
		t.Error("TempUnit wrong")
	}
	// Connectors without a Kelvin option never pass "standard"; anything
	// unrecognised still reads as metric rather than blank.
	if TempUnit("") != "°C" || TempUnit("junk") != "°C" {
		t.Error("TempUnit default wrong")
	}
	if SpeedUnit("imperial") != "mph" || SpeedUnit("metric") != "m/s" || SpeedUnit("") != "m/s" {
		t.Error("SpeedUnit wrong")
	}
}

func TestNumFormats(t *testing.T) {
	if Num1(12.34) != "12.3" || Num1(12) != "12.0" {
		t.Errorf("Num1: %q %q", Num1(12.34), Num1(12))
	}
	if Num0(12.6) != "13" || Num0(-0.4) != "-0" {
		t.Errorf("Num0: %q %q", Num0(12.6), Num0(-0.4))
	}
}

func TestCapitalizeFirst(t *testing.T) {
	cases := map[string]string{
		"clear sky": "Clear sky",
		"Clear sky": "Clear sky",
		"":          "",
		"1 drop":    "1 drop",
		// Non-ASCII keeps its own casing — localized descriptions are already
		// cased by the provider.
		"åska": "åska",
	}
	for in, want := range cases {
		if got := CapitalizeFirst(in); got != want {
			t.Errorf("CapitalizeFirst(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTransportFailure(t *testing.T) {
	if r := TransportFailure(core.Job{}, "smhi", "SMHI", nil); r != nil {
		t.Fatalf("nil error should fall through, got %+v", r)
	}
	r := TransportFailure(core.Job{}, "smhi", "SMHI", context.DeadlineExceeded)
	if r == nil || r.Error == nil || r.Error.Code != "smhi_http_error" {
		t.Fatalf("want smhi_http_error, got %+v", r)
	}
	if r.Error.Message == "" || r.Error.Message[:16] != "Couldn't reach S" {
		t.Errorf("message should name the service, got %q", r.Error.Message)
	}
	// A plain error is not an SSRF refusal, so it must not claim egress blocked.
	if r2 := TransportFailure(core.Job{}, "owm", "OpenWeather", errors.New("boom")); r2.Error.Code == "egress_blocked" {
		t.Error("plain error misclassified as egress_blocked")
	}
}

func TestTransportFailure_SSRFBlocked(t *testing.T) {
	// hfnet.IsSSRFError keys off the "ssrf_blocked" marker the guarded dialer
	// puts in its error, so an egress refusal gets the explaining code rather
	// than looking like an ordinary network hiccup.
	r := TransportFailure(core.Job{}, "owm", "OpenWeather", errors.New("dial tcp: ssrf_blocked: private address"))
	if r == nil || r.Error == nil || r.Error.Code != "egress_blocked" {
		t.Fatalf("want egress_blocked, got %+v", r)
	}
	if !strings.Contains(r.Error.Message, "OpenWeather") ||
		!strings.Contains(r.Error.Message, "egress policy") {
		t.Errorf("message should name the service and the cause, got %q", r.Error.Message)
	}
}

func TestProbe(t *testing.T) {
	// Probe reaches a loopback test server only with the private-egress opt-in
	// production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
	prev := hfnet.PrivateEgressAllowed()
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"message":"Invalid API key"}`))
	}))
	defer srv.Close()

	// A non-2xx is a status, not an error — the caller decides what it means.
	status, body, err := Probe(context.Background(), "OpenWeather", srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != 401 || !strings.Contains(string(body), "Invalid API key") {
		t.Fatalf("got (%d, %q)", status, body)
	}

	// A dial failure is labelled with the service name.
	srv.Close()
	if _, _, err := Probe(context.Background(), "OpenWeather", srv.URL); err == nil {
		t.Error("closed server: want error")
	} else if !strings.Contains(err.Error(), "could not reach OpenWeather") {
		t.Errorf("error should name the service, got %v", err)
	}

	// An unparseable URL fails before dialing.
	if _, _, err := Probe(context.Background(), "OpenWeather", "http://[::1]:namedport/"); err == nil {
		t.Error("bad URL: want error")
	}
}

func TestProbe_SSRFGuarded(t *testing.T) {
	// Without the private-egress opt-in a loopback target is refused by the
	// guarded dialer. The refusal is wrapped as a dial failure (that is where
	// it happens), but keeps the ssrf_blocked marker TransportFailure keys on.
	prev := hfnet.PrivateEgressAllowed()
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(prev)

	_, _, err := Probe(context.Background(), "OpenWeather", "http://127.0.0.1:9/")
	if err == nil {
		t.Fatal("private address without opt-in: want error")
	}
	if !hfnet.IsSSRFError(err) {
		t.Errorf("error should carry the SSRF marker, got %v", err)
	}
}

func TestProbe_EgressAllowlist(t *testing.T) {
	// An allowlist that excludes the host is refused by policy BEFORE any dial,
	// and returned verbatim — it already explains itself to the Apps page, so it
	// must not be dressed up as "could not reach …".
	if err := hfnet.SetEgressAllowlist([]string{"example.com"}); err != nil {
		t.Fatalf("SetEgressAllowlist: %v", err)
	}
	defer func() {
		if err := hfnet.SetEgressAllowlist(nil); err != nil {
			t.Errorf("reset allowlist: %v", err)
		}
	}()

	_, _, err := Probe(context.Background(), "OpenWeather", "https://api.openweathermap.org/data/2.5/weather")
	if err == nil {
		t.Fatal("host outside allowlist: want error")
	}
	if !strings.Contains(err.Error(), "egress_blocked") {
		t.Errorf("want the policy refusal verbatim, got %v", err)
	}
	if strings.Contains(err.Error(), "could not reach") {
		t.Errorf("policy refusal should not be wrapped as a dial failure, got %v", err)
	}
}
