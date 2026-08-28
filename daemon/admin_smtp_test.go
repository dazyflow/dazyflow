// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"
	"time"
)

// A non-platform-admin must never reach the SMTP smoke test — the mailer
// is instance-wide infrastructure. Gating runs before the mailer check,
// so this holds even on a harness with no mailer configured.
func TestSMTPTest_RequiresPlatformAdmin(t *testing.T) {
	h := newGatewayHarness(t)
	if rw := h.do(t, "POST", "/api/v1/admin/smtp-test", nil); rw.Code != 403 {
		t.Errorf("editor should be 403; got %d body=%s", rw.Code, rw.Body.String())
	}
	if rw := h.adminDo(t, "POST", "/api/v1/admin/smtp-test", nil); rw.Code != 403 {
		t.Errorf("tenant admin should be 403 (instance-wide); got %d body=%s", rw.Code, rw.Body.String())
	}
}

// With no mailer wired, a platform admin gets a clear 501 rather than a
// 500 — "not configured" is a normal state the UI surfaces as guidance.
func TestSMTPTest_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.platformDo(t, "POST", "/api/v1/admin/smtp-test", nil)
	if rw.Code != 501 {
		t.Fatalf("want 501 when mailer off; got %d body=%s", rw.Code, rw.Body.String())
	}
}

// A bad recipient is rejected as client input (400), before any dial.
func TestSMTPTest_InvalidRecipient(t *testing.T) {
	h := newGatewayHarness(t)
	srv := newFakeSMTP(t)
	m, err := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	h.svc.Mailer = m
	rw := h.platformDo(t, "POST", "/api/v1/admin/smtp-test", map[string]any{"to": "not-an-address"})
	if rw.Code != 400 {
		t.Fatalf("want 400 for junk recipient; got %d body=%s", rw.Code, rw.Body.String())
	}
}

// Happy path: the message actually reaches the (fake) SMTP server with
// the requested recipient and the configured From.
func TestSMTPTest_Sends(t *testing.T) {
	h := newGatewayHarness(t)
	srv := newFakeSMTP(t)
	m, err := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	h.svc.Mailer = m

	rw := h.platformDo(t, "POST", "/api/v1/admin/smtp-test", map[string]any{"to": "ops@example.com"})
	if rw.Code != 200 {
		t.Fatalf("want 200; got %d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, from, _, to := srv.snapshot()
		if len(to) == 1 && strings.Contains(to[0], "ops@example.com") {
			if !strings.Contains(from, "noreply@example.com") {
				t.Errorf("MAIL FROM = %q, want noreply@example.com", from)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("test message never reached the SMTP server; last to=%v", to)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
