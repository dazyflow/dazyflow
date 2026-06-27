// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

func TestValidateGoogleClaims(t *testing.T) {
	verified := googleUserInfo{Email: "a@acme.test", EmailVerified: true, HD: "acme.test"}

	cases := []struct {
		name       string
		info       googleUserInfo
		email      string
		cfg        auth.OrgAuthConfig
		wantReason string
		wantStatus int
	}{
		{
			name:  "ok, no domain restriction",
			info:  googleUserInfo{Email: "a@gmail.com", EmailVerified: true},
			email: "a@gmail.com",
			cfg:   auth.OrgAuthConfig{},
		},
		{
			name:  "ok, domain matches (case-insensitive)",
			info:  googleUserInfo{Email: "a@acme.test", EmailVerified: true, HD: "ACME.test"},
			email: "a@acme.test",
			cfg:   auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
		},
		{
			name:       "empty email",
			info:       verified,
			email:      "",
			cfg:        auth.OrgAuthConfig{},
			wantReason: "no_email",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "unverified email",
			info:       googleUserInfo{Email: "a@acme.test", EmailVerified: false},
			email:      "a@acme.test",
			cfg:        auth.OrgAuthConfig{},
			wantReason: "not_verified",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "domain mismatch",
			info:       googleUserInfo{Email: "a@gmail.com", EmailVerified: true, HD: "gmail.com"},
			email:      "a@gmail.com",
			cfg:        auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
			wantReason: "domain_mismatch",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "domain restricted but hd empty (personal account)",
			info:       googleUserInfo{Email: "a@gmail.com", EmailVerified: true, HD: ""},
			email:      "a@gmail.com",
			cfg:        auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
			wantReason: "domain_mismatch",
			wantStatus: http.StatusForbidden,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, status, msg := validateGoogleClaims(c.info, c.email, c.cfg)
			if reason != c.wantReason || status != c.wantStatus {
				t.Fatalf("validateGoogleClaims = (%q, %d), want (%q, %d)", reason, status, c.wantReason, c.wantStatus)
			}
			// Success returns no message; failures must carry one.
			if (msg == "") != (c.wantReason == "") {
				t.Errorf("msg = %q for reason %q", msg, c.wantReason)
			}
			// The domain-mismatch message names the required domain so the
			// user knows which account to use.
			if c.wantReason == "domain_mismatch" && !strings.Contains(msg, c.cfg.GoogleWorkspaceDomain) {
				t.Errorf("domain_mismatch msg %q should name %q", msg, c.cfg.GoogleWorkspaceDomain)
			}
		})
	}
}
