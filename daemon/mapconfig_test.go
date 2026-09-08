// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMapAPI_DefaultsToPublicOSM(t *testing.T) {
	t.Parallel()
	m := (&HTTPGateway{}).mapAPI()
	if m.TileURL != defaultMapTileURL {
		t.Errorf("TileURL = %q, want %q", m.TileURL, defaultMapTileURL)
	}
	if m.GeocoderURL != defaultMapGeocoderURL {
		t.Errorf("GeocoderURL = %q, want %q", m.GeocoderURL, defaultMapGeocoderURL)
	}
}

// A self-hosted geocoder is configured with or without a trailing slash; the
// client appends "/search", so the stored value must not end in one.
func TestMapAPI_TrimsGeocoderTrailingSlash(t *testing.T) {
	t.Parallel()
	m := (&HTTPGateway{MapGeocoderURL: "  https://nom.internal/  "}).mapAPI()
	if m.GeocoderURL != "https://nom.internal" {
		t.Errorf("GeocoderURL = %q, want %q", m.GeocoderURL, "https://nom.internal")
	}
}

// The default policy must admit the public OSM hosts, or the out-of-the-box
// map picker is broken — which is exactly how this shipped.
func TestAppCSP_AllowsDefaultMapHosts(t *testing.T) {
	t.Parallel()
	csp := (&HTTPGateway{}).appCSP()
	for _, want := range []string{
		"img-src 'self' data: blob: https://tile.openstreetmap.org;",
		"connect-src 'self' https://nominatim.openstreetmap.org;",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q\ngot: %s", want, csp)
		}
	}
}

// Configuring self-hosted map services must move the CSP with them: allowing
// the operator's own host is the whole point of making these configurable.
func TestAppCSP_FollowsConfiguredMapHosts(t *testing.T) {
	t.Parallel()
	csp := (&HTTPGateway{
		MapTileURL:     "https://tiles.internal:8443/{z}/{x}/{y}.png",
		MapGeocoderURL: "http://nom.internal:7070",
	}).appCSP()
	if !strings.Contains(csp, "img-src 'self' data: blob: https://tiles.internal:8443;") {
		t.Errorf("tile origin not in img-src\ngot: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' http://nom.internal:7070;") {
		t.Errorf("geocoder origin not in connect-src\ngot: %s", csp)
	}
	// The public hosts must NOT linger once overridden — leaving them in would
	// quietly keep a locked-down deployment talking to openstreetmap.org.
	if strings.Contains(csp, "openstreetmap.org") {
		t.Errorf("overridden CSP still allows the public OSM hosts\ngot: %s", csp)
	}
}

// A deployment proxying tiles under its own domain needs no new source; 'self'
// already covers it, and adding a bare path to the directive would be invalid.
func TestAppCSP_SameOriginMapNeedsNoSource(t *testing.T) {
	t.Parallel()
	csp := (&HTTPGateway{
		MapTileURL:     "/tiles/{z}/{x}/{y}.png",
		MapGeocoderURL: "/geocode",
	}).appCSP()
	if !strings.Contains(csp, "img-src 'self' data: blob:;") {
		t.Errorf("img-src should be untouched for a same-origin tile URL\ngot: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self';") {
		t.Errorf("connect-src should be untouched for a same-origin geocoder\ngot: %s", csp)
	}
}

// The directives this policy exists for must survive the rewrite.
func TestAppCSP_KeepsHardeningDirectives(t *testing.T) {
	t.Parallel()
	csp := (&HTTPGateway{}).appCSP()
	for _, want := range []string{
		"default-src 'self';",
		"script-src 'self';",
		"object-src 'none';",
		"base-uri 'self';",
		"form-action 'self';",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q\ngot: %s", want, csp)
		}
	}
	// script-src must stay inline-free; that is the directive that matters.
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("script-src must not allow inline script\ngot: %s", csp)
	}
}

// A junk URL must be dropped, not pasted into the header: a stray space or
// semicolon there would corrupt every directive after it, which is a far worse
// outcome than one unreachable tile host.
func TestCSPOrigin_RejectsUnusableURLs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"absolute https", "https://tile.example.org/{z}/{x}/{y}.png", "https://tile.example.org"},
		{"port preserved", "http://host.example:8080/x", "http://host.example:8080"},
		{"empty", "", ""},
		{"same origin", "/tiles/{z}/{x}/{y}.png", ""},
		{"no scheme", "tile.example.org/x", ""},
		{"non-http scheme", "ftp://tile.example.org/x", ""},
		{"injection via host", "https://evil.example' 'unsafe-inline/x", ""},
		{"injection via semicolon", "https://evil.example;script-src *", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cspOrigin(tc.in); got != tc.want {
				t.Errorf("cspOrigin(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The editor reads its two URLs from here. Whatever the CSP allows, this must
// report — they are resolved from one place so they cannot disagree.
func TestGetMapConfig_ReportsEffectiveURLs(t *testing.T) {
	t.Parallel()
	gw := &HTTPGateway{MapTileURL: "https://tiles.internal/{z}/{x}/{y}.png"}
	rw := httptest.NewRecorder()
	gw.mapAPI().getMapConfig(rw, httptest.NewRequest("GET", "/api/v1/map/config", nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
	var got struct {
		TileURL     string `json:"tile_url"`
		GeocoderURL string `json:"geocoder_url"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rw.Body.String())
	}
	if got.TileURL != "https://tiles.internal/{z}/{x}/{y}.png" {
		t.Errorf("tile_url = %q", got.TileURL)
	}
	// Unset falls back to the public default rather than an empty string the
	// client would have to interpret.
	if got.GeocoderURL != defaultMapGeocoderURL {
		t.Errorf("geocoder_url = %q, want %q", got.GeocoderURL, defaultMapGeocoderURL)
	}
	// Every origin the config advertises must be one the policy permits.
	csp := gw.appCSP()
	if !strings.Contains(csp, cspOrigin(got.TileURL)) {
		t.Errorf("advertised tile host is not allowed by the CSP\ncsp: %s", csp)
	}
	if !strings.Contains(csp, cspOrigin(got.GeocoderURL)) {
		t.Errorf("advertised geocoder host is not allowed by the CSP\ncsp: %s", csp)
	}
}
