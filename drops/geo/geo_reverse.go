// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/geoloc"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "geo_reverse",
			Version:     "1.0",
			Label:       "Look up a place",
			Subtitle:    "Map coordinate → place",
			Summary:     "Pick a point on a map (or connect a coordinate) and get its place name.",
			Description: "The inverse of Location: name a point on the map. Drop a pin on the OpenStreetMap map right on the card — or connect a \"lat,lon\" Coordinate in from another step (a Location pick, a device's GPS) to override it — and it returns the human Place name (\"Stockholm, Södermanland, Sweden\") plus the structured Address. Handy for labelling an alert: 'Rain expected near <place>'. Uses OpenStreetMap — no account or key.",
			Integration: "OpenStreetMap",
			Category:    "network",
			Icon:        "map-pin",
			BrandLogo:   "/brands/openstreetmap.svg",
			Color:       "#7ebc6f",
			Provider:    "internal",
			Tags:        []string{"reverse geocode", "coordinate", "place", "address", "openstreetmap", "osm", "nominatim", "map", "pin", "lat", "lon", "location"},
			Examples: []core.ParamsExample{
				{
					Title:  "Name a point",
					Params: json.RawMessage(`{"point":"59.3293,18.0686"}`),
				},
				{
					Title:  "From a connected coordinate",
					Params: json.RawMessage(`{"point":"40.7484,-73.9857"}`),
					Notes:  "The 'Coordinate' input (\"lat,lon\") overrides the map pin — connect a Location step into it.",
				},
			},
			// Per-tenant geocoding backend (shared with Location); all optional.
			ConnectionFields: geoConnectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				// Coordinate (a "lat,lon") overrides the map pin when wired.
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "place", Label: "Place", MIME: []string{"text/plain"}},
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
				{Port: "address", Label: "Address", MIME: []string{"application/json"}},
				{Port: "result", Label: "Full result", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"point":{"type":"string","format":"geo-point","title":"Location","description":"The map pin as \"lat,lon\". Use the map to set it. Overridden by the Coordinate input."},
					"show_map":{"type":"boolean","default":true,"title":"Show map on card","description":"Show the interactive map on the step card. Turn off for a compact card — you can still pick in the inspector or drive it with the Coordinate input."},
					"language":{"type":"string","title":"Language","description":"Optional ISO language code for the returned place name (e.g. sv, de)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeReverse,
	})
}

// executeReverse resolves the point (the Coordinate input overrides the map
// pin), reverse-geocodes it, and emits the place name + structured address,
// plus the normalized coordinate for chaining.
func executeReverse(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := resolveCoord(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	place, errRes := geocoderFor(job).reverse(ctx, job, lat, lon)
	if errRes != nil {
		return *errRes, nil
	}

	addr := place.Address
	if addr == nil {
		addr = map[string]any{}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// Echo the queried coordinate (not the backend's snapped one) so
			// chaining stays faithful to what the user pointed at.
			"place":      {MIME: "text/plain", Inline: place.DisplayName},
			"coordinate": {MIME: "text/plain", Inline: geoloc.Fmt(lat, lon)},
			"address":    {MIME: "application/json", Inline: addr},
			"result":     {MIME: "application/json", Inline: place.Raw},
		},
	}, nil
}

// resolveCoord determines the point: the Coordinate input ("lat,lon") wins when
// wired with text, otherwise the map pin (the `point` param).
func resolveCoord(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return geoloc.Parse(s)
	}
	point := strings.TrimSpace(params.StringDefault(job.Params, "point", ""))
	if point == "" {
		return 0, 0, errors.New(`pick a point on the map, or wire a "lat,lon" value into the Coordinate input`)
	}
	return geoloc.Parse(point)
}
