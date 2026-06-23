package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/smtputil"
)

// Connection verification for this package's integrations, registered so the
// Apps page can test credentials before storing them: ntfy (server + token)
// and Email (SMTP server + login). Each label matches its drop's
// Manifest.Integration.
func init() {
	engine.RegisterConnectionVerifier("ntfy", verifyNtfy)
	engine.RegisterConnectionVerifier("Email", verifyEmail)
}

// verifyEmail dials the configured mail server and runs the SMTP handshake —
// STARTTLS/implicit TLS as configured, plus AUTH when a username is set — then
// QUITs without sending anything (smtputil.Verify). It surfaces the common
// failures in plain language: a bad port/security value, an unreachable or
// private host (the operator must opt into private egress), or rejected
// credentials.
func verifyEmail(ctx context.Context, conn map[string]string) error {
	host := strings.TrimSpace(conn["host"])
	if host == "" {
		return errors.New("enter your mail server")
	}
	if strings.TrimSpace(conn["from"]) == "" {
		return errors.New("enter a From address")
	}

	port := 587
	if s := strings.TrimSpace(conn["port"]); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return errors.New("port must be a number, e.g. 587 or 465")
		}
		port = n
	}

	mode := strings.TrimSpace(conn["tls"])
	if mode == "" {
		mode = "starttls"
	}
	switch mode {
	case "starttls", "implicit", "none":
	default:
		return errors.New(`connection security must be "starttls", "implicit", or "none"`)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if err := hfnet.CheckDialHost(addr); err != nil {
		// CheckDialHost fails for two very different reasons: the host doesn't
		// resolve at all (a typo'd or wrong server name) vs. it resolves to a
		// private/LAN address (the egress guard). Don't tell someone with a typo
		// to enable private-network access — say the address looks wrong.
		if strings.Contains(err.Error(), "cannot resolve") {
			return fmt.Errorf("couldn't find a mail server at %q — check the address", host)
		}
		return errors.New("that looks like a local/private address — the operator must enable private-network access (DAZYFLOW_ALLOW_PRIVATE_EGRESS) to reach it")
	}

	var auth smtp.Auth
	if u := strings.TrimSpace(conn["username"]); u != "" {
		auth = smtp.PlainAuth("", u, conn["password"], host)
	}

	if err := smtputil.Verify(ctx, addr, host, mode, auth); err != nil {
		return fmt.Errorf("couldn't connect to the mail server: %w", err)
	}
	return nil
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
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
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
