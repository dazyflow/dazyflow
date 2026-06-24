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
			Subtitle:    "Coordinate → place",
			Summary:     "Turn a coordinate into a place name via OpenStreetMap.",
			Description: "Name a point on the map. Give it a coordinate — type the Latitude and Longitude, or wire a \"lat,lon\" value in from another step (a Location pick, a Geocode, a device's GPS) — and it returns the human Place name (\"Stockholm, Södermanland, Sweden\") plus the structured Address. Handy for labelling an alert: 'Rain expected near <place>'. Uses OpenStreetMap's free Nominatim service — no account or key.",
			Integration: "OpenStreetMap",
			Category:    "network",
			Icon:        "map-pin",
			BrandLogo:   "/brands/openstreetmap.svg",
			Color:       "#7ebc6f",
			Provider:    "internal",
			Tags:        []string{"reverse geocode", "coordinate", "place", "address", "openstreetmap", "osm", "nominatim", "lat", "lon", "location"},
			Examples: []core.ParamsExample{
				{
					Title:  "Name a coordinate",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686}`),
				},
				{
					Title:  "From a wired coordinate",
					Params: json.RawMessage(`{"lat":40.7484,"lon":-73.9857}`),
					Notes:  "The 'Coordinate' input (\"lat,lon\") overrides the lat/lon params — wire a Location or Geocode step into it.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "place", Label: "Place", MIME: []string{"text/plain"}},
				{Port: "address", Label: "Address", MIME: []string{"application/json"}},
				{Port: "result", Label: "Full result", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude, -180..180. Overridden by the Coordinate input."},
					"language":{"type":"string","title":"Language","description":"Optional ISO language code for the returned place name (e.g. sv, de)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["lat","lon"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeReverse,
	})
}

// executeReverse resolves the coordinate (Coordinate input wins over lat/lon
// params), reverse-geocodes it, and emits the place name + structured address.
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
			"place":   {MIME: "text/plain", Inline: place.DisplayName},
			"address": {MIME: "application/json", Inline: addrAny},
			"result":  {MIME: "application/json", Inline: raw},
		},
	}, nil
}

// resolveCoord determines the coordinate: the Coordinate input ("lat,lon")
// wins when wired with text, otherwise the lat/lon params.
func resolveCoord(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return parseLatLon(s)
	}
	la, laOK := numParam(job.Params, "lat")
	lo, loOK := numParam(job.Params, "lon")
	if !laOK || !loOK {
		return 0, 0, errors.New(`set Latitude and Longitude, or wire a "lat,lon" value into the Coordinate input`)
	}
	return parseLatLon(fmtCoord(la, lo))
}

// numParam reads a numeric param as float64 (JSON numbers / Go ints), never a
// numeric string — so a stray text value isn't mistaken for a coordinate.
func numParam(p map[string]any, key string) (float64, bool) {
	switch n := p[key].(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}
