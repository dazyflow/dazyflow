// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package weather

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/engine"
)

// OpenWeather connection verification, registered so the Apps page can test
// the API key before storing it. The integration label must match the drops'
// Manifest.Integration ("OpenWeather").
func init() {
	engine.RegisterConnectionVerifier("OpenWeather", verifyOpenWeather)
}

func verifyOpenWeather(ctx context.Context, conn map[string]string) error {
	key := strings.TrimSpace(conn["api_key"])
	if key == "" {
		return errors.New("enter your OpenWeather API key")
	}

	q := url.Values{}
	q.Set("lat", "0")
	q.Set("lon", "0")
	q.Set("appid", key)

	status, body, err := geoloc.Probe(ctx, "OpenWeather", currentURL+"?"+q.Encode())
	if err != nil {
		return err
	}

	switch {
	case status == 401:
		if msg := extractOWMError(body); msg != "" {
			return errors.New(msg)
		}
		return errors.New("OpenWeather rejected the API key — check that it's correct and active (a newly created key can take a couple of hours to start working)")
	case status < 200 || status >= 300:
		return fmt.Errorf("OpenWeather returned HTTP %d: %s", status, extractOWMError(body))
	}
	return nil
}
