package geo

import (
	"context"
	"encoding/json"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "geocode",
			Version:     "1.0",
			Label:       "Geocode",
			Subtitle:    "Place → coordinate",
			Summary:     "Turn a place name or address into a coordinate via OpenStreetMap.",
			Description: "Look up where a place is. Give it an address or place name — typed on the step or wired in from another step (a form field, a message) — and it returns the best match's Coordinate (\"lat,lon\"), the full Place name, and the raw result. Wire the Coordinate into a Weather step to, say, report the forecast for whatever city someone typed into a form. Uses OpenStreetMap's free Nominatim geocoder — no account or key.",
			Integration: "OpenStreetMap",
			Category:    "network",
			Icon:        "map-pin",
			BrandLogo:   "/brands/openstreetmap.svg",
			Color:       "#7ebc6f",
			Provider:    "internal",
			Tags:        []string{"geocode", "geocoding", "address", "place", "coordinate", "openstreetmap", "osm", "nominatim", "search", "location"},
			Examples: []core.ParamsExample{
				{
					Title:  "City name",
					Params: json.RawMessage(`{"query":"Stockholm, Sweden"}`),
				},
				{
					Title:  "Street address, biased to a country",
					Params: json.RawMessage(`{"query":"1600 Pennsylvania Ave NW, Washington","countrycodes":"us"}`),
					Notes:  "Coordinate output is \"lat,lon\" — feed it into a Weather step. The 'Address' input overrides the query param when wired.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "address", Label: "Address", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
				{Port: "lat", Label: "Latitude", MIME: []string{"text/plain"}},
				{Port: "lon", Label: "Longitude", MIME: []string{"text/plain"}},
				{Port: "place", Label: "Place", MIME: []string{"text/plain"}},
				{Port: "result", Label: "Full result", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query":{"type":"string","title":"Address or place","description":"What to look up — a place name or street address. Overridden by the 'Address' input."},
					"countrycodes":{"type":"string","title":"Country bias","description":"Optional comma-separated ISO country codes to bias/limit results (e.g. \"us\" or \"se,no\")."},
					"language":{"type":"string","title":"Language","description":"Optional ISO language code for the returned place name (e.g. sv, de)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["query"]
			}`),
			// A lookup with no side effects — safe to retry on a blip.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGeocode,
	})
}

// executeGeocode resolves the address (Address input wins over the query param)
// to the best Nominatim match and emits its coordinate, place name, and the raw
// result. Zero matches is a pointed error, not an empty success.
func executeGeocode(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	query, ok := params.TextInputOr(job, "address", params.StringDefault(job.Params, "query", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Address' input must be text"), nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return params.Err(job, "bad_param", "set an address/place to look up (or wire the 'Address' input)"), nil
	}

	hit, errRes := geocodeFirst(ctx, job, query)
	if errRes != nil {
		return *errRes, nil
	}
	_, _, coord, cerr := hit.coord()
	if cerr != nil {
		return params.ErrDetails(job, "nominatim_error", "OpenStreetMap returned a malformed coordinate.", cerr.Error()), nil
	}

	parts := strings.SplitN(coord, ",", 2)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"coordinate": {MIME: "text/plain", Inline: coord},
			"lat":        {MIME: "text/plain", Inline: parts[0]},
			"lon":        {MIME: "text/plain", Inline: parts[1]},
			"place":      {MIME: "text/plain", Inline: hit.DisplayName},
			"result":     {MIME: "application/json", Inline: hit},
		},
	}, nil
}
