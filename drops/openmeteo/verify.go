// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package openmeteo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Open-Meteo connection verification, registered so the Apps page can test a
// commercial API key before storing it. The integration label must match the
// drops' Manifest.Integration ("Open-Meteo").
func init() {
	engine.RegisterConnectionVerifier("Open-Meteo", verifyOpenMeteo)
}

// verifyOpenMeteo confirms a configured key. The key is OPTIONAL — Open-Meteo's
// free non-commercial endpoint needs none — so an empty key verifies trivially
// (the drops will use the free host). A present key is checked against the
// commercial host at (0,0): a 401 means it's wrong. The dial goes through the
// same SSRF-guarded client as every other connector.
func verifyOpenMeteo(ctx context.Context, conn map[string]string) error {
	key := strings.TrimSpace(conn["api_key"])
	if key == "" {
		// No key is a valid configuration — the free endpoint is key-less.
		return nil
	}

	q := url.Values{}
	q.Set("latitude", "0")
	q.Set("longitude", "0")
	q.Set("current", "temperature_2m")
	q.Set("apikey", key)
	u := commercialURL + "?" + q.Encode()

	if err := hfnet.EgressAllowedFor(ctx, u); err != nil {
		return err
	}

	timeout := 10 * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", u, nil)
	if err != nil {
		return fmt.Errorf("could not build request: %w", err)
	}

	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Open-Meteo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == 401:
		if msg := extractOMError(body); msg != "" {
			return errors.New(msg)
		}
		return errors.New("Open-Meteo rejected the API key — check that it's correct (it's only needed for the commercial plan)")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("Open-Meteo returned HTTP %d: %s", resp.StatusCode, extractOMError(body))
	}
	return nil
}
