// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	_ "git.sr.ht/~klahr/dazyflow/drops/notify" // register the email_send manifest so its connection fields resolve
)

// storeEmailConn writes the tenant's Email SMTP connection fields the way the
// Apps page would, so send-test has a real connection to dial.
func storeEmailConn(t *testing.T, h *gatewayHarness, fields map[string]string) {
	t.Helper()
	for k, v := range fields {
		if err := h.gw.EncryptedSecrets.PutScoped(t.Context(), "t", "", ScopeTenant, core.ConnectionSecretKey("Email", k), v); err != nil {
			t.Fatalf("store conn %s: %v", k, err)
		}
	}
}

// A bad recipient is rejected as client input (400) before any connection
// load or dial.
func TestSendTestEmail_InvalidRecipient(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "POST", "/api/v1/email-templates/send-test", map[string]any{"to": "not-an-address"})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for junk recipient; got %d body=%s", rw.Code, rw.Body.String())
	}
}

// Without secret:write the caller can't read the SMTP credentials or send — a
// 403 — even though the same secret:read role can list and preview templates.
func TestSendTestEmail_RequiresSecretWrite(t *testing.T) {
	h := newSecretsHarness(t)
	role := core.Role{Name: "viewer", Permissions: []core.Permission{core.PermSecretRead}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "viewer-key", "t", "ws", "viewer", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue viewer token: %v", err)
	}
	if code := doAsToken(t, h, tok, "POST", "/api/v1/email-templates/send-test",
		[]byte(`{"to":"me@example.com"}`)); code != http.StatusForbidden {
		t.Fatalf("viewer should be 403; got %d", code)
	}
}

// With the Email integration available but no connection stored, the caller
// gets a 409 pointing them at the Email page — not a confusing dial error.
func TestSendTestEmail_NotConnected(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "POST", "/api/v1/email-templates/send-test", map[string]any{"to": "me@example.com"})
	if rw.Code != http.StatusConflict {
		t.Fatalf("want 409 when Email not connected; got %d body=%s", rw.Code, rw.Body.String())
	}
}

// Happy path: the rendered template actually reaches the (fake) SMTP server
// with the requested recipient and the connection's From address.
func TestSendTestEmail_Sends(t *testing.T) {
	// The fake SMTP server is on loopback; the package TestMain allows private
	// egress so the mailer's SSRF guard doesn't refuse it.
	h := newSecretsHarness(t)
	srv := newFakeSMTP(t)
	host, port, err := net.SplitHostPort(srv.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	storeEmailConn(t, h, map[string]string{
		"host": host,
		"port": port,
		"tls":  "none",
		"from": "reports@example.com",
	})

	rw := h.do(t, "POST", "/api/v1/email-templates/send-test", map[string]any{
		"to":   "ops@example.com",
		"html": "<div>{{.Body}}</div>",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("want 200; got %d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, from, data, to := srv.snapshot()
		if len(to) == 1 && strings.Contains(to[0], "ops@example.com") {
			if !strings.Contains(from, "reports@example.com") {
				t.Errorf("MAIL FROM = %q, want reports@example.com", from)
			}
			if !strings.Contains(data, "multipart/alternative") {
				t.Errorf("expected a multipart message; data=%q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("test message never reached the SMTP server; last to=%v", to)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A From address with a display name must split: the name rides the From:
// header, while MAIL FROM carries only the bare address. Sending the
// display-name form as the envelope sender ("<Reports <r@example.com>>") is an
// invalid reverse-path and real servers reject the whole message.
func TestSendTestEmail_DisplayNameSender(t *testing.T) {
	h := newSecretsHarness(t)
	srv := newFakeSMTP(t)
	host, port, err := net.SplitHostPort(srv.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	storeEmailConn(t, h, map[string]string{
		"host": host,
		"port": port,
		"tls":  "none",
		"from": "Reports <reports@example.com>",
	})

	rw := h.do(t, "POST", "/api/v1/email-templates/send-test", map[string]any{
		"to":   "ops@example.com",
		"html": "<div>{{.Body}}</div>",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("want 200; got %d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, from, data, to := srv.snapshot()
		if len(to) == 1 && strings.Contains(to[0], "ops@example.com") {
			if !strings.Contains(from, "<reports@example.com>") || strings.Contains(from, "Reports") {
				t.Errorf("MAIL FROM = %q, want the bare address only", from)
			}
			if !strings.Contains(data, `From: "Reports" <reports@example.com>`) {
				t.Errorf("From header lost the display name; data=%q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("test message never reached the SMTP server; last to=%v", to)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
