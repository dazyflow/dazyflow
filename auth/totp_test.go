package auth

import (
	"context"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"
	"time"

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
