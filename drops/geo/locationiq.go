// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"net/url"
	"os"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// locationiqURL is LocationIQ's geocoding base. LocationIQ mirrors the
// Nominatim API (/search, /reverse, format=jsonv2) but authenticates with a
// `key` query param and is hosted (no infrastructure to run). A var so tests
// can stub it; DAZYFLOW_LOCATIONIQ_URL overrides the deployment default, and a
// tenant can override per-connection via base_url.
var locationiqURL = func() string {
	if u := strings.TrimRight(strings.TrimSpace(os.Getenv("DAZYFLOW_LOCATIONIQ_URL")), "/"); u != "" {
		return u
	}
	return "https://us1.locationiq.com/v1"
}()

const locationiqRateHint = "Check your LocationIQ plan's rate limit, or self-host (set base_url) / switch the backend to Photon (no key)."

// locationiqGeocoder talks to LocationIQ. It reuses the Nominatim-compatible
// request/parse path, adding the required API key as a query param.
type locationiqGeocoder struct{}

func (locationiqGeocoder) label() string { return "LocationIQ" }

func (g locationiqGeocoder) forward(ctx context.Context, job core.Job, query string) (geoPlace, *core.Result) {
	key, errRes := g.keyParam(job)
	if errRes != nil {
		return geoPlace{}, errRes
	}
	return nominatimForward(ctx, job, query, g.label(), locationiqRateHint, connBaseURL(job, locationiqURL), key)
}

func (g locationiqGeocoder) reverse(ctx context.Context, job core.Job, lat, lon float64) (geoPlace, *core.Result) {
	key, errRes := g.keyParam(job)
	if errRes != nil {
		return geoPlace{}, errRes
	}
	return nominatimReverse(ctx, job, lat, lon, g.label(), locationiqRateHint, connBaseURL(job, locationiqURL), key)
}

// keyParam builds the LocationIQ `key` query param from the connection's
// api_key, or a pointed not_connected error when it's missing — selecting
// LocationIQ without a key is the one setup mistake worth catching early.
func (locationiqGeocoder) keyParam(job core.Job) (url.Values, *core.Result) {
	key := connAPIKey(job)
	if key == "" {
		r := params.Err(job, "not_connected",
			"LocationIQ needs an API key — add it on the OpenStreetMap integration page, or switch the backend to Nominatim/Photon (which need no key).")
		return nil, &r
	}
	v := url.Values{}
	v.Set("key", key)
	return v, nil
}
