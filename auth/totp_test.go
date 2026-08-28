// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/pquerna/otp/totp"
)

// testTOTPKey returns a deterministic-length (32-byte) random AES key for
// the encrypt-at-rest path.
func testTOTPKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

// secretFromSetup pulls the base32 secret out of a TOTPSetup so the test
// can mint a valid code the way an authenticator app would.
func codeFor(t *testing.T, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

func newUserStoreWithUser(t *testing.T, email string) *JSONUserStore {
	t.Helper()
	store, err := OpenJSONUserStore("") // path "" → in-memory, no flush
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.PutUser(context.Background(), User{Email: email, Subject: email}); err != nil {
		t.Fatalf("put user: %v", err)
	}
	return store
}

// TestTOTP_RejectsReplayWithinWindow locks in replay protection: a TOTP
// code is valid for ~90s, so the same code must not be redeemable twice even
// with a fresh login challenge.
func TestTOTP_RejectsReplayWithinWindow(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "replay@example.com"
	users := newUserStoreWithUser(t, email)

	setup, err := EnrolStart(ctx, users, key, email)
	if err != nil {
		t.Fatalf("EnrolStart: %v", err)
	}
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	challenges := NewMemTOTPChallengeStore()
	code := codeFor(t, setup.SecretBase32)

	// First use of the code succeeds.
	tok1, _ := IssueTOTPChallenge(ctx, challenges, email)
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok1, code, ""); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	// Replaying the SAME code (same time-step) on a fresh challenge must be
	// rejected — the consumed step was burned.
	tok2, _ := IssueTOTPChallenge(ctx, challenges, email)
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok2, code, ""); err != ErrTOTPInvalid {
		t.Fatalf("replay consume err = %v, want ErrTOTPInvalid", err)
	}
}

func TestTOTPEnrolConfirmAndLogin(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "owner@example.com"
	users := newUserStoreWithUser(t, email)

	// Enrol: a pending secret is stored but 2FA is not yet enabled.
	setup, err := EnrolStart(ctx, users, key, email)
	if err != nil {
		t.Fatalf("EnrolStart: %v", err)
	}
	if setup.SecretBase32 == "" {
		t.Fatal("EnrolStart returned empty secret")
	}
	if !strings.HasPrefix(setup.QRPNGDataURL, "data:image/png;base64,") {
		t.Errorf("QR data URL has unexpected prefix: %q", setup.QRPNGDataURL[:min(40, len(setup.QRPNGDataURL))])
	}
	if st, _ := LoadTOTPStatus(ctx, users, email); st.Enabled {
		t.Fatal("status enabled before confirm")
	}

	// Confirm with a valid code → 2FA enabled + recovery codes minted.
	codes, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32))
	if err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}
	if len(codes) != totpRecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), totpRecoveryCodeCount)
	}
	st, _ := LoadTOTPStatus(ctx, users, email)
	if !st.Enabled || st.RecoveryCodesLeft != totpRecoveryCodeCount {
		t.Fatalf("post-confirm status = %+v", st)
	}

	// A wrong code at confirm time is rejected.
	users2 := newUserStoreWithUser(t, "other@example.com")
	s2, _ := EnrolStart(ctx, users2, key, "other@example.com")
	if _, err := EnrolConfirm(ctx, users2, key, "other@example.com", "000000"); err == nil {
		_ = s2
		t.Fatal("EnrolConfirm accepted a bad code")
	}

	// Login leg: a challenge consumed with a valid TOTP code succeeds.
	challenges := NewMemTOTPChallengeStore()
	tok, err := IssueTOTPChallenge(ctx, challenges, email)
	if err != nil {
		t.Fatalf("IssueTOTPChallenge: %v", err)
	}
	res, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, codeFor(t, setup.SecretBase32), "")
	if err != nil {
		t.Fatalf("ConsumeTOTPChallenge (code): %v", err)
	}
	if res.Factor != FactorTOTP || res.User.Email != email {
		t.Fatalf("unexpected result: %+v", res)
	}
	// Single-use: the same token can't be redeemed twice.
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, codeFor(t, setup.SecretBase32), ""); err != ErrChallengeUnknown {
		t.Fatalf("reused challenge err = %v, want ErrChallengeUnknown", err)
	}

	// Recovery code path: consumes one code and decrements the count.
	tok2, _ := IssueTOTPChallenge(ctx, challenges, email)
	res2, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok2, "", codes[0])
	if err != nil {
		t.Fatalf("ConsumeTOTPChallenge (recovery): %v", err)
	}
	if res2.Factor != FactorRecoveryCode {
		t.Fatalf("factor = %q, want recovery_code", res2.Factor)
	}
	if st, _ := LoadTOTPStatus(ctx, users, email); st.RecoveryCodesLeft != totpRecoveryCodeCount-1 {
		t.Fatalf("recovery codes left = %d, want %d", st.RecoveryCodesLeft, totpRecoveryCodeCount-1)
	}
	// The burned recovery code can't be reused.
	tok3, _ := IssueTOTPChallenge(ctx, challenges, email)
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok3, "", codes[0]); err != ErrRecoveryCodeInvalid {
		t.Fatalf("reused recovery code err = %v, want ErrRecoveryCodeInvalid", err)
	}
}

func TestTOTPChallengeExpiry(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "exp@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	challenges := NewMemTOTPChallengeStore()
	tok, _ := newChallengeToken()
	// Inject an already-expired challenge directly.
	_ = challenges.Put(ctx, tok, TOTPChallenge{Email: email, ExpiresAt: time.Now().Add(-time.Minute)})
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, codeFor(t, setup.SecretBase32), ""); err != ErrChallengeExpired {
		t.Fatalf("err = %v, want ErrChallengeExpired", err)
	}
}

func TestEnrolStartRefusesWhenEnabled(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "dup@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}
	if _, err := EnrolStart(ctx, users, key, email); err != ErrTOTPAlreadyEnrolled {
		t.Fatalf("EnrolStart err = %v, want ErrTOTPAlreadyEnrolled", err)
	}
}

func TestOTPAuthURLCarriesIssuer(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "issuer@example.com"
	users := newUserStoreWithUser(t, email)
	setup, err := EnrolStart(ctx, users, key, email)
	if err != nil {
		t.Fatalf("EnrolStart: %v", err)
	}
	u, err := url.Parse(setup.OTPAuthURL)
	if err != nil {
		t.Fatalf("parse otpauth url: %v", err)
	}
	if u.Query().Get("issuer") != TOTPIssuer {
		t.Errorf("issuer = %q, want %q", u.Query().Get("issuer"), TOTPIssuer)
	}
}

func TestLoadTOTPKey_Cov(t *testing.T) {
	// Unset → ErrTOTPKeyMissing.
	t.Setenv(totpEnvKey, "")
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMissing) {
		t.Errorf("missing key err = %v, want ErrTOTPKeyMissing", err)
	}

	// Valid 32-byte std base64.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(key))
	got, err := LoadTOTPKey()
	if err != nil || len(got) != 32 {
		t.Errorf("valid key = %d bytes, %v", len(got), err)
	}

	// Wrong length (valid base64 but 16 bytes) → malformed.
	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMalformed) {
		t.Errorf("short key err = %v, want ErrTOTPKeyMalformed", err)
	}

	// Not base64 at all → malformed.
	t.Setenv(totpEnvKey, "!!!not base64!!!")
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMalformed) {
		t.Errorf("non-base64 key err = %v, want ErrTOTPKeyMalformed", err)
	}
}

func TestDisableTOTP_Cov(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "disable@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	if err := DisableTOTP(ctx, users, email); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	st, _ := LoadTOTPStatus(ctx, users, email)
	if st.Enabled || st.RecoveryCodesLeft != 0 {
		t.Errorf("post-disable status = %+v", st)
	}

	// Unknown user surfaces the store error.
	if err := DisableTOTP(ctx, users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("DisableTOTP(unknown) = %v, want ErrUnknownUser", err)
	}
}

func TestRegenerateRecoveryCodes_Cov(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "regen@example.com"
	users := newUserStoreWithUser(t, email)

	// Not enrolled yet → ErrTOTPNotEnrolled.
	if _, err := RegenerateRecoveryCodes(ctx, users, email); !errors.Is(err, ErrTOTPNotEnrolled) {
		t.Errorf("regen before enrol err = %v, want ErrTOTPNotEnrolled", err)
	}
	// Unknown user → store error.
	if _, err := RegenerateRecoveryCodes(ctx, users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("regen unknown = %v, want ErrUnknownUser", err)
	}

	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}
	codes, err := RegenerateRecoveryCodes(ctx, users, email)
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	if len(codes) != totpRecoveryCodeCount {
		t.Errorf("got %d codes, want %d", len(codes), totpRecoveryCodeCount)
	}
}

func TestLoadTOTPStatus_UnknownUser(t *testing.T) {
	users := newUserStoreWithUser(t, "real@example.com")
	if _, err := LoadTOTPStatus(context.Background(), users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("LoadTOTPStatus(unknown) = %v, want ErrUnknownUser", err)
	}
}

func TestDecryptTOTPSecret_Corrupt(t *testing.T) {
	key := testTOTPKey(t)
	// Too-short blob → ErrTOTPSecretCorrupt.
	if _, err := decryptTOTPSecret(key, []byte("short")); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("short blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
	// Valid-length blob but garbage ciphertext → corrupt.
	blob, err := encryptTOTPSecret(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob[len(blob)-1] ^= 0xFF // tamper
	if _, err := decryptTOTPSecret(key, blob); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("tampered blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
}

// TestTOTPChallenge_BruteForceCap verifies the per-challenge guess limit: after
// maxTOTPChallengeAttempts wrong codes the challenge is invalidated, so even a
// correct code can't redeem it (the attacker must redo the rate-limited first
// leg). Closes the "challenge survives failed guesses" brute-force gap.
func TestTOTPChallenge_BruteForceCap(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "brute@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	valid := codeFor(t, setup.SecretBase32)
	wrong := "000000"
	if wrong == valid {
		wrong = "111111"
	}
	challenges := NewMemTOTPChallengeStore()
	tok, _ := IssueTOTPChallenge(ctx, challenges, email)

	// Each wrong guess up to the cap returns ErrTOTPInvalid.
	for i := 0; i < maxTOTPChallengeAttempts; i++ {
		if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, wrong, ""); err != ErrTOTPInvalid {
			t.Fatalf("guess %d err = %v, want ErrTOTPInvalid", i+1, err)
		}
	}
	// The challenge is now gone — a further attempt (even with the right code)
	// is rejected as unknown, forcing a fresh sign-in.
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, valid, ""); err != ErrChallengeUnknown {
		t.Fatalf("post-cap valid code err = %v, want ErrChallengeUnknown", err)
	}
}

// TestTOTPChallenge_OrgOverride verifies the SSO leg's resolved-org override is
// applied to the redeemed user, so a 2FA SSO sign-in lands in the org the user
// signed into (with its membership roles), not their home org.
func TestTOTPChallenge_OrgOverride(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "sso@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	roles := []core.Role{{Name: "editor"}}
	challenges := NewMemTOTPChallengeStore()
	tok, err := IssueTOTPChallengeWithOrg(ctx, challenges, email, "acme", "ws-prod", roles)
	if err != nil {
		t.Fatalf("IssueTOTPChallengeWithOrg: %v", err)
	}
	res, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, codeFor(t, setup.SecretBase32), "")
	if err != nil {
		t.Fatalf("ConsumeTOTPChallenge: %v", err)
	}
	if res.User.Tenant != "acme" || res.User.Workspace != "ws-prod" {
		t.Errorf("org override not applied: tenant=%q workspace=%q", res.User.Tenant, res.User.Workspace)
	}
	if len(res.User.Roles) != 1 || res.User.Roles[0].Name != "editor" {
		t.Errorf("role override not applied: %+v", res.User.Roles)
	}
}
