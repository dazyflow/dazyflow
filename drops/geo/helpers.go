// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package geo hosts the OpenStreetMap connector: pick a location on a map and
// emit its coordinate (a design-time value source), turn a place name into a
// coordinate (forward geocode), and turn a coordinate back into a place name
// (reverse geocode).
//
// The map-picker drop (geo_location) is a pure value source — its coordinate
// is chosen in the editor via a Leaflet/OpenStreetMap widget (format
// "geo-point") and parsed at run time, no network. The Location Place override
// and geo_reverse drops geocode at run time through a pluggable backend.
//
// # Geocoding backends
//
// The backend is chosen per tenant via the OpenStreetMap connection (the
// `backend`, `base_url`, and `api_key` ConnectionFields, set under Apps), with
// DAZYFLOW_GEOCODER as the deployment-wide default and Nominatim as the final
// fallback. See geocoderFor. Supported backends:
//
//   - nominatim — OpenStreetMap's Nominatim API (the reference, default, no
//     key). The public instance (https://nominatim.openstreetmap.org) is
//     rate-limited to ~1 req/s and forbids bulk use; point base_url (or
//     DAZYFLOW_NOMINATIM_URL) at a self-hosted instance for real load.
//   - photon — Komoot's Photon (also OpenStreetMap data, GeoJSON API, no key).
//     Its public instance (https://photon.komoot.io) is fair-use; self-host via
//     base_url or DAZYFLOW_PHOTON_URL. Good for autocomplete/typo tolerance.
//   - locationiq — LocationIQ's Nominatim-compatible API (OpenStreetMap data,
//     hosted, REQUIRES an api_key). A no-infrastructure paid option.
//
// Every backend normalizes to geoPlace, so the drops are backend-agnostic.
// Output coordinates are the "lat,lon" string the OpenWeather/SMHI drops'
// Coordinate input accepts, so a geocode/picker wires straight into a weather
// lookup. All dials go through the shared SSRF-guarded client.
package geo

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// geoConnectionFields is the per-tenant geocoding connection shared by every
// geo drop (they all belong to the OpenStreetMap integration, so the values are
// set once under Apps). All fields are OPTIONAL: with none set the drops use the
// keyless OpenStreetMap default and work out of the box; a tenant configures
// these only to self-host or move to a key-based provider. backend is an enum
// (rendered as a dropdown); base_url and api_key are free text / secret.
var geoConnectionFields = []core.ConnectionField{
	{
		Key:         "backend",
		Label:       "Geocoding backend",
		Options:     []string{"nominatim", "photon", "locationiq"},
		Placeholder: "nominatim (default)",
	},
	{
		Key:   "base_url",
		Label: "Custom API URL",
		Help:  "Optional — the base URL of your self-hosted Nominatim, Photon or LocationIQ.",
	},
	{
		Key:    "api_key",
		Label:  "API key",
		Secret: true,
		Help:   "Required for LocationIQ. Leave blank for Nominatim or Photon.",
	},
}

// userAgent identifies dazyflow to the geocoder — Nominatim's policy rejects
// requests without a real User-Agent (HTTP 403), so this is mandatory, not
// cosmetic. Photon doesn't require it but accepts it.
const userAgent = "dazyflow (+https://github.com/dazyflow/dazyflow)"

const maxResponseBytes = 2 << 20 // 2 MiB — a geocode hit is a few KiB.

// geoPlace is a backend-neutral geocoding result. Each backend maps its own
// response (Nominatim JSON, Photon GeoJSON, …) onto this shape so the drops
// consume one type regardless of provider.
type geoPlace struct {
	Lat, Lon    float64 // parsed coordinate
	Coord       string  // canonical "lat,lon" (trimmed), ready to wire onward
	DisplayName string  // human-readable place name
	Address     any     // structured address object (decoded JSON), or nil
	Raw         any     // the backend's full response (decoded JSON), for "result"
}

// geocoder turns place names ↔ coordinates against a specific backend. forward
// and reverse return either a populated geoPlace (and a nil *core.Result) or a
// zero geoPlace and an error Result the caller returns to the engine directly.
type geocoder interface {
	// label names the backing service for error messages ("OpenStreetMap").
	label() string
	// forward geocodes a free-text place query, returning the best match.
	forward(ctx context.Context, job core.Job, query string) (geoPlace, *core.Result)
	// reverse geocodes a coordinate into a place.
	reverse(ctx context.Context, job core.Job, lat, lon float64) (geoPlace, *core.Result)
}

// defaultGeocoderName is the deployment-wide fallback backend (DAZYFLOW_GEOCODER),
// used when a tenant's connection doesn't choose one. Empty → Nominatim.
var defaultGeocoderName = strings.ToLower(strings.TrimSpace(os.Getenv("DAZYFLOW_GEOCODER")))

// geocoderFor selects the backend for a job. Precedence: the tenant's
// connection `backend` field (set under the OpenStreetMap integration) wins,
// then the deployment default (DAZYFLOW_GEOCODER), then Nominatim. The connection
// value reaches job.Params via injectConnectionDefaults, like every other
// ConnectionField.
func geocoderFor(job core.Job) geocoder {
	name := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "backend", "")))
	if name == "" {
		name = defaultGeocoderName
	}
	return newGeocoder(name)
}

// newGeocoder maps a backend name to its implementation. An unknown value is a
// config mistake — log it and fall back to Nominatim rather than crash.
func newGeocoder(name string) geocoder {
	switch name {
	case "photon":
		return photonGeocoder{}
	case "locationiq":
		return locationiqGeocoder{}
	case "", "nominatim":
		return nominatimGeocoder{}
	default:
		log.Printf("geo: unknown geocoder backend %q — falling back to nominatim (valid: nominatim, photon, locationiq)", name)
		return nominatimGeocoder{}
	}
}

// connBaseURL returns the tenant connection's base_url override (trailing slash
// trimmed) or the given fallback — the backend's env/public default. Lets an
// operator point a backend at a self-hosted instance per tenant.
func connBaseURL(job core.Job, fallback string) string {
	if u := strings.TrimRight(strings.TrimSpace(params.StringDefault(job.Params, "base_url", "")), "/"); u != "" {
		return u
	}
	return fallback
}

// connAPIKey returns the tenant connection's API key (a resolved secret), if any.
func connAPIKey(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "api_key", ""))
}

// acceptLanguage returns the optional `language` param, used both as a query
// hint (Photon) and the Accept-Language header (Nominatim).
func acceptLanguage(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "language", ""))
}

// countryCodes returns the optional comma-separated ISO country bias.
func countryCodes(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "countrycodes", ""))
}

// geoFetch runs one geocoder GET with the shared User-Agent (and an optional
// Accept-Language). Returns the HTTP status + raw body; the backend classifies.
// The dial is SSRF-guarded like every other connector.
func geoFetch(ctx context.Context, job core.Job, fullURL string) (int, []byte, error) {
	headers := map[string]string{"User-Agent": userAgent}
	if lang := acceptLanguage(job); lang != "" {
		headers["Accept-Language"] = lang
	}
	timeoutMS := params.TimeoutMS(job, 15000)
	status, body, _, err := hfnet.Do(ctx, "GET", fullURL, headers, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// geoHTTPFailure maps a transport error or non-2xx geocoder response to an
// error Result, returning nil on success — the shared epilogue of the geocoder
// backends. svc names the service; rateHint is appended to the 403/429 message
// so each backend can point at its own self-host knob.
func geoHTTPFailure(job core.Job, svc, rateHint string, status int, body []byte, err error) *core.Result {
	if r := geoloc.TransportFailure(job, "geocoder", svc, err); r != nil {
		return r
	}
	if status == 403 || status == 429 {
		r := params.Err(job, "rate_limited",
			svc+" declined the request (HTTP "+strconv.Itoa(status)+"). "+rateHint)
		return &r
	}
	if status < 200 || status >= 300 {
		r := params.Err(job, "geocoder_error", fmt.Sprintf("%s returned %d: %s", svc, status, params.Truncate(string(body), 200)))
		return &r
	}
	return nil
}
