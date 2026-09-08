// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func awaitMail(t *testing.T, srv *fakeSMTP, want string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, data, _ := srv.snapshot()
		body := qpDecode(data)
		if strings.Contains(body, want) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("no mail containing %q arrived; last body:\n%s", want, body)
			return ""
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMailLang_PasswordResetFollowsTheAccount(t *testing.T) {
	t.Parallel()
	h, users, srv := verificationHarness(t)

	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "sven@example.com", "password": "OldPassw0rd!23",
	}); rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	u, err := users.GetByEmail(t.Context(), "sven@example.com")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	u.UI.Language = "sv"
	if err := users.PutUser(t.Context(), u); err != nil {
		t.Fatalf("put user: %v", err)
	}

	if rw := h.do(t, "POST", "/api/v1/auth/forgot-password", map[string]string{
		"email": "sven@example.com",
	}); rw.Code != http.StatusOK {
		t.Fatalf("forgot-password: %d", rw.Code)
	}
	body := awaitMail(t, srv, "Återställ ditt lösenord")
	if strings.Contains(body, "Reset your password") {
		t.Errorf("the English heading is still in the Swedish mail:\n%s", body)
	}
	if !strings.Contains(body, "Länken går ut den") {
		t.Errorf("expiry sentence not translated:\n%s", body)
	}
}

func TestMailLang_DefaultsToEnglish(t *testing.T) {
	t.Parallel()
	h, _, srv := verificationHarness(t)
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "english@example.com", "password": "Passw0rd!2345",
	}); rw.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rw.Code, rw.Body.String())
	}
	awaitMail(t, srv, "Your account is ready")
}

func TestMailLangResolvers(t *testing.T) {
	t.Parallel()
	h, users, _ := verificationHarness(t)
	ctx := t.Context()
	if err := users.PutUser(ctx, auth.User{Email: "sv@example.com", UI: auth.UIPrefs{Language: "sv"}}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := users.PutUser(ctx, auth.User{Email: "plain@example.com"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if got := h.gw.mailLang(ctx, "sv@example.com"); got != "sv" {
		t.Errorf("mailLang = %q, want sv", got)
	}
	if got := h.gw.mailLang(ctx, "plain@example.com"); got != "" {
		t.Errorf("mailLang(no preference) = %q, want empty", got)
	}
	if got := h.gw.mailLang(ctx, "nobody@example.com"); got != "" {
		t.Errorf("mailLang(unknown user) = %q, want empty", got)
	}

	if got := h.gw.inviteLang(ctx, "nobody@example.com", "sv@example.com"); got != "sv" {
		t.Errorf("inviteLang fell back wrong: %q, want sv (the inviter's)", got)
	}
	if got := h.gw.inviteLang(ctx, "sv@example.com", "plain@example.com"); got != "sv" {
		t.Errorf("inviteLang = %q, want the invitee's own sv", got)
	}

	if got := flowLang(core.Graph{Language: "sv"}); got != "sv" {
		t.Errorf("flowLang = %q, want sv", got)
	}
	if got := flowLang(core.Graph{}); got != "" {
		t.Errorf("flowLang(unset) = %q, want empty", got)
	}
}
