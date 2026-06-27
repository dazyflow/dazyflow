// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ---- orgprofile pure helpers ---------------------------------------

func TestDefaultOrgDisplayName_Cov(t *testing.T) {
	cases := map[string]struct {
		email string
		want  string
	}{
		"brand domain":       {"alice@acme.test", "Acme"},
		"strip generic":      {"bob@my.acme.com", "Acme"},
		"strip mail prefix":  {"x@mail.globex.io", "Globex"},
		"consumer gmail":     {"alice@gmail.com", "Alice"},
		"consumer outlook":   {"Bob.Smith@outlook.com", "Bob.smith"},
		"no at":              {"noatsign", ""},
		"at end":             {"local@", ""},
		"at start":           {"@domain.com", ""},
		"single label brand": {"u@acme", "Acme"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DefaultOrgDisplayName(tc.email); got != tc.want {
				t.Errorf("DefaultOrgDisplayName(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestDefaultOrgDisplayName_AllGenericLabels(t *testing.T) {
	// A domain made entirely of generic prefixes falls back to the
	// local-part (loop strips down to one label, the brand path titleizes).
	if got := DefaultOrgDisplayName("alice@my.team.mail"); got == "" {
		t.Errorf("expected non-empty name, got empty")
	}
}

func TestIsConsumerEmailDomain_Cov(t *testing.T) {
	for _, d := range []string{"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "msn.com", "yahoo.com", "yahoo.co.uk", "ymail.com", "icloud.com", "me.com", "mac.com", "proton.me", "protonmail.com", "aol.com", "fastmail.com"} {
		if !isConsumerEmailDomain(d) {
			t.Errorf("isConsumerEmailDomain(%q) = false, want true", d)
		}
	}
	if isConsumerEmailDomain("acme.com") {
		t.Errorf("acme.com should not be a consumer domain")
	}
}

func TestIsGenericDomainPrefix_Cov(t *testing.T) {
	for _, p := range []string{"my", "team", "mail", "email", "www", "app", "go", "MY", "Team"} {
		if !isGenericDomainPrefix(p) {
			t.Errorf("isGenericDomainPrefix(%q) = false, want true", p)
		}
	}
	if isGenericDomainPrefix("acme") {
		t.Errorf("acme should not be a generic prefix")
	}
}

func TestTitleize_Cov(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"  ":    "",
		"acme":  "Acme",
		"ACME":  "Acme",
		"aCmE":  "Acme",
		" foo ": "Foo",
		"a":     "A",
	}
	for in, want := range cases {
		if got := titleize(in); got != want {
			t.Errorf("titleize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOrgProfile_Suspended_Cov(t *testing.T) {
	if (OrgProfile{}).Suspended() {
		t.Error("zero OrgProfile should not be suspended")
	}
	if !(OrgProfile{Status: StatusSuspended}).Suspended() {
		t.Error("suspended status should report Suspended")
	}
}

// ---- blocklist pure helpers ----------------------------------------

func TestNormalizeBlockEmail_Cov(t *testing.T) {
	cases := map[string]string{
		"  Alice@ACME.com ": "alice@acme.com",
		"":                  "",
		"X":                 "x",
	}
	for in, want := range cases {
		if got := NormalizeBlockEmail(in); got != want {
			t.Errorf("NormalizeBlockEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmailDomain_Cov(t *testing.T) {
	cases := map[string]string{
		"alice@acme.com": "acme.com",
		" A@B.IO ":       "b.io",
		"noatsign":       "",
		"local@":         "",
		"@domain.com":    "",
	}
	for in, want := range cases {
		if got := emailDomain(in); got != want {
			t.Errorf("emailDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- password hashing / verification --------------------------------

func TestHashPassword_Cov(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("HashPassword(\"\") should error")
	}
	h, err := HashPassword("hunter2")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if len(h) == 0 {
		t.Error("expected a non-empty hash")
	}
}

func TestVerifyPassword_Cov(t *testing.T) {
	store, err := OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("OpenJSONUserStore: %v", err)
	}
	ctx := context.Background()
	hash, _ := HashPassword("correct horse")
	if err := store.PutUser(ctx, User{Email: "Alice@Example.com", PasswordHash: hash, Subject: "alice"}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	// no-password user, exercising the len(PasswordHash)==0 branch.
	if err := store.PutUser(ctx, User{Email: "sso@example.com", Subject: "sso"}); err != nil {
		t.Fatalf("PutUser sso: %v", err)
	}

	// Success, case-insensitive lookup.
	u, err := VerifyPassword(ctx, store, "alice@example.com", "correct horse")
	if err != nil || u.Subject != "alice" {
		t.Errorf("VerifyPassword success = %+v, %v", u, err)
	}
	// Wrong password.
	if _, err := VerifyPassword(ctx, store, "alice@example.com", "nope"); err != ErrInvalidCredential {
		t.Errorf("wrong password err = %v, want ErrInvalidCredential", err)
	}
	// Empty email / password short-circuit.
	if _, err := VerifyPassword(ctx, store, "", "x"); err != ErrInvalidCredential {
		t.Errorf("empty email err = %v", err)
	}
	if _, err := VerifyPassword(ctx, store, "alice@example.com", ""); err != ErrInvalidCredential {
		t.Errorf("empty password err = %v", err)
	}
	// Unknown user (timing-equalizer path).
	if _, err := VerifyPassword(ctx, store, "ghost@example.com", "x"); err != ErrInvalidCredential {
		t.Errorf("unknown user err = %v", err)
	}
	// SSO-only user with no password set.
	if _, err := VerifyPassword(ctx, store, "sso@example.com", "x"); err != ErrInvalidCredential {
		t.Errorf("no-password user err = %v", err)
	}
}

func TestUserHelpers_Cov(t *testing.T) {
	if (User{}).EmailVerified() {
		t.Error("zero user should not be verified")
	}
	now := time.Now()
	if !(User{VerifiedAt: &now}).EmailVerified() {
		t.Error("user with VerifiedAt should be verified")
	}
	if (User{}).Suspended() {
		t.Error("zero user not suspended")
	}
	if !(User{Status: StatusSuspended}).Suspended() {
		t.Error("suspended user should report Suspended")
	}
	// NotifyPrefs tri-state.
	if !(NotifyPrefs{}).EmailOnFlowFailureEnabled() {
		t.Error("unset notify pref defaults ON")
	}
	off := false
	if (NotifyPrefs{EmailOnFlowFailure: &off}).EmailOnFlowFailureEnabled() {
		t.Error("explicit off should be off")
	}
	on := true
	if !(NotifyPrefs{EmailOnFlowFailure: &on}).EmailOnFlowFailureEnabled() {
		t.Error("explicit on should be on")
	}
}

// ---- JSONUserStore file round-trip ---------------------------------

func TestJSONUserStore_FileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	ctx := context.Background()
	s1, err := OpenJSONUserStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	u := User{Email: "Bob@Example.com", Subject: "bob", Roles: []core.Role{{Name: "editor"}}}
	if err := s1.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	// PutUser with empty email rejected.
	if err := s1.PutUser(ctx, User{Email: "  "}); err == nil {
		t.Error("empty email should be rejected")
	}

	// Reopen: data should load + normalize (lowercased key).
	s2, err := OpenJSONUserStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.GetByEmail(ctx, "bob@example.com")
	if err != nil || got.Subject != "bob" {
		t.Errorf("reloaded user = %+v, %v", got, err)
	}
	list, err := s2.ListUsers(ctx)
	if err != nil || len(list) != 1 {
		t.Errorf("ListUsers = %v, %v", list, err)
	}
	// GetByEmail unknown.
	if _, err := s2.GetByEmail(ctx, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("unknown user err = %v", err)
	}
	// DeleteUser idempotent.
	if err := s2.DeleteUser(ctx, "BOB@example.com"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := s2.DeleteUser(ctx, "bob@example.com"); err != nil {
		t.Fatalf("DeleteUser idempotent: %v", err)
	}
	if _, err := s2.GetByEmail(ctx, "bob@example.com"); err != ErrUnknownUser {
		t.Errorf("after delete err = %v", err)
	}
}

func TestJSONFileStore_LoadErrors(t *testing.T) {
	// Malformed JSON should surface a parse error.
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJSONUserStore(bad); err == nil {
		t.Error("expected parse error for malformed file")
	}
	// Missing file is fine (empty store).
	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := OpenJSONUserStore(missing); err != nil {
		t.Errorf("missing file should be ok, got %v", err)
	}
}

// ---- JSONInvitationStore --------------------------------------------

func TestJSONInvitationStore_Cov(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inv.json")
	ctx := context.Background()
	s, err := OpenJSONInvitationStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now()
	inv := Invitation{
		Token: "inv_1", Email: "Bob@Example.com", Tenant: "acme", Workspace: "ws1",
		Roles: []core.Role{{Name: "editor"}}, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := s.PutInvitation(ctx, inv); err != nil {
		t.Fatalf("PutInvitation: %v", err)
	}
	// Validation paths.
	if err := s.PutInvitation(ctx, Invitation{Tenant: "acme"}); err == nil {
		t.Error("missing token should be rejected")
	}
	if err := s.PutInvitation(ctx, Invitation{Token: "x"}); err == nil {
		t.Error("missing tenant should be rejected")
	}

	got, err := s.GetByToken(ctx, "inv_1")
	if err != nil || got.Email != "bob@example.com" {
		t.Errorf("GetByToken = %+v, %v", got, err)
	}
	if _, err := s.GetByToken(ctx, "nope"); err != ErrUnknownInvitation {
		t.Errorf("unknown token err = %v", err)
	}
	if !got.IsPending(now) {
		t.Error("fresh invite should be pending")
	}

	// A second invite for the same email in another tenant.
	if err := s.PutInvitation(ctx, Invitation{Token: "inv_2", Email: "bob@example.com", Tenant: "globex", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("PutInvitation 2: %v", err)
	}
	if list, err := s.ListByTenant(ctx, "acme"); err != nil || len(list) != 1 {
		t.Errorf("ListByTenant = %v, %v", list, err)
	}
	if list, err := s.ListByEmail(ctx, "BOB@example.com"); err != nil || len(list) != 2 {
		t.Errorf("ListByEmail = %v, %v", list, err)
	}

	// MarkAccepted / MarkRevoked, and their unknown-token paths.
	if err := s.MarkAccepted(ctx, "inv_1", now); err != nil {
		t.Fatalf("MarkAccepted: %v", err)
	}
	if err := s.MarkAccepted(ctx, "nope", now); err != ErrUnknownInvitation {
		t.Errorf("MarkAccepted unknown = %v", err)
	}
	if err := s.MarkRevoked(ctx, "inv_2", now); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}
	if err := s.MarkRevoked(ctx, "nope", now); err != ErrUnknownInvitation {
		t.Errorf("MarkRevoked unknown = %v", err)
	}
	got, _ = s.GetByToken(ctx, "inv_1")
	if got.IsPending(now) {
		t.Error("accepted invite should not be pending")
	}

	// DeleteByEmail / DeleteByTenant.
	if n, err := s.DeleteByTenant(ctx, "globex"); err != nil || n != 1 {
		t.Errorf("DeleteByTenant = %d, %v", n, err)
	}
	if n, err := s.DeleteByEmail(ctx, "bob@example.com"); err != nil || n != 1 {
		t.Errorf("DeleteByEmail = %d, %v", n, err)
	}
}

func TestInvitation_SignupAndPending(t *testing.T) {
	if !(Invitation{Tenant: SignupInviteTenant}).IsSignupInvite() {
		t.Error("signup tenant should report IsSignupInvite")
	}
	if (Invitation{Tenant: "acme"}).IsSignupInvite() {
		t.Error("org tenant is not a signup invite")
	}
	now := time.Now()
	past := now.Add(-time.Hour)
	if (Invitation{ExpiresAt: past}).IsPending(now) {
		t.Error("expired invite not pending")
	}
	if (Invitation{ExpiresAt: now.Add(time.Hour), AcceptedAt: &now}).IsPending(now) {
		t.Error("accepted invite not pending")
	}
	if (Invitation{ExpiresAt: now.Add(time.Hour), RevokedAt: &now}).IsPending(now) {
		t.Error("revoked invite not pending")
	}
}

func TestMintInvitationToken_Cov(t *testing.T) {
	tok, err := MintInvitationToken()
	if err != nil {
		t.Fatalf("MintInvitationToken: %v", err)
	}
	if !strings.HasPrefix(tok, "inv_") || len(tok) != len("inv_")+32 {
		t.Errorf("token = %q, want inv_ + 32 hex", tok)
	}
	tok2, _ := MintInvitationToken()
	if tok == tok2 {
		t.Error("tokens should be unique")
	}
}
