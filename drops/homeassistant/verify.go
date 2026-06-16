package homeassistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// Home Assistant connection verification, registered so the Apps page can
// test the instance URL + access token before storing them. The integration
// label must match the drops' Manifest.Integration ("Home Assistant").
func init() {
	engine.RegisterConnectionVerifier("Home Assistant", verifyHomeAssistant)
}

// verifyHomeAssistant confirms the configured instance is reachable and the
// token is accepted, by hitting GET /api/ — Home Assistant's cheap liveness
// endpoint ({"message":"API running."}), which requires a valid token and
// has no side effects. A 401 means the token is wrong/expired. Like every
// other dial here it goes through the SSRF guard, so a LAN instance needs the
// operator's private-egress opt-in; the error makes that explicit.
func verifyHomeAssistant(ctx context.Context, conn map[string]string) error {
	base := strings.TrimRight(strings.TrimSpace(conn["base_url"]), "/")
	token := strings.TrimSpace(conn["token"])
	if base == "" {
		return errors.New("enter your Home Assistant instance URL")
	}
	if token == "" {
		return errors.New("enter a long-lived access token")
	}

	url := base + "/api/"
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return err
	}

	timeout := 10 * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("invalid instance URL: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		if hfnet.IsSSRFError(err) {
			return errors.New("that looks like a local/private address — the operator must enable private-network access (HAZYFLOW_ALLOW_PRIVATE_EGRESS) for hazyflow to reach a LAN instance")
		}
		return fmt.Errorf("could not reach Home Assistant: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return errors.New("Home Assistant rejected the access token")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("Home Assistant returned HTTP %d", resp.StatusCode)
	}
	return nil
}
