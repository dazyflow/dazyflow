// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

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

// Locks in replay protection: a TOTP code is valid for ~90s, so the same code
// must not be redeemable twice even with a fresh login challenge.
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

	users2 := newUserStoreWithUser(t, "other@example.com")
	s2, _ := EnrolStart(ctx, users2, key, "other@example.com")
	if _, err := EnrolConfirm(ctx, users2, key, "other@example.com", "000000"); err == nil {
		_ = s2
		t.Fatal("EnrolConfirm accepted a bad code")
	}

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
	t.Setenv(totpEnvKey, "")
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMissing) {
		t.Errorf("missing key err = %v, want ErrTOTPKeyMissing", err)
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(key))
	got, err := LoadTOTPKey()
	if err != nil || len(got) != 32 {
		t.Errorf("valid key = %d bytes, %v", len(got), err)
	}

	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMalformed) {
		t.Errorf("short key err = %v, want ErrTOTPKeyMalformed", err)
	}

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

	if err := DisableTOTP(ctx, users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("DisableTOTP(unknown) = %v, want ErrUnknownUser", err)
	}
}

func TestRegenerateRecoveryCodes_Cov(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "regen@example.com"
	users := newUserStoreWithUser(t, email)

	if _, err := RegenerateRecoveryCodes(ctx, users, email); !errors.Is(err, ErrTOTPNotEnrolled) {
		t.Errorf("regen before enrol err = %v, want ErrTOTPNotEnrolled", err)
	}
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
	if _, err := decryptTOTPSecret(key, []byte("short")); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("short blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
	blob, err := encryptTOTPSecret(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob[len(blob)-1] ^= 0xFF // tamper
	if _, err := decryptTOTPSecret(key, blob); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("tampered blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
}

// Verifies the per-challenge guess limit: after maxTOTPChallengeAttempts wrong
// codes the challenge is invalidated, so even a correct code can't redeem it
// (the attacker must redo the rate-limited first leg). Closes the "challenge
// survives failed guesses" brute-force gap.
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

	for i := 0; i < maxTOTPChallengeAttempts; i++ {
		if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, wrong, ""); err != ErrTOTPInvalid {
			t.Fatalf("guess %d err = %v, want ErrTOTPInvalid", i+1, err)
		}
	}
	if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, valid, ""); err != ErrChallengeUnknown {
		t.Fatalf("post-cap valid code err = %v, want ErrChallengeUnknown", err)
	}
}

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

// codeForStep generates the code an authenticator would show during the
// given time-step, so tests can address the edges of the skew window
// explicitly instead of only the current step.
func codeForStep(t *testing.T, secret string, step int64) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriodSeconds, 0), totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      totpSkewSteps,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code for step %d: %v", step, err)
	}

	return code
}

// The acceptance window is inclusive on BOTH sides of the current step —
// that is what makes it ~90s wide with a ±1 skew. A window that stopped
// short of cur+skew would still accept a freshly shown code, so only the
// far edge pins the bound.
func TestValidateTOTPStep_AcceptsBothSkewEdges(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	now := time.Unix(1_700_000_000, 0)
	cur := now.Unix() / totpPeriodSeconds

	for _, step := range []int64{cur - totpSkewSteps, cur, cur + totpSkewSteps} {
		got, ok := validateTOTPStep(codeForStep(t, secret, step), secret, now)
		if !ok {
			t.Errorf("step cur%+d rejected, want accepted", step-cur)

			continue
		}
		if got != step {
			t.Errorf("step cur%+d: matched step = %d, want %d", step-cur, got, step)
		}
	}
}

// Step 0 is a real window (the epoch itself). The guard exists to skip
// NEGATIVE steps, which cannot be rendered as a time; treating 0 as out
// of range would silently drop a valid window.
func TestValidateTOTPStep_AcceptsStepZero(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	now := time.Unix(5, 0) // cur == 0, so the window spans steps {-1, 0, 1}
	if cur := now.Unix() / totpPeriodSeconds; cur != 0 {
		t.Fatalf("precondition: cur = %d, want 0", cur)
	}

	got, ok := validateTOTPStep(codeForStep(t, secret, 0), secret, now)
	if !ok {
		t.Fatal("a code for step 0 must validate: the guard skips negative steps only")
	}
	if got != 0 {
		t.Errorf("matched step = %d, want 0", got)
	}
}

func TestDecryptTOTPSecret_NonceSizedBlobReachesGCM(t *testing.T) {
	key := testTOTPKey(t)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonceSize := gcm.NonceSize()

	_, err = decryptTOTPSecret(key, make([]byte, nonceSize-1))
	if !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Fatalf("short blob: err = %v, want ErrTOTPSecretCorrupt", err)
	}
	if err.Error() != ErrTOTPSecretCorrupt.Error() {
		t.Errorf("short blob: err = %q, want the bare sentinel (rejected before GCM)", err)
	}

	_, err = decryptTOTPSecret(key, make([]byte, nonceSize))
	if !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Fatalf("nonce-sized blob: err = %v, want ErrTOTPSecretCorrupt", err)
	}
	if err.Error() == ErrTOTPSecretCorrupt.Error() {
		t.Error("nonce-sized blob: the guard rejected it; a blob with a full nonce must reach GCM")
	}
}
