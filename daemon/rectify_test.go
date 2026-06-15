package daemon

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

func seedUser(t *testing.T, users auth.UserStore, email, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := users.PutUser(context.Background(), auth.User{
		Email: email, Subject: email, Tenant: "usr_x", Workspace: "default", PasswordHash: hash,
	}); err != nil {
		t.Fatalf("put user: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	users, _ := auth.OpenJSONUserStore("")
	seedUser(t, users, "alice@example.com", "oldpassword")
	sessions := auth.NewMemSessionStore()
	_ = sessions.PutSession(ctx, auth.Session{ID: "s1", Subject: "alice@example.com"})

	h := &HTTPGateway{svc: &Service{}, Users: users, Sessions: sessions}

	body := `{"current_password":"oldpassword","new_password":"brandnewpass"}`
	r := httptest.NewRequest("POST", "/api/v1/me/password", strings.NewReader(body))
	rw := httptest.NewRecorder()
	h.changePasswordHandler(rw, r, core.Principal{Subject: "alice@example.com"})
	if rw.Code != 200 {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	// New password works, old one no longer does.
	if _, err := auth.VerifyPassword(ctx, users, "alice@example.com", "brandnewpass"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if _, err := auth.VerifyPassword(ctx, users, "alice@example.com", "oldpassword"); err == nil {
		t.Error("old password still accepted")
	}
	// Sessions revoked.
	if _, err := sessions.GetSession(ctx, "s1"); err == nil {
		t.Error("session not revoked after password change")
	}
}

func TestChangePassword_WrongCurrent(t *testing.T) {
	users, _ := auth.OpenJSONUserStore("")
	seedUser(t, users, "alice@example.com", "oldpassword")
	h := &HTTPGateway{svc: &Service{}, Users: users, Sessions: auth.NewMemSessionStore()}

	body := `{"current_password":"WRONG","new_password":"brandnewpass"}`
	r := httptest.NewRequest("POST", "/api/v1/me/password", strings.NewReader(body))
	rw := httptest.NewRecorder()
	h.changePasswordHandler(rw, r, core.Principal{Subject: "alice@example.com"})
	if rw.Code != 401 {
		t.Fatalf("status=%d, want 401", rw.Code)
	}
	// Unchanged.
	if _, err := auth.VerifyPassword(context.Background(), users, "alice@example.com", "oldpassword"); err != nil {
		t.Error("password should be unchanged after a failed attempt")
	}
}

func TestChangeEmail_Rekey(t *testing.T) {
	ctx := context.Background()
	const oldEmail, newEmail = "alice@example.com", "alice@new.com"

	users, _ := auth.OpenJSONUserStore("")
	seedUser(t, users, oldEmail, "pw12345678")
	sessions := auth.NewMemSessionStore()
	_ = sessions.PutSession(ctx, auth.Session{ID: "s1", Subject: oldEmail})
	keys := auth.NewMemKeyStore()
	_ = keys.PutKey(ctx, auth.APIKey{ID: "k1", Subject: oldEmail, Tenant: "usr_x"})
	members := newFakeMembershipStore()
	_ = members.PutMembership(ctx, auth.Membership{UserEmail: oldEmail, Tenant: "acme", Workspace: "ws"})

	h := &HTTPGateway{
		svc:         &Service{AdminKeys: keys},
		Users:       users,
		Sessions:    sessions,
		Memberships: members,
	}

	body := `{"new_email":"alice@new.com","password":"pw12345678"}`
	r := httptest.NewRequest("POST", "/api/v1/me/email", strings.NewReader(body))
	rw := httptest.NewRecorder()
	h.changeEmailHandler(rw, r, core.Principal{Subject: oldEmail})
	if rw.Code != 200 {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}

	// Old identity gone, new identity present with re-keyed subject.
	if _, err := users.GetByEmail(ctx, oldEmail); err == nil {
		t.Error("old user row survived re-key")
	}
	nu, err := users.GetByEmail(ctx, newEmail)
	if err != nil {
		t.Fatalf("new user missing: %v", err)
	}
	if nu.Subject != newEmail {
		t.Errorf("subject not re-keyed: %q", nu.Subject)
	}
	// Membership re-pointed.
	if ms, _ := members.ListByEmail(ctx, newEmail); len(ms) != 1 {
		t.Errorf("membership not re-pointed to new email: %d", len(ms))
	}
	if ms, _ := members.ListByEmail(ctx, oldEmail); len(ms) != 0 {
		t.Errorf("membership left under old email: %d", len(ms))
	}
	// API key subject re-pointed.
	if ks, _ := keys.ListBySubject(ctx, newEmail); len(ks) != 1 {
		t.Errorf("api key not re-pointed: %d", len(ks))
	}
	// Old sessions revoked.
	if _, err := sessions.GetSession(ctx, "s1"); err == nil {
		t.Error("old session not revoked")
	}
}

func TestChangeEmail_TargetTaken(t *testing.T) {
	users, _ := auth.OpenJSONUserStore("")
	seedUser(t, users, "alice@example.com", "pw12345678")
	seedUser(t, users, "taken@example.com", "other")
	h := &HTTPGateway{svc: &Service{}, Users: users, Sessions: auth.NewMemSessionStore()}

	body := `{"new_email":"taken@example.com","password":"pw12345678"}`
	r := httptest.NewRequest("POST", "/api/v1/me/email", strings.NewReader(body))
	rw := httptest.NewRecorder()
	h.changeEmailHandler(rw, r, core.Principal{Subject: "alice@example.com"})
	if rw.Code != 409 {
		t.Fatalf("status=%d, want 409 (email taken)", rw.Code)
	}
}
