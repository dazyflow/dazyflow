// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// nominatimURL is OpenStreetMap's geocoder. A var (not const) so tests can
// point it at a local httptest server; DAZYFLOW_NOMINATIM_URL overrides it for
// operators who self-host the deployment default (the public instance is
// rate-limited). A tenant can also override per-connection via base_url.
var nominatimURL = func() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("DAZYFLOW_NOMINATIM_URL")), "/"); u != "" {
		return u
	}
	return "https://nominatim.openstreetmap.org"
}()

// nominatimRateHint guides an operator who hits the public instance's limit.
const nominatimRateHint = "Its free service is rate-limited (~1 request/second) and forbids bulk use; for heavier load self-host Nominatim (set base_url or DAZYFLOW_NOMINATIM_URL) or switch the backend to LocationIQ/Photon."

// nominatimGeocoder talks to OpenStreetMap's Nominatim API (or a self-hosted
// one). It's the default backend and the one the base tests stub.
type nominatimGeocoder struct{}

func (nominatimGeocoder) label() string { return "OpenStreetMap" }

func (g nominatimGeocoder) forward(ctx context.Context, job core.Job, query string) (geoPlace, *core.Result) {
	return nominatimForward(ctx, job, query, g.label(), nominatimRateHint, connBaseURL(job, nominatimURL), nil)
}

func (g nominatimGeocoder) reverse(ctx context.Context, job core.Job, lat, lon float64) (geoPlace, *core.Result) {
	return nominatimReverse(ctx, job, lat, lon, g.label(), nominatimRateHint, connBaseURL(job, nominatimURL), nil)
}

// nominatimForward runs a Nominatim-API /search against any base URL. svc /
// rateHint name the service for errors; extra carries backend-specific query
// params (e.g. LocationIQ's key). Shared by Nominatim and LocationIQ — both
// speak the same JSON.
func nominatimForward(ctx context.Context, job core.Job, query, svc, rateHint, base string, extra url.Values) (geoPlace, *core.Result) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	if cc := countryCodes(job); cc != "" {
		q.Set("countrycodes", cc)
	}
	mergeValues(q, extra)
	status, body, err := geoFetch(ctx, job, base+"/search?"+q.Encode())
	if f := geoHTTPFailure(job, svc, rateHint, status, body, err); f != nil {
		return geoPlace{}, f
	}
	var hits []nominatimPlace
	if uerr := json.Unmarshal(body, &hits); uerr != nil {
		r := params.ErrDetails(job, "geocoder_error", svc+" returned an unexpected response.", uerr.Error())
		return geoPlace{}, &r
	}
	if len(hits) == 0 {
		r := params.Err(job, "no_match", "No place found for "+query)
		return geoPlace{}, &r
	}
	return hits[0].toGeoPlace(job, svc, hits[0].raw())
}

// nominatimReverse runs a Nominatim-API /reverse against any base URL.
func nominatimReverse(ctx context.Context, job core.Job, lat, lon float64, svc, rateHint, base string, extra url.Values) (geoPlace, *core.Result) {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("format", "jsonv2")
	mergeValues(q, extra)
	status, body, err := geoFetch(ctx, job, base+"/reverse?"+q.Encode())
	if f := geoHTTPFailure(job, svc, rateHint, status, body, err); f != nil {
		return geoPlace{}, f
	}
	var place nominatimPlace
	if uerr := json.Unmarshal(body, &place); uerr != nil {
		r := params.ErrDetails(job, "geocoder_error", svc+" returned an unexpected response.", uerr.Error())
		return geoPlace{}, &r
	}
	if place.DisplayName == "" {
		r := params.Err(job, "no_match", "No place found at "+fmtCoord(lat, lon))
		return geoPlace{}, &r
	}
	return place.toGeoPlace(job, svc, body)
}

// mergeValues copies src params into dst (overwriting).
func mergeValues(dst, src url.Values) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Set(k, v)
		}
	}
}

// nominatimPlace is the slice of a Nominatim/LocationIQ result the drops
// surface. Both return lat/lon as STRINGS; we reparse them into "lat,lon".
type nominatimPlace struct {
	Lat         string          `json:"lat"`
	Lon         string          `json:"lon"`
	DisplayName string          `json:"display_name"`
	Address     json.RawMessage `json:"address"`
}

// raw re-encodes a single hit so the forward path carries a "result" payload
// shaped like the reverse path's (a single place object, not the search array).
func (p nominatimPlace) raw() []byte {
	b, _ := json.Marshal(p)
	return b
}

// toGeoPlace normalizes a Nominatim-compatible place into geoPlace, reparsing
// the string lat/lon and decoding the address + raw body. rawBody is the bytes
// surfaced on the "result" pin; svc names the backend for error messages.
func (p nominatimPlace) toGeoPlace(job core.Job, svc string, rawBody []byte) (geoPlace, *core.Result) {
	lat, err := strconv.ParseFloat(strings.TrimSpace(p.Lat), 64)
	if err != nil {
		r := params.ErrDetails(job, "geocoder_error", svc+" returned a malformed coordinate.", "latitude "+strconv.Quote(p.Lat))
		return geoPlace{}, &r
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(p.Lon), 64)
	if err != nil {
		r := params.ErrDetails(job, "geocoder_error", svc+" returned a malformed coordinate.", "longitude "+strconv.Quote(p.Lon))
		return geoPlace{}, &r
	}
	var addr any
	if len(p.Address) > 0 {
		_ = json.Unmarshal(p.Address, &addr)
	}
	var raw any
	_ = json.Unmarshal(rawBody, &raw)
	return geoPlace{
		Lat:         lat,
		Lon:         lon,
		Coord:       fmtCoord(lat, lon),
		DisplayName: p.DisplayName,
		Address:     addr,
		Raw:         raw,
	}, nil
}
