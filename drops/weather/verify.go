package weather

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

// OpenWeather connection verification, registered so the Apps page can test
// the API key before storing it. The integration label must match the drops'
// Manifest.Integration ("OpenWeather").
func init() {
	engine.RegisterConnectionVerifier("OpenWeather", verifyOpenWeather)
}

// verifyOpenWeather confirms the API key is accepted by making a tiny call to
// the FREE Current Weather endpoint at (0,0) — the same endpoint weather_current
// uses, so a key that passes here works at run time without any paid
// subscription. A 401 means the key is wrong or not yet activated; the dial
// goes through the same SSRF-guarded client as every other connector.
func verifyOpenWeather(ctx context.Context, conn map[string]string) error {
	key := strings.TrimSpace(conn["api_key"])
	if key == "" {
		return errors.New("enter your OpenWeather API key")
	}

	q := url.Values{}
	q.Set("lat", "0")
	q.Set("lon", "0")
	q.Set("appid", key)
	u := currentURL + "?" + q.Encode()

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
		return fmt.Errorf("could not reach OpenWeather: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == 401:
		// Surface OpenWeather's own 401 message verbatim — it reaches the Apps
		// page unchanged so the user sees the real reason.
		if msg := extractOWMError(body); msg != "" {
			return errors.New(msg)
		}
		return errors.New("OpenWeather rejected the API key — check that it's correct and active (a newly created key can take a couple of hours to start working)")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("OpenWeather returned HTTP %d: %s", resp.StatusCode, extractOWMError(body))
	}
	return nil
}
