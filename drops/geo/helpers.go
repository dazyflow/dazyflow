// Package geo hosts the OpenStreetMap connector: pick a location on a map and
// emit its coordinate (a design-time value source), turn a place name into a
// coordinate (forward geocode), and turn a coordinate back into a place name
// (reverse geocode).
//
// The map-picker drop (geo_location) is a pure value source — its coordinate
// is chosen in the editor via a Leaflet/OpenStreetMap widget (format
// "geo-point") and parsed at run time, no network. The geocode / geo_reverse
// drops call OpenStreetMap's Nominatim service at run time through the shared
// SSRF-guarded client.
//
// Nominatim's usage policy requires an identifying User-Agent and asks for
// no more than ~1 request/second; the endpoint is configurable
// (DAZYFLOW_NOMINATIM_URL) so an operator can point at a self-hosted instance
// for heavier use. Output coordinates are the "lat,lon" string the OpenWeather
// drops' Coordinate input accepts, so a geocode/picker wires straight into a
// weather lookup.
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// nominatimURL is OpenStreetMap's geocoder. A var (not const) so tests can
// point it at a local httptest server; DAZYFLOW_NOMINATIM_URL overrides it for
// operators who self-host (Nominatim's public instance is rate-limited).
var nominatimURL = func() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("DAZYFLOW_NOMINATIM_URL")), "/"); u != "" {
		return u
	}
	return "https://nominatim.openstreetmap.org"
}()

// userAgent identifies dazyflow to Nominatim — its policy rejects requests
// without a real User-Agent (HTTP 403), so this is mandatory, not cosmetic.
const userAgent = "dazyflow (+https://git.sr.ht/~klahr/dazyflow)"

const maxResponseBytes = 2 << 20 // 2 MiB — a geocode hit is a few KiB.

// parseLatLon splits a "lat,lon" string into two range-checked floats. Shared
// by the picker (parsing its stored point) and reverse geocode (parsing the
// Coordinate input).
func parseLatLon(s string) (lat, lon float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("coordinate %q must be \"lat,lon\" — e.g. 59.33,18.07", s)
	}
	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("latitude %q isn't a number", strings.TrimSpace(parts[0]))
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("longitude %q isn't a number", strings.TrimSpace(parts[1]))
	}
	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lon)
	}
	return lat, lon, nil
}

// fmtCoord renders a "lat,lon" string, trimming trailing zeros so a tidy point
// stays tidy ("59.3293,18.0686", not "59.32930000,18.06860000").
func fmtCoord(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', -1, 64) + "," + strconv.FormatFloat(lon, 'f', -1, 64)
}

// acceptLanguage returns the Accept-Language header value from the optional
// `language` param, so place names can come back localized.
func acceptLanguage(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "language", ""))
}

// nominatimGet runs one Nominatim request with the required User-Agent (and an
// optional Accept-Language). Returns the HTTP status + raw body; the caller
// classifies. The dial is SSRF-guarded like every other connector.
func nominatimGet(ctx context.Context, job core.Job, path string, q url.Values) (int, []byte, error) {
	headers := map[string]string{"User-Agent": userAgent}
	if lang := acceptLanguage(job); lang != "" {
		headers["Accept-Language"] = lang
	}
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	full := nominatimURL + path + "?" + q.Encode()
	status, body, _, err := hfnet.Do(ctx, "GET", full, headers, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// httpFailure maps a transport error or non-2xx Nominatim response to an error
// Result, returning nil on success — the shared epilogue of the network drops.
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach OpenStreetMap — the request was blocked by the egress policy.", err.Error())
			return &r
		}
		r := params.Err(job, "nominatim_http_error", "Couldn't reach OpenStreetMap: "+err.Error())
		return &r
	}
	if status == 403 || status == 429 {
		r := params.Err(job, "rate_limited",
			"OpenStreetMap's geocoder declined the request (HTTP "+strconv.Itoa(status)+"). Its free service is rate-limited (~1 request/second); for heavier use, self-host Nominatim and set DAZYFLOW_NOMINATIM_URL.")
		return &r
	}
	if status < 200 || status >= 300 {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200]
		}
		r := params.Err(job, "nominatim_error", fmt.Sprintf("OpenStreetMap returned %d: %s", status, s))
		return &r
	}
	return nil
}

// nominatimPlace is the slice of a Nominatim result the drops surface. Nominatim
// returns lat/lon as STRINGS; we reparse them into the canonical "lat,lon".
type nominatimPlace struct {
	Lat         string          `json:"lat"`
	Lon         string          `json:"lon"`
	DisplayName string          `json:"display_name"`
	Address     json.RawMessage `json:"address"`
}

// coord turns the string lat/lon Nominatim returns into floats + the canonical
// "lat,lon" string. A malformed pair (shouldn't happen) is reported.
func (p nominatimPlace) coord() (lat, lon float64, coord string, err error) {
	lat, err = strconv.ParseFloat(strings.TrimSpace(p.Lat), 64)
	if err != nil {
		return 0, 0, "", fmt.Errorf("geocoder returned a non-numeric latitude %q", p.Lat)
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(p.Lon), 64)
	if err != nil {
		return 0, 0, "", fmt.Errorf("geocoder returned a non-numeric longitude %q", p.Lon)
	}
	return lat, lon, fmtCoord(lat, lon), nil
}

// geocodeFirst forward-geocodes `query` and returns the best match. On any
// failure (transport, non-2xx, malformed body, no match) it returns an error
// Result the caller can return directly; otherwise the place and a nil Result.
// Shared by the Geocode drop and the Location drop's Place override.
func geocodeFirst(ctx context.Context, job core.Job, query string) (nominatimPlace, *core.Result) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "jsonv2")
	q.Set("limit", "1")
	if cc := strings.TrimSpace(params.StringDefault(job.Params, "countrycodes", "")); cc != "" {
		q.Set("countrycodes", cc)
	}
	status, body, err := nominatimGet(ctx, job, "/search", q)
	if f := httpFailure(job, status, body, err); f != nil {
		return nominatimPlace{}, f
	}
	var hits []nominatimPlace
	if uerr := json.Unmarshal(body, &hits); uerr != nil {
		r := params.ErrDetails(job, "nominatim_error", "OpenStreetMap returned an unexpected response.", uerr.Error())
		return nominatimPlace{}, &r
	}
	if len(hits) == 0 {
		r := params.Err(job, "no_match", "No place found for "+query)
		return nominatimPlace{}, &r
	}
	return hits[0], nil
}
