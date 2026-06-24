package geo

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "geo_reverse",
			Version:     "1.0",
			Label:       "Reverse geocode",
			Subtitle:    "Point → place name",
			Summary:     "Pick a point on a map (or wire a coordinate) and get its place name.",
			Description: "The inverse of Location: name a point on the map. Drop a pin on the OpenStreetMap map right on the card — or wire a \"lat,lon\" Coordinate in from another step (a Location pick, a device's GPS) to override it — and it returns the human Place name (\"Stockholm, Södermanland, Sweden\") plus the structured Address. Handy for labelling an alert: 'Rain expected near <place>'. Uses OpenStreetMap — no account or key.",
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
					Title:  "From a wired coordinate",
					Params: json.RawMessage(`{"point":"40.7484,-73.9857"}`),
					Notes:  "The 'Coordinate' input (\"lat,lon\") overrides the map pin — wire a Location step into it.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
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
					"show_map":{"type":"boolean","default":true,"title":"Show map on card","description":"Show the interactive map on the node card. Turn off for a compact card — you can still pick in the inspector or drive it with the Coordinate input."},
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

	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("format", "jsonv2")

	status, body, gerr := nominatimGet(ctx, job, "/reverse", q)
	if f := httpFailure(job, status, body, gerr); f != nil {
		return *f, nil
	}

	var place nominatimPlace
	if uerr := json.Unmarshal(body, &place); uerr != nil {
		return params.ErrDetails(job, "nominatim_error", "OpenStreetMap returned an unexpected response.", uerr.Error()), nil
	}
	if place.DisplayName == "" {
		return params.Err(job, "no_match", "No place found at "+fmtCoord(lat, lon)), nil
	}

	addr := place.Address
	if len(addr) == 0 {
		addr = json.RawMessage(`{}`)
	}
	var raw any
	_ = json.Unmarshal(body, &raw)
	var addrAny any
	_ = json.Unmarshal(addr, &addrAny)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"place":      {MIME: "text/plain", Inline: place.DisplayName},
			"coordinate": {MIME: "text/plain", Inline: fmtCoord(lat, lon)},
			"address":    {MIME: "application/json", Inline: addrAny},
			"result":     {MIME: "application/json", Inline: raw},
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
		return parseLatLon(s)
	}
	point := strings.TrimSpace(params.StringDefault(job.Params, "point", ""))
	if point == "" {
		return 0, 0, errors.New(`pick a point on the map, or wire a "lat,lon" value into the Coordinate input`)
	}
	return parseLatLon(point)
}
