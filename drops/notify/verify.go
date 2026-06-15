package notify

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

// ntfy connection verification, registered so the Apps page can test the
// server URL + access token before storing them. Email is intentionally not
// here: its SMTP settings are advanced node params, not a ConnectionFields
// connection, so it has no integration connection card to verify.
func init() {
	engine.RegisterConnectionVerifier("ntfy", verifyNtfy)
}

// verifyNtfy confirms the configured ntfy server is reachable and, when an
// access token is set, that the token is accepted. With a token it reads
// /v1/account (401 on a bad token); without one it hits the public
// /v1/health. Both are cheap GETs that send no notification.
func verifyNtfy(ctx context.Context, conn map[string]string) error {
	server := strings.TrimRight(strings.TrimSpace(conn["server"]), "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	token := strings.TrimSpace(conn["token"])

	path := "/v1/health"
	if token != "" {
		path = "/v1/account"
	}
	url := server + path
	if err := hfnet.EgressAllowed(url); err != nil {
		return err
	}

	timeout := 10 * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach ntfy server: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return errors.New("ntfy rejected the access token")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("ntfy server returned HTTP %d", resp.StatusCode)
	}
	return nil
}
