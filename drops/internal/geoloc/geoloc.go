// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package geoloc holds the coordinate, unit and formatting helpers the
// location/weather connectors carried as copy-pasted boilerplate: parsing and
// range-checking a "lat,lon" string, resolving the Coordinate input against the
// Latitude/Longitude params, the display symbols for a units value, the two
// number formats a human summary uses, and the SSRF/transport prologue of every
// httpFailure epilogue.
//
// It lives under drops/internal/ so only sibling connector packages import it.
// The user-facing error strings are part of the contract — connector tests
// assert on them, so keep them verbatim.
//
// What stays per-connector is anything provider-shaped: which host to dial, how
// the provider spells its units query, which status codes get a bespoke
// message, and the weather-code tables (WMO vs Wsymb2).
package geoloc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func Parse(s string) (lat, lon float64, err error) {
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
	if err := CheckRange(lat, lon); err != nil {
		return 0, 0, err
	}
	return lat, lon, nil
}

func CheckRange(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", lat)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lon)
	}
	return nil
}

// Num reads a numeric param as float64, accepting the Go number types a
// decoded JSON document can carry. It deliberately does NOT parse numeric
// strings: a coordinate is a number, and refusing strings keeps a stray text
// value (e.g. a mis-wired param) from being mistaken for a valid lat/lon.
func Num(p map[string]any, key string) (float64, bool) {
	v, ok := p[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
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

// ResolveLatLon determines the target coordinate for a weather-style lookup:
// the Coordinate input ("lat,lon") wins when wired with text, otherwise the
// Latitude/Longitude params. Both paths are range-checked.
//
// Connectors whose coordinate comes from somewhere else (geo's map pin, say)
// resolve it themselves and call Parse.
func ResolveLatLon(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return Parse(s)
	}
	latOK, lonOK := false, false
	lat, latOK = Num(job.Params, "lat")
	lon, lonOK = Num(job.Params, "lon")
	if !latOK || !lonOK {
		return 0, 0, errors.New(`set Latitude and Longitude, or wire a "lat,lon" value into the Coordinate input`)
	}
	if err := CheckRange(lat, lon); err != nil {
		return 0, 0, err
	}
	return lat, lon, nil
}

func Fmt(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', -1, 64) + "," + strconv.FormatFloat(lon, 'f', -1, 64)
}

// TempUnit returns the display symbol for a units value, so a human-readable
// summary reads "12.3°C" rather than a bare number. "standard" (Kelvin) only
// exists on OpenWeather; connectors without it never pass it.
func TempUnit(units string) string {
	switch units {
	case "imperial":
		return "°F"
	case "standard":
		return "K"
	default:
		return "°C"
	}
}

func SpeedUnit(units string) string {
	if units == "imperial" {
		return "mph"
	}
	return "m/s"
}

func Num1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }

func Num0(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

func CapitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func TransportFailure(job core.Job, code, label string, err error) *core.Result {
	if err == nil {
		return nil
	}
	if hfnet.IsSSRFError(err) {
		r := params.ErrDetails(job, "egress_blocked",
			"Couldn't reach "+label+" — the request was blocked by the egress policy.", err.Error())
		return &r
	}
	r := params.Err(job, code+"_http_error", "Couldn't reach "+label+": "+err.Error())
	return &r
}

// probeBodyCap bounds what Probe buffers — a verification response is a few
// hundred bytes; 64 KiB is headroom that still refuses an unbounded body.
const probeBodyCap = 1 << 16

const probeTimeout = 10 * time.Second

// Probe performs the one-shot GET a connection verifier uses to check
// credentials, returning the status and a capped body. Egress policy is checked
// before dialing and the dial goes through the same SSRF-guarded client as
// every other connector, so a verifier can't reach an address a drop couldn't.
//
// label is the human service name used in the dial-failure message; an egress
// refusal is returned verbatim, since it already explains itself. A non-2xx
// status is NOT an error — what a 401 means differs per provider.
func Probe(ctx context.Context, label, url string) (status int, body []byte, err error) {
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("could not build request: %w", err)
	}
	resp, err := hfnet.SafeHTTPClient(probeTimeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach %s: %w", label, err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, probeBodyCap))
	return resp.StatusCode, body, nil
}
