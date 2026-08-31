// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// --- verifyNtfy ---

// TestVerifyNtfy covers the health-check (no token), the account-check (token
// accepted vs rejected), and the generic non-2xx branch.
func TestVerifyNtfy(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	cases := []struct {
		name      string
		token     string
		status    int
		wantPath  string
		wantErr   bool
		errSubstr string
	}{
		{"health ok no token", "", 200, "/v1/health", false, ""},
		{"account ok with token", "tk_good", 200, "/v1/account", false, ""},
		{"token rejected 401", "tk_bad", 401, "/v1/account", true, "rejected the access token"},
		{"token rejected 403", "tk_bad", 403, "/v1/account", true, "rejected the access token"},
		{"server error 500", "", 500, "/v1/health", true, "HTTP 500"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(c.status)
			}))
			defer srv.Close()

			conn := map[string]string{"server": srv.URL}
			if c.token != "" {
				conn["token"] = c.token
			}
			err := verifyNtfy(context.Background(), conn)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), c.errSubstr) {
					t.Fatalf("err = %v, want contains %q", err, c.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("verifyNtfy: %v", err)
			}
			if gotPath != c.wantPath {
				t.Errorf("path = %q, want %q", gotPath, c.wantPath)
			}
			if c.token != "" && gotAuth != "Bearer "+c.token {
				t.Errorf("auth = %q", gotAuth)
			}
		})
	}
}

// TestVerifyNtfy_EgressBlocked covers the egress guard: a global allowlist that
// excludes the target host makes EgressAllowedFor reject before any request.
func TestVerifyNtfy_EgressBlocked(t *testing.T) {
	if err := hfnet.SetEgressAllowlist([]string{"allowed.example.com"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	defer func() { _ = hfnet.SetEgressAllowlist(nil) }()
	err := verifyNtfy(context.Background(), map[string]string{"server": "https://notallowed.example.com"})
	if err == nil || !strings.Contains(err.Error(), "egress_blocked") {
		t.Fatalf("err = %v, want egress_blocked", err)
	}
}

// --- verifyEmail ---

func TestVerifyEmail_ParamValidation(t *testing.T) {
	cases := []struct {
		name      string
		conn      map[string]string
		errSubstr string
	}{
		{"missing host", map[string]string{"from": "me@x.test"}, "enter your mail server"},
		{"missing from", map[string]string{"host": "smtp.x.test"}, "enter a From address"},
		{"bad port", map[string]string{"host": "smtp.x.test", "from": "me@x.test", "port": "abc"}, "port must be a number"},
		{"zero port", map[string]string{"host": "smtp.x.test", "from": "me@x.test", "port": "0"}, "port must be a number"},
		{"bad tls mode", map[string]string{"host": "smtp.x.test", "from": "me@x.test", "tls": "weird"}, "connection security must be"},
		// A From that isn't an address at all would otherwise only fail much
		// later, mid-send, as a raw SMTP rejection — the handshake never sends
		// a MAIL FROM, so this check is the only place that catches it.
		{"unparseable from", map[string]string{"host": "smtp.x.test", "from": "not an address"}, "doesn't look right"},
		{"from missing domain", map[string]string{"host": "smtp.x.test", "from": "reports"}, "doesn't look right"},
		{"two addresses in from", map[string]string{"host": "smtp.x.test", "from": "a@x.test, b@x.test"}, "doesn't look right"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verifyEmail(context.Background(), c.conn)
			if err == nil || !strings.Contains(err.Error(), c.errSubstr) {
				t.Fatalf("err = %v, want contains %q", err, c.errSubstr)
			}
		})
	}
}

// TestVerifyEmail_PrivateHostBlocked: a loopback host resolves but is private,
// so CheckDialHost rejects it and verifyEmail returns the private-address help.
func TestVerifyEmail_PrivateHostBlocked(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	err := verifyEmail(context.Background(), map[string]string{
		"host": "127.0.0.1", "from": "me@x.test", "tls": "none",
	})
	if err == nil || !strings.Contains(err.Error(), "local/private address") {
		t.Fatalf("err = %v, want private-address message", err)
	}
}

// TestVerifyEmail_UnresolvableHost: a host that does not resolve yields the
// "couldn't find a mail server" branch. Private egress must be OFF so
// CheckDialHost actually performs the lookup (it short-circuits when on).
func TestVerifyEmail_UnresolvableHost(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	err := verifyEmail(context.Background(), map[string]string{
		"host": "no-such-host.invalid", "from": "me@x.test", "tls": "none",
	})
	if err == nil || !strings.Contains(err.Error(), "couldn't find a mail server") {
		t.Fatalf("err = %v, want resolve-failure message", err)
	}
}

// TestVerifyEmail_HandshakeHappyPath drives the full SMTP verify dance against
// a scripted server (no AUTH), covering the success path including the QUIT.
func TestVerifyEmail_HandshakeHappyPath(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	host, port, _ := net.SplitHostPort(scriptedSMTP(t, nil))
	err := verifyEmail(context.Background(), map[string]string{
		"host": host, "port": port, "from": "me@x.test", "tls": "none",
	})
	if err != nil {
		t.Fatalf("verifyEmail: %v", err)
	}
}

// TestVerifyEmail_HandshakeWithAuth covers the username→PlainAuth branch in the
// verify path.
func TestVerifyEmail_HandshakeWithAuth(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	host, port, _ := net.SplitHostPort(scriptedSMTP(t, nil))
	err := verifyEmail(context.Background(), map[string]string{
		"host": host, "port": port, "from": "me@x.test", "tls": "none",
		"username": "me@x.test", "password": "pw",
	})
	if err != nil {
		t.Fatalf("verifyEmail with auth: %v", err)
	}
}

// TestVerifyEmail_DialError: a closed port reaches the smtputil.Verify error
// branch ("couldn't connect to the mail server").
func TestVerifyEmail_DialError(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	err := verifyEmail(context.Background(), map[string]string{
		"host": host, "port": port, "from": "me@x.test", "tls": "none",
	})
	if err == nil || !strings.Contains(err.Error(), "couldn't connect to the mail server") {
		t.Fatalf("err = %v, want connect-failure message", err)
	}
}

// TestVerifyEmail_AcceptsDisplayNameFrom: the From address may carry a display
// name, so verification must get past the From check on that form and go on to
// dial (here a loopback host, which the egress guard then refuses — proof the
// address itself was accepted).
func TestVerifyEmail_AcceptsDisplayNameFrom(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	for _, from := range []string{
		"Reports <reports@x.test>",
		`"Klahr, Joachim" <j@x.test>`,
		"<reports@x.test>",
	} {
		err := verifyEmail(context.Background(), map[string]string{
			"host": "127.0.0.1", "from": from, "tls": "none",
		})
		if err == nil || !strings.Contains(err.Error(), "local/private address") {
			t.Errorf("from %q: err = %v, want to reach the dial guard", from, err)
		}
	}
}
