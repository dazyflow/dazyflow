// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

// TOTP validation parameters. period 30s with skew ±1 matches the
// enrollment defaults of pquerna/otp's totp.Validate and gives a ~90s
// acceptance window.
const (
	totpPeriodSeconds = 30
	totpSkewSteps     = 1
)

// validateTOTPStep validates code against secret and, on success, returns the
// time-step (unix/period) of the window that matched. Unlike totp.Validate it
// exposes which step accepted, so the caller can persist it and reject a later
// replay of a code from the same (or an earlier) step within its ~90s life.
// The per-step comparison is constant-time.
func validateTOTPStep(code, secret string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, false
	}
	opts := totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      totpSkewSteps,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}
	cur := now.Unix() / totpPeriodSeconds
	matched := int64(0)
	ok := false
	// Iterate the whole window regardless of an early match so timing doesn't
	// leak which step (or whether any) accepted.
	for d := -int64(totpSkewSteps); d <= int64(totpSkewSteps); d++ {
		step := cur + d
		if step < 0 {
			continue
		}
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriodSeconds, 0), opts)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 && !ok {
			matched = step
			ok = true
		}
	}
	return matched, ok
}

// TOTP 2FA for password-authenticated users. The shape mirrors the rest
// of this package: it operates over the UserStore boundary (so it works
// against the JSON-file dev store and the Postgres store alike) and
// keeps an in-memory store for the short-lived login challenge that
// bridges the password step and the second-factor step.
//
// User-visible flow:
//
//	enrol:    EnrolStart → EnrolConfirm (returns recovery codes once)
//	login:    VerifyPassword → IssueTOTPChallenge → ConsumeTOTPChallenge
//	disable:  DisableTOTP
//
// The secret is encrypted at rest with AES-256-GCM using a key from the
// DAZYFLOW_TOTP_KEY env var. Losing the key forces every enrolled user
// to re-enrol — operators should treat it like a database credential.

// TOTPChallengeTTL bounds the bridge token between the password step and
// the TOTP step. Five minutes is generous enough for a fumble with the
// authenticator app, tight enough that a leaked challenge doesn't sit on
// the wire for long.
const TOTPChallengeTTL = 5 * time.Minute

// TOTPIssuer is the label the authenticator app shows next to the
// account row. Matches the product name; authenticator apps merge
// entries by (issuer, account) anyway, so it isn't configurable.
const TOTPIssuer = "Dazyflow"

// totpRecoveryCodeCount is how many single-use recovery codes are minted
// per enrolment. Ten balances "enough for a few lockouts" against "few
// enough that operators don't sweep long tables."
const totpRecoveryCodeCount = 10

// totpRecoveryCodeAlphabet uses lowercase a–z + 2–9, dropping 0/1/o/l to
// dodge hand-transcription confusables.
const totpRecoveryCodeAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// totpEnvKey is the env var holding the base64-encoded 32-byte AES key.
const totpEnvKey = "DAZYFLOW_TOTP_KEY"

// Errors returned by the TOTP layer. Handlers map these to HTTP statuses
// + machine-readable error codes.
var (
	ErrTOTPKeyMissing      = errors.New("DAZYFLOW_TOTP_KEY is not configured")
	ErrTOTPKeyMalformed    = errors.New("DAZYFLOW_TOTP_KEY is malformed (need 32 bytes base64)")
	ErrTOTPNotEnrolled     = errors.New("totp not enrolled")
	ErrTOTPAlreadyEnrolled = errors.New("totp already enrolled")
	ErrTOTPInvalid         = errors.New("invalid totp code")
	ErrChallengeUnknown    = errors.New("totp challenge unknown")
	ErrChallengeExpired    = errors.New("totp challenge expired")
	ErrRecoveryCodeInvalid = errors.New("recovery code invalid")
	ErrTOTPSecretCorrupt   = errors.New("totp secret could not be decrypted")
)

// LoadTOTPKey decodes DAZYFLOW_TOTP_KEY into a 32-byte AES key. Called
// once at server boot — any misconfiguration fails fast with a clear
// error rather than ambushing the first user trying to enrol. Returns
// ErrTOTPKeyMissing when the env var is unset; the daemon treats that as
// "2FA is disabled at the install level" and routes /totp endpoints to a
// 503.
func LoadTOTPKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(totpEnvKey))
	if raw == "" {
		return nil, ErrTOTPKeyMissing
	}
	// Accept standard or URL base64, padded or not, so any of
	// `openssl rand -base64 32` or whatever the operator's key store
	// produced round-trips.
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(raw); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("%w: got %d bytes, want 32", ErrTOTPKeyMalformed, len(b))
			}
			return b, nil
		}
	}
	return nil, ErrTOTPKeyMalformed
}

// encryptTOTPSecret wraps the base32 secret with AES-256-GCM. The
// 12-byte random nonce is prepended to the ciphertext so the stored blob
// is self-contained — decryptTOTPSecret slices it back off.
func encryptTOTPSecret(key []byte, plaintextBase32 string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintextBase32), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

func decryptTOTPSecret(key, blob []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", ErrTOTPSecretCorrupt
	}
	nonce := blob[:gcm.NonceSize()]
	ct := blob[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTOTPSecretCorrupt, err)
	}
	return string(pt), nil
}

// TOTPSetup is what EnrolStart returns: the otpauth:// URL the client
// renders as a QR code, a server-encoded PNG of the same URL the
// frontend can drop straight into an <img src=…>, and the manual base32
// secret a user can type if their authenticator app can't scan.
//
// The QR is encoded server-side so the frontend doesn't need a QR
// library. ~3 KB base64 for a 256x256 PNG; sent once per enrolment.
type TOTPSetup struct {
	OTPAuthURL   string
	SecretBase32 string
	QRPNGDataURL string
}

// encodeQR renders an otpauth URL as a base64-encoded PNG suitable for
// direct use in an <img src=…> attribute. Medium recovery is the
// canonical TOTP-QR choice — the URLs are short enough that a higher
// level isn't needed.
func encodeQR(otpauthURL string) (string, error) {
	png, err := qrcode.Encode(otpauthURL, qrcode.Medium, 256)
	if err != nil {
		return "", fmt.Errorf("qr encode: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// EnrolStart generates a fresh TOTP secret for the user, stores it
// encrypted with TOTPEnabled=false, and returns the provisioning URL +
// manual secret. Idempotent: a second call before confirm replaces any
// prior pending secret. Refuses if the user is already enrolled — they
// must disable first to re-enrol.
func EnrolStart(ctx context.Context, store UserStore, key []byte, email string) (TOTPSetup, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return TOTPSetup{}, err
	}
	if u.TOTPEnabled {
		return TOTPSetup{}, ErrTOTPAlreadyEnrolled
	}
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: u.Email,
	})
	if err != nil {
		return TOTPSetup{}, err
	}
	enc, err := encryptTOTPSecret(key, k.Secret())
	if err != nil {
		return TOTPSetup{}, err
	}
	u.TOTPSecretEnc = enc
	u.TOTPEnabled = false
	u.TOTPEnrolledAt = nil
	if err := store.PutUser(ctx, u); err != nil {
		return TOTPSetup{}, err
	}
	otpauth := k.URL()
	qr, err := encodeQR(otpauth)
	if err != nil {
		// Don't fail enrolment: the user can still copy the manual
		// secret. The frontend hides the <img> when the field is empty.
		qr = ""
	}
	return TOTPSetup{
		OTPAuthURL:   otpauth,
		SecretBase32: k.Secret(),
		QRPNGDataURL: qr,
	}, nil
}

// EnrolConfirm verifies the first TOTP code against the pending secret,
// flips TOTPEnabled=true, and mints totpRecoveryCodeCount single-use
// recovery codes. Plaintext codes are returned ONCE for the caller to
// display; only their bcrypt hashes are retained.
func EnrolConfirm(ctx context.Context, store UserStore, key []byte, email, code string) ([]string, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if u.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnrolled
	}
	if len(u.TOTPSecretEnc) == 0 {
		return nil, ErrTOTPNotEnrolled
	}
	secret, err := decryptTOTPSecret(key, u.TOTPSecretEnc)
	if err != nil {
		return nil, err
	}
	if _, ok := validateTOTPStep(code, secret, time.Now()); !ok {
		return nil, ErrTOTPInvalid
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	u.TOTPEnabled = true
	u.TOTPEnrolledAt = &now
	u.RecoveryCodeHashes = hashes
	// Note: we deliberately do NOT burn the enrollment code's step here.
	// Enrollment confirm runs inside an already-authenticated session, and
	// burning it would reject a legitimate first sign-in that lands in the
	// same ~90s window. Replay protection applies to the sign-in leg
	// (ConsumeTOTPChallenge), which is the code an attacker would observe.
	if err := store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP clears the secret, the enabled flag, and the recovery
// codes for the user. Callers gate this behind a fresh password compare
// so a stolen session cookie can't silently turn 2FA off.
func DisableTOTP(ctx context.Context, store UserStore, email string) error {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return err
	}
	u.TOTPSecretEnc = nil
	u.TOTPEnabled = false
	u.TOTPEnrolledAt = nil
	u.RecoveryCodeHashes = nil
	return store.PutUser(ctx, u)
}

// RegenerateRecoveryCodes drops every existing code for the user and
// mints a fresh set. Returns the plaintext codes for one-time display.
// Refuses if the user isn't enrolled — there's no useful "regenerate
// while disabled" path.
func RegenerateRecoveryCodes(ctx context.Context, store UserStore, email string) ([]string, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if !u.TOTPEnabled {
		return nil, ErrTOTPNotEnrolled
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	u.RecoveryCodeHashes = hashes
	if err := store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	return codes, nil
}

// TOTPStatus describes the current 2FA state for a user, surfaced to the
// Settings UI so it can render the right card.
type TOTPStatus struct {
	Enabled           bool
	EnrolledAt        *time.Time
	RecoveryCodesLeft int
}

// LoadTOTPStatus returns the current TOTP state for a user.
func LoadTOTPStatus(ctx context.Context, store UserStore, email string) (TOTPStatus, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return TOTPStatus{}, err
	}
	return TOTPStatus{
		Enabled:           u.TOTPEnabled,
		EnrolledAt:        u.TOTPEnrolledAt,
		RecoveryCodesLeft: len(u.RecoveryCodeHashes),
	}, nil
}

// ---------------------------------------------------------------------------
// Login challenge
// ---------------------------------------------------------------------------

// Factor identifies which second factor cleared the TOTP step. The
// caller branches on it (e.g. to warn "you just used a recovery code").
type Factor string

const (
	FactorTOTP         Factor = "totp"
	FactorRecoveryCode Factor = "recovery_code"
)

// maxTOTPChallengeAttempts caps wrong second-factor guesses against a single
// challenge before it is invalidated. Without it a challenge lives the full
// TOTPChallengeTTL and accepts unlimited guesses, so the only brake on a
// brute-force pass is the per-IP rate limit — which a distributed attacker
// sidesteps. Five is generous for a human fat-fingering a 6-digit code while
// keeping the per-challenge guess budget far below the 10^6 space.
const maxTOTPChallengeAttempts = 5

// TOTPChallenge is the server-side record bridging the first sign-in step
// (password OR verified SSO) to the second-factor step. It carries the subject
// email — the principal is rebuilt from the (verified) user record at consume
// time — plus a failed-attempt counter, and an OPTIONAL resolved-org override
// used by the SSO leg so the second factor lands the user in the org they were
// signing into rather than their home org.
type TOTPChallenge struct {
	Email     string
	ExpiresAt time.Time
	// Attempts counts wrong code/recovery guesses; at maxTOTPChallengeAttempts
	// the challenge is dropped (the user must re-authenticate).
	Attempts int
	// Tenant/Workspace/Roles, when Tenant != "", override the redeemed user's
	// home org at session-issue time (set by the SSO leg via
	// IssueTOTPChallengeWithOrg). Empty for the password leg.
	Tenant    string
	Workspace string
	Roles     []core.Role
}

// TOTPChallengeStore is the lookup boundary for login challenges. The
// in-memory implementation suffices: challenges live five minutes and
// losing them on restart just means the user re-enters their password,
// which matches MemSessionStore's posture.
type TOTPChallengeStore interface {
	Put(ctx context.Context, token string, c TOTPChallenge) error
	Get(ctx context.Context, token string) (TOTPChallenge, error)
	Delete(ctx context.Context, token string) error
}

// MemTOTPChallengeStore keeps challenges in process memory. It sweeps
// expired entries lazily on access so a burst of abandoned logins
// doesn't accumulate forever.
type MemTOTPChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]TOTPChallenge
}

func NewMemTOTPChallengeStore() *MemTOTPChallengeStore {
	return &MemTOTPChallengeStore{challenges: make(map[string]TOTPChallenge)}
}

func (s *MemTOTPChallengeStore) Put(_ context.Context, token string, c TOTPChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.challenges[token] = c
	return nil
}

func (s *MemTOTPChallengeStore) Get(_ context.Context, token string) (TOTPChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[token]
	if !ok {
		return TOTPChallenge{}, ErrChallengeUnknown
	}
	return c, nil
}

func (s *MemTOTPChallengeStore) Delete(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.challenges, token)
	return nil
}

func (s *MemTOTPChallengeStore) sweepLocked() {
	now := time.Now()
	for tok, c := range s.challenges {
		if now.After(c.ExpiresAt) {
			delete(s.challenges, tok)
		}
	}
}

// IssueTOTPChallenge mints a challenge token for a user who has cleared
// the password step but still owes a second factor. The token is handed
// to the client in the sign-in response; ConsumeTOTPChallenge redeems it.
func IssueTOTPChallenge(ctx context.Context, store TOTPChallengeStore, email string) (string, error) {
	return IssueTOTPChallengeWithOrg(ctx, store, email, "", "", nil)
}

// IssueTOTPChallengeWithOrg is IssueTOTPChallenge plus a resolved-org override
// to carry through the second factor. The SSO leg uses it so a 2FA user lands
// in the org they signed into (its membership roles), not their home org; pass
// empty tenant for the password leg (equivalent to IssueTOTPChallenge).
func IssueTOTPChallengeWithOrg(ctx context.Context, store TOTPChallengeStore, email, tenant, workspace string, roles []core.Role) (string, error) {
	tok, err := newChallengeToken()
	if err != nil {
		return "", err
	}
	c := TOTPChallenge{
		Email:     email,
		ExpiresAt: time.Now().Add(TOTPChallengeTTL),
		Tenant:    tenant,
		Workspace: workspace,
		Roles:     roles,
	}
	if err := store.Put(ctx, tok, c); err != nil {
		return "", err
	}
	return tok, nil
}

// TOTPChallengeResult is what ConsumeTOTPChallenge returns on success:
// the user whose challenge was redeemed and which factor cleared it.
type TOTPChallengeResult struct {
	User   User
	Factor Factor
}

// ConsumeTOTPChallenge looks up a challenge token, validates expiry,
// checks either the supplied TOTP code or a recovery code, deletes the
// challenge on success (single-use), and returns the redeemed user. Pass
// exactly one of code / recoveryCode.
//
// Callers are responsible for rate-limiting by IP so a brute-force pass
// can't enumerate the 10^6 TOTP space trivially.
func ConsumeTOTPChallenge(ctx context.Context, challenges TOTPChallengeStore, users UserStore, key []byte, token, code, recoveryCode string) (TOTPChallengeResult, error) {
	if token == "" {
		return TOTPChallengeResult{}, ErrChallengeUnknown
	}
	c, err := challenges.Get(ctx, token)
	if err != nil {
		return TOTPChallengeResult{}, err
	}
	if time.Now().After(c.ExpiresAt) {
		_ = challenges.Delete(ctx, token)
		return TOTPChallengeResult{}, ErrChallengeExpired
	}
	u, err := users.GetByEmail(ctx, c.Email)
	if err != nil {
		return TOTPChallengeResult{}, err
	}
	if !u.TOTPEnabled || len(u.TOTPSecretEnc) == 0 {
		// Race: user disabled TOTP between login leg 1 and leg 2.
		return TOTPChallengeResult{}, ErrTOTPNotEnrolled
	}

	// recordWrongGuess bounds brute force against this challenge: each wrong
	// code/recovery guess increments the counter, and at the cap the challenge
	// is dropped so further guesses get ErrChallengeUnknown — the attacker must
	// redo the (rate-limited, password/SSO-gated) first leg to get a new one.
	recordWrongGuess := func() {
		c.Attempts++
		if c.Attempts >= maxTOTPChallengeAttempts {
			_ = challenges.Delete(ctx, token)
			return
		}
		_ = challenges.Put(ctx, token, c)
	}

	var factor Factor
	switch {
	case strings.TrimSpace(code) != "":
		secret, derr := decryptTOTPSecret(key, u.TOTPSecretEnc)
		if derr != nil {
			return TOTPChallengeResult{}, derr
		}
		step, ok := validateTOTPStep(code, secret, time.Now())
		if !ok {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrTOTPInvalid
		}
		// Replay protection: a TOTP code stays valid for ~90s. Reject a
		// code from a step we have already consumed (or an earlier one) so
		// an observed/sniffed code can't be redeemed a second time inside
		// its window. Persist the consumed step before completing login.
		if u.TOTPLastStep != 0 && step <= u.TOTPLastStep {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrTOTPInvalid
		}
		u.TOTPLastStep = step
		if err := users.PutUser(ctx, u); err != nil {
			return TOTPChallengeResult{}, err
		}
		factor = FactorTOTP
	case strings.TrimSpace(recoveryCode) != "":
		remaining, ok := consumeRecoveryCode(u.RecoveryCodeHashes, recoveryCode)
		if !ok {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrRecoveryCodeInvalid
		}
		// Persist the burned code before completing login so a replay of
		// the same recovery code can't clear a second challenge.
		u.RecoveryCodeHashes = remaining
		if err := users.PutUser(ctx, u); err != nil {
			return TOTPChallengeResult{}, err
		}
		factor = FactorRecoveryCode
	default:
		return TOTPChallengeResult{}, ErrTOTPInvalid
	}

	// Single-use: drop the challenge so it can't be redeemed twice.
	_ = challenges.Delete(ctx, token)
	// Apply the SSO leg's resolved-org override (if any) so the session is
	// issued against the org the user signed into, matching the non-2FA SSO
	// path. The password leg leaves these empty → the user's home org stands.
	if c.Tenant != "" {
		u.Tenant = c.Tenant
		u.Workspace = c.Workspace
		u.Roles = c.Roles
	}
	return TOTPChallengeResult{User: u, Factor: factor}, nil
}

// ---------------------------------------------------------------------------
// Recovery codes
// ---------------------------------------------------------------------------

// consumeRecoveryCode finds a stored hash matching the supplied plaintext
// and returns the slice with that hash removed plus ok=true. Every hash
// is compared regardless of an early match so the response time is a
// function of how many codes the user has left, not which one matched —
// an early break would leak both "you guessed right" and "the user has N
// codes left" via timing.
func consumeRecoveryCode(hashes []string, plaintext string) ([]string, bool) {
	plaintext = canonicaliseRecoveryCode(plaintext)
	matchIdx := -1
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(plaintext)) == nil && matchIdx == -1 {
			matchIdx = i
			// keep iterating to drain the timing budget
		}
	}
	if matchIdx == -1 {
		return hashes, false
	}
	remaining := make([]string, 0, len(hashes)-1)
	remaining = append(remaining, hashes[:matchIdx]...)
	remaining = append(remaining, hashes[matchIdx+1:]...)
	return remaining, true
}

// generateRecoveryCodes mints totpRecoveryCodeCount codes formatted
// xxxx-xxxx (lowercase, 8 chars + a dash) and their bcrypt hashes.
// Returns plaintext + hashes; the plaintext is shown once, the hashes
// are stored.
func generateRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, totpRecoveryCodeCount)
	hashes := make([]string, totpRecoveryCodeCount)
	for i := range codes {
		raw, err := randomRecoveryCodeChars(8)
		if err != nil {
			return nil, nil, err
		}
		codes[i] = raw[:4] + "-" + raw[4:]
		// Hash the canonical form so case + dashes at redemption time
		// don't matter — people reading codes off paper fumble.
		h, err := bcrypt.GenerateFromPassword(
			[]byte(canonicaliseRecoveryCode(codes[i])),
			bcrypt.DefaultCost,
		)
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = string(h)
	}
	return codes, hashes, nil
}

func randomRecoveryCodeChars(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	alpha := totpRecoveryCodeAlphabet
	for i, x := range b {
		out[i] = alpha[int(x)%len(alpha)]
	}
	return string(out), nil
}

// canonicaliseRecoveryCode normalises user input before hash comparison:
// lowercase, drop dashes/spaces. The displayed form includes a dash for
// readability; an "ABCD EFGH" fumble still matches the stored hash for
// "abcdefgh".
func canonicaliseRecoveryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// newChallengeToken returns 24 random bytes URL-safe-base64-encoded (192
// bits of entropy — overkill for a 5-minute token, matches the rest of
// the auth code).
func newChallengeToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
