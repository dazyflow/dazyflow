// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Editor map configuration: which tile server and which Nominatim-compatible
// geocoder the flow editor's map picker (web GeoPointField, the widget behind
// a param with format:"geo-point") talks to.
//
// This is DESIGN-TIME only and separate from the `geo` drop's run-time
// backends (drops/geo, DAZYFLOW_GEOCODER + friends): the picker always speaks
// Nominatim's /search?format=jsonv2, whereas a tenant's runs may use Photon or
// LocationIQ. The two are configured independently so pointing runs at
// LocationIQ doesn't drag the editor's map along with it.
//
// Both values are read by the browser directly, so both also feed the app's
// Content-Security-Policy (appCSP): their origins are what gets added to
// img-src and connect-src. Keeping one source of truth for "where the map
// talks to" is the point — a deployment that self-hosts tiles or Nominatim
// sets these, and the policy widens to match instead of silently blocking it.

import (
	"log"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultMapTileURL     = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
	defaultMapGeocoderURL = "https://nominatim.openstreetmap.org"
)

type mapAPI struct {
	TileURL     string
	GeocoderURL string
}

// mapAPI resolves the gateway's raw config into the effective values, applying
// the public-OpenStreetMap defaults. appCSP calls this too, so the URLs the
// browser is told to use and the origins the policy permits cannot drift.
func (h *HTTPGateway) mapAPI() *mapAPI {
	tile := strings.TrimSpace(h.MapTileURL)
	if tile == "" {
		tile = defaultMapTileURL
	}
	geocoder := strings.TrimRight(strings.TrimSpace(h.MapGeocoderURL), "/")
	if geocoder == "" {
		geocoder = defaultMapGeocoderURL
	}
	return &mapAPI{TileURL: tile, GeocoderURL: geocoder}
}

// getMapConfig tells the editor where to fetch tiles and geocode. Public and
// secret-free by design: these are two URLs the browser is about to request
// anyway, and the map picker lives deep inside the authenticated app, so
// threading a token down to it buys nothing. Mirrors GET /api/v1/auth/config,
// which likewise hands the pre-render config to whoever asks.
func (h *mapAPI) getMapConfig(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"tile_url":     h.TileURL,
		"geocoder_url": h.GeocoderURL,
	})
}

// cspOrigin reduces a URL to the "scheme://host[:port]" a CSP source list
// wants, or "" when there is nothing to add.
//
// Empty is the right answer in two different cases, both benign:
//   - a same-origin URL ("/tiles/{z}/{x}/{y}.png") — already covered by 'self'
//   - an unparseable one — logged, and left out rather than pasted into the
//     header, since a stray space or semicolon there would corrupt every
//     directive that follows it
func cspOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		log.Printf("map config: ignoring unusable URL %q for CSP (want an absolute http(s) URL)", raw)
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		log.Printf("map config: ignoring non-HTTP URL %q for CSP", raw)
		return ""
	}
	if strings.ContainsAny(u.Host, " ;,'\"") {
		log.Printf("map config: ignoring malformed host %q for CSP", u.Host)
		return ""
	}
	return u.Scheme + "://" + u.Host
}
