// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// photonURL is Komoot's Photon geocoder. A var so tests can point it at a local
// httptest server; DAZYFLOW_PHOTON_URL overrides it for operators who self-host
// (the public instance is fair-use only).
var photonURL = func() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("DAZYFLOW_PHOTON_URL")), "/"); u != "" {
		return u
	}
	return "https://photon.komoot.io"
}()

const photonRateHint = "Photon's public instance is fair-use only; for heavier load self-host Photon and set DAZYFLOW_PHOTON_URL."

// photonGeocoder talks to a Photon instance. Photon is OpenStreetMap data
// behind an Elasticsearch index — keyless, autocomplete-friendly — but speaks
// GeoJSON (a FeatureCollection of Point features with flat address properties)
// rather than Nominatim's JSON, so it needs its own mapping onto geoPlace.
//
// Photon has no Nominatim-style "display_name" or "countrycodes"; we compose a
// label from the address properties and ignore the country-bias param (Photon
// biases by bbox/location instead, which these drops don't expose).
type photonGeocoder struct{}

func (photonGeocoder) label() string { return "Photon" }

func (g photonGeocoder) fail(job core.Job, status int, body []byte, err error) *core.Result {
	return geoHTTPFailure(job, g.label(), photonRateHint, status, body, err)
}

func (g photonGeocoder) forward(ctx context.Context, job core.Job, query string) (geoPlace, *core.Result) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("limit", "1")
	if lang := photonLang(job); lang != "" {
		q.Set("lang", lang)
	}
	status, body, err := geoFetch(ctx, job, photonURL+"/api?"+q.Encode())
	if f := g.fail(job, status, body, err); f != nil {
		return geoPlace{}, f
	}
	return g.firstFeature(job, body, "No place found for "+query)
}

func (g photonGeocoder) reverse(ctx context.Context, job core.Job, lat, lon float64) (geoPlace, *core.Result) {
	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', -1, 64))
	if lang := photonLang(job); lang != "" {
		q.Set("lang", lang)
	}
	status, body, err := geoFetch(ctx, job, photonURL+"/reverse?"+q.Encode())
	if f := g.fail(job, status, body, err); f != nil {
		return geoPlace{}, f
	}
	return g.firstFeature(job, body, "No place found at "+geoloc.Fmt(lat, lon))
}

// firstFeature decodes a Photon FeatureCollection and maps its first feature
// onto geoPlace. noMatch is the message when the collection is empty.
func (g photonGeocoder) firstFeature(job core.Job, body []byte, noMatch string) (geoPlace, *core.Result) {
	var doc photonResponse
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		r := params.ErrDetails(job, "geocoder_error", "Photon returned an unexpected response.", uerr.Error())
		return geoPlace{}, &r
	}
	if len(doc.Features) == 0 {
		r := params.Err(job, "no_match", noMatch)
		return geoPlace{}, &r
	}
	f := doc.Features[0]
	// GeoJSON orders coordinates [lon, lat] — the opposite of "lat,lon".
	if len(f.Geometry.Coordinates) < 2 {
		r := params.ErrDetails(job, "geocoder_error", "Photon returned a feature with no coordinate.", "geometry.coordinates")
		return geoPlace{}, &r
	}
	lon, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		r := params.ErrDetails(job, "geocoder_error", "Photon returned an out-of-range coordinate.", geoloc.Fmt(lat, lon))
		return geoPlace{}, &r
	}
	var raw any
	_ = json.Unmarshal(body, &raw)
	return geoPlace{
		Lat:         lat,
		Lon:         lon,
		Coord:       geoloc.Fmt(lat, lon),
		DisplayName: photonDisplayName(f.Properties),
		Address:     f.Properties, // flat OSM address fields: name, city, state, country, …
		Raw:         raw,
	}, nil
}

// photonResponse is the GeoJSON FeatureCollection Photon returns.
type photonResponse struct {
	Features []photonFeature `json:"features"`
}

type photonFeature struct {
	Geometry struct {
		Coordinates []float64 `json:"coordinates"` // [lon, lat]
	} `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

// photonLang passes the optional language through as Photon's `lang` query
// param. Photon only understands a handful (en, de, fr, it, default) and falls
// back to default for anything else, so an unsupported code is harmless.
func photonLang(job core.Job) string { return acceptLanguage(job) }

// photonDisplayName composes a human label from Photon's flat address
// properties (it has no Nominatim-style display_name). It walks the most
// salient fields outermost-last and skips empties and consecutive repeats, so
// "Stockholm / Stockholm / Sweden" collapses to "Stockholm, Sweden".
func photonDisplayName(props map[string]any) string {
	street := pstr(props, "street")
	if hn := pstr(props, "housenumber"); hn != "" && street != "" {
		street += " " + hn
	}
	var out []string
	for _, field := range []string{
		pstr(props, "name"),
		street,
		pstr(props, "city"),
		pstr(props, "state"),
		pstr(props, "country"),
	} {
		if field == "" {
			continue
		}
		if len(out) > 0 && strings.EqualFold(out[len(out)-1], field) {
			continue
		}
		out = append(out, field)
	}
	return strings.Join(out, ", ")
}

// pstr reads a string property, tolerating absent/non-string values.
func pstr(props map[string]any, key string) string {
	if v, ok := props[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
