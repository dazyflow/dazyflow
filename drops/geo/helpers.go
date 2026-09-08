// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package geo hosts the OpenStreetMap connector: pick a location on a map and
// emit its coordinate (geo_location, a pure value source parsed at run time
// with no network), turn a place name into a coordinate, and turn a coordinate
// back into a place name. The geocoding drops go through a pluggable backend
// chosen per tenant via the OpenStreetMap connection (`backend`, `base_url`,
// `api_key`), with DAZYFLOW_GEOCODER as the deployment default and Nominatim
// as the final fallback (see geocoderFor):
//
//   - nominatim: OpenStreetMap's reference API, no key. The public instance is
//     limited to ~1 req/s and forbids bulk use; self-host for real load
//     (base_url or DAZYFLOW_NOMINATIM_URL).
//   - photon: Komoot's GeoJSON API, no key, good typo tolerance. Public
//     instance is fair-use; self-host via base_url or DAZYFLOW_PHOTON_URL.
//   - locationiq: hosted Nominatim-compatible API, REQUIRES an api_key.
//
// Every backend normalizes to geoPlace. Output coordinates are the "lat,lon"
// string the weather drops accept, so a geocode wires straight into a lookup.
// All dials go through the shared SSRF-guarded client.
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

const userAgent = "dazyflow (+https://github.com/dazyflow/dazyflow)"

const maxResponseBytes = 2 << 20 // 2 MiB — a geocode hit is a few KiB.

type geoPlace struct {
	Lat, Lon    float64 // parsed coordinate
	Coord       string  // canonical "lat,lon" (trimmed), ready to wire onward
	DisplayName string  // human-readable place name
	Address     any     // structured address object (decoded JSON), or nil
	Raw         any     // the backend's full response (decoded JSON), for "result"
}

type geocoder interface {
	label() string
	forward(ctx context.Context, job core.Job, query string) (geoPlace, *core.Result)
	reverse(ctx context.Context, job core.Job, lat, lon float64) (geoPlace, *core.Result)
}

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

func acceptLanguage(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "language", ""))
}

func countryCodes(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "countrycodes", ""))
}

func geoFetch(ctx context.Context, job core.Job, fullURL string) (int, []byte, error) {
	headers := map[string]string{"User-Agent": userAgent}
	if lang := acceptLanguage(job); lang != "" {
		headers["Accept-Language"] = lang
	}
	timeoutMS := params.TimeoutMS(job, 15000)
	status, body, _, err := hfnet.Do(ctx, "GET", fullURL, headers, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

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
