// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package imaputil

import (
	"context"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ModeSTARTTLS}, // blank must agree with the Email drop's default
		{in: " starttls ", want: ModeSTARTTLS},
		{in: "implicit", want: ModeImplicit},
		{in: "none", want: ModeNone},
		{in: "ssl", wantErr: true},
		{in: "TLS", wantErr: true}, // the stored values are lowercase; a near-miss is a typo, not a synonym
	} {
		got, err := ParseMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseMode(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

// The port default follows the security mode, and both callers — the drop and
// the verifier — go through this one function precisely so a "Test connection"
// can't probe a different port than the run will use.
func TestParsePortFollowsTheMode(t *testing.T) {
	for _, tc := range []struct {
		port, mode string
		want       int
		wantErr    bool
	}{
		{port: "", mode: ModeImplicit, want: 993},
		{port: "", mode: ModeSTARTTLS, want: 143},
		{port: "", mode: ModeNone, want: 143},
		{port: " 1143 ", mode: ModeSTARTTLS, want: 1143},
		{port: "0", mode: ModeSTARTTLS, wantErr: true},
		{port: "-1", mode: ModeSTARTTLS, wantErr: true},
		{port: "993x", mode: ModeImplicit, wantErr: true},
	} {
		got, err := ParsePort(tc.port, tc.mode)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePort(%q, %q) = %d, want an error", tc.port, tc.mode, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParsePort(%q, %q) = %d, %v; want %d", tc.port, tc.mode, got, err, tc.want)
		}
	}
}

func TestConfigFromConn(t *testing.T) {
	cfg, err := ConfigFromConn(map[string]string{
		"host":     " imap.example.com ",
		"username": " ada@example.com ",
		"password": " keep me ",
		"tls":      "implicit",
	})
	if err != nil {
		t.Fatalf("ConfigFromConn: %v", err)
	}
	if cfg.Host != "imap.example.com" || cfg.Username != "ada@example.com" {
		t.Errorf("host/username not trimmed: %+v", cfg)
	}
	// A password is deliberately not trimmed — surrounding spaces can be part
	// of it, and silently eating them locks someone out with a green tick.
	if cfg.Password != " keep me " {
		t.Errorf("password = %q, want it verbatim", cfg.Password)
	}
	if cfg.Port != 993 {
		t.Errorf("port = %d, want implicit TLS's 993", cfg.Port)
	}
	if cfg.Folder != DefaultFolder {
		t.Errorf("folder = %q, want %q", cfg.Folder, DefaultFolder)
	}

	if _, err := ConfigFromConn(map[string]string{}); err == nil {
		t.Error("a connection with no host should not validate")
	}
}

// A login over an unencrypted connection would put the mailbox password on the
// wire in the clear. net/smtp's PlainAuth refuses that for the Email drop;
// IMAP has no equivalent built in, so the refusal is ours to make — and it has
// to happen before any I/O, or the credential is already gone.
func TestDialRefusesACleartextLogin(t *testing.T) {
	_, err := Dial(context.Background(), Config{
		Host: "imap.example.com", Port: 143, TLS: ModeNone,
		Username: "ada", Password: "secret",
	})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("want a refusal before dialing, got %v", err)
	}
	// The message has to name both ways out: "none" is a choice someone made
	// on the integration page, so they need to know which fix applies.
	for _, want := range []string{"starttls", "implicit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal doesn't mention %q: %v", want, err)
		}
	}
}

// Loopback is exempt, for the same reason PlainAuth exempts it: a local mail
// bridge has no network to sniff. The dial then fails on the egress guard
// (this package's tests don't opt into private egress) — which is a different
// error, and that difference is the assertion.
func TestDialAllowsACleartextLoginToLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		_, err := Dial(context.Background(), Config{
			Host: host, Port: 143, TLS: ModeNone, Username: "ada", Password: "secret",
		})
		if err != nil && strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s was refused as a cleartext target: %v", host, err)
		}
	}
}

func TestDialWithNoHost(t *testing.T) {
	if _, err := Dial(context.Background(), Config{}); err == nil {
		t.Fatal("want an error with no host configured")
	}
}
